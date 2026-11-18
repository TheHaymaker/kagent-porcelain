package store

import (
	"context"
	"encoding/json"
	"time"
)

// AuditEvent is one entry of the append-only record.
type AuditEvent struct {
	Seq          int64
	ID           string
	Time         time.Time
	Type         string
	ActorID      string
	OnBehalfOfID string
	SessionID    string
	TaskID       string
	AppID        string
	ApprovalID   string
	Summary      string
	Detail       map[string]any
	Surface      string
}

// AppendAudit adds an event.
func (s *Store) AppendAudit(ctx context.Context, e AuditEvent) (AuditEvent, error) {
	if e.ID == "" {
		e.ID = NewID("evt")
	}
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	detail, err := json.Marshal(e.Detail)
	if err != nil {
		return e, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO audit_event (id, type, actor_id, on_behalf_of_id, session_id, task_id, app_id, approval_id, summary, detail, surface)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING seq, time`,
		e.ID, e.Type, e.ActorID, e.OnBehalfOfID, e.SessionID, e.TaskID, e.AppID, e.ApprovalID, e.Summary, detail, e.Surface).
		Scan(&e.Seq, &e.Time)
	return e, mapErr(err)
}

// AuditFilter narrows ListAudit. VisibleTo, when set, restricts results to
// events on sessions and apps that person owns, or that they acted in.
type AuditFilter struct {
	SessionID  string
	AppID      string
	ActorID    string
	TypePrefix string
	Since      time.Time
	Until      time.Time
	VisibleTo  string
	// AfterSeq lists only events after this sequence number.
	AfterSeq int64
	// Ascending lists oldest first, as an exporter needs.
	Ascending bool
}

const auditColumns = `seq, id, time, type, actor_id, on_behalf_of_id, session_id, task_id, app_id, approval_id, summary, detail, surface`

// ListAudit lists events newest first, or oldest first with Ascending.
func (s *Store) ListAudit(ctx context.Context, f AuditFilter, page Page) ([]AuditEvent, error) {
	order := "seq DESC"
	if f.Ascending {
		order = "seq ASC"
	}
	until := f.Until
	if until.IsZero() {
		until = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+auditColumns+` FROM audit_event e
		WHERE ($1 = '' OR session_id = $1) AND ($2 = '' OR app_id = $2) AND ($3 = '' OR actor_id = $3)
		  AND ($4 = '' OR type LIKE $4 || '%') AND time >= $5 AND time < $6 AND seq > $7
		  AND ($8 = '' OR actor_id = $8 OR on_behalf_of_id = $8
		       OR session_id IN (SELECT session_id FROM watched_session WHERE requester_id = $8)
		       OR app_id IN (SELECT id FROM app WHERE owner_id = $8)
		       OR approval_id IN (SELECT id FROM approval WHERE requester_id = $8))
		ORDER BY `+order+` LIMIT $9 OFFSET $10`,
		f.SessionID, f.AppID, f.ActorID, f.TypePrefix, f.Since, until, f.AfterSeq, f.VisibleTo, page.limit(), page.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var detail []byte
		if err := rows.Scan(&e.Seq, &e.ID, &e.Time, &e.Type, &e.ActorID, &e.OnBehalfOfID, &e.SessionID, &e.TaskID, &e.AppID, &e.ApprovalID, &e.Summary, &detail, &e.Surface); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(detail, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ExportCursor returns how far a named exporter has shipped.
func (s *Store) ExportCursor(ctx context.Context, name string) (int64, error) {
	var seq int64
	err := s.pool.QueryRow(ctx, `SELECT last_seq FROM export_cursor WHERE name = $1`, name).Scan(&seq)
	if mapErr(err) == ErrNotFound {
		return 0, nil
	}
	return seq, err
}

// SetExportCursor records how far a named exporter has shipped. It never moves backwards.
func (s *Store) SetExportCursor(ctx context.Context, name string, seq int64) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO export_cursor (name, last_seq) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET last_seq = GREATEST(export_cursor.last_seq, EXCLUDED.last_seq)`, name, seq)
	return err
}
