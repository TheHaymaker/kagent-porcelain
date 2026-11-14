package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
)

// Approval states.
const (
	ApprovalPending    = "pending"
	ApprovalApproved   = "approved"
	ApprovalRejected   = "rejected"
	ApprovalSuperseded = "superseded"
)

// Call is one tool call an approval covers, with its kind.
type Call struct {
	hitl.Tool
	Kind policy.Kind `json:"kind"`
}

// Approval wraps one kagent HITL pause.
type Approval struct {
	ID                 string
	Fingerprint        string
	State              string
	SessionID          string
	TaskID             string
	Agent              string
	RequesterID        string
	Kind               policy.Kind
	Hint               string
	Calls              []Call
	ApproverGroups     []string
	ApproverIDs        []string
	RequesterMayDecide bool
	AppID              string
	DecidedByID        string
	Decisions          []hitl.Decision
	Reason             string
	Delivered          bool
	CreatedAt          time.Time
	DecidedAt          *time.Time
}

// Routing returns the approval's routing for permission checks.
func (a Approval) Routing() policy.Routing {
	return policy.Routing{Kind: a.Kind, RequesterID: a.RequesterID, ApproverGroups: a.ApproverGroups,
		ApproverIDs: a.ApproverIDs, RequesterMayDecide: a.RequesterMayDecide}
}

// Request rebuilds the HITL request the approval answers.
func (a Approval) Request() hitl.Request {
	tools := make([]hitl.Tool, len(a.Calls))
	for i, c := range a.Calls {
		tools[i] = c.Tool
	}
	return hitl.Request{Kind: hitl.KindToolApproval, TaskID: a.TaskID, ContextID: a.SessionID, Hint: a.Hint, Tools: tools}
}

const approvalColumns = `id, fingerprint, state, session_id, task_id, agent, requester_id, kind, hint, calls,
	approver_groups, approver_ids, requester_may_decide, app_id, decided_by_id, decisions, reason, delivered,
	created_at, decided_at`

func scanApproval(row pgx.Row) (Approval, error) {
	var a Approval
	var calls, decisions []byte
	err := row.Scan(&a.ID, &a.Fingerprint, &a.State, &a.SessionID, &a.TaskID, &a.Agent, &a.RequesterID, &a.Kind, &a.Hint,
		&calls, &a.ApproverGroups, &a.ApproverIDs, &a.RequesterMayDecide, &a.AppID, &a.DecidedByID, &decisions,
		&a.Reason, &a.Delivered, &a.CreatedAt, &a.DecidedAt)
	if err != nil {
		return a, mapErr(err)
	}
	if err := json.Unmarshal(calls, &a.Calls); err != nil {
		return a, fmt.Errorf("approval %s calls: %w", a.ID, err)
	}
	if err := json.Unmarshal(decisions, &a.Decisions); err != nil {
		return a, fmt.Errorf("approval %s decisions: %w", a.ID, err)
	}
	return a, nil
}

func collectApprovals(rows pgx.Rows, err error) ([]Approval, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateApproval inserts an approval unless one with the same fingerprint
// exists, in which case it returns the existing one and created=false.
func (s *Store) CreateApproval(ctx context.Context, a Approval) (Approval, bool, error) {
	if a.ID == "" {
		a.ID = NewID("apr")
	}
	calls, err := json.Marshal(a.Calls)
	if err != nil {
		return a, false, err
	}
	if a.ApproverGroups == nil {
		a.ApproverGroups = []string{}
	}
	if a.ApproverIDs == nil {
		a.ApproverIDs = []string{}
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO approval (id, fingerprint, state, session_id, task_id, agent, requester_id, kind, hint, calls,
			approver_groups, approver_ids, requester_may_decide, app_id)
		VALUES ($1, $2, 'pending', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (fingerprint) DO NOTHING
		RETURNING `+approvalColumns,
		a.ID, a.Fingerprint, a.SessionID, a.TaskID, a.Agent, a.RequesterID, a.Kind, a.Hint, calls,
		a.ApproverGroups, a.ApproverIDs, a.RequesterMayDecide, a.AppID)
	created, err := scanApproval(row)
	if err == nil {
		return created, true, nil
	}
	if err != ErrNotFound {
		return a, false, err
	}
	existing, err := s.GetApprovalByFingerprint(ctx, a.Fingerprint)
	return existing, false, err
}

// GetApproval returns an approval by ID.
func (s *Store) GetApproval(ctx context.Context, id string) (Approval, error) {
	return scanApproval(s.pool.QueryRow(ctx, `SELECT `+approvalColumns+` FROM approval WHERE id = $1`, id))
}

// GetApprovalByFingerprint returns the approval for an exact pause.
func (s *Store) GetApprovalByFingerprint(ctx context.Context, fp string) (Approval, error) {
	return scanApproval(s.pool.QueryRow(ctx, `SELECT `+approvalColumns+` FROM approval WHERE fingerprint = $1`, fp))
}

// ListSessionApprovals returns a session's approvals, oldest first.
func (s *Store) ListSessionApprovals(ctx context.Context, sessionID string) ([]Approval, error) {
	return collectApprovals(s.pool.Query(ctx, `SELECT `+approvalColumns+` FROM approval WHERE session_id = $1 ORDER BY created_at`, sessionID))
}

// ListPendingForDecider returns pending approvals a decider may see as theirs
// to decide: routed to one of their groups or to them by name, excluding
// their own requests unless the requester may decide. MayDecide remains the
// authority; this query only narrows the candidates.
func (s *Store) ListPendingForDecider(ctx context.Context, personID string, groups []string, page Page) ([]Approval, error) {
	if groups == nil {
		groups = []string{}
	}
	return collectApprovals(s.pool.Query(ctx, `SELECT `+approvalColumns+` FROM approval
		WHERE state = 'pending' AND (approver_groups && $2 OR $1 = ANY(approver_ids) OR (requester_id = $1 AND requester_may_decide))
		ORDER BY created_at LIMIT $3 OFFSET $4`, personID, groups, page.limit(), page.Offset))
}

// ListByRequester returns approvals on a requester's conversations, newest first.
func (s *Store) ListByRequester(ctx context.Context, personID string, page Page) ([]Approval, error) {
	return collectApprovals(s.pool.Query(ctx, `SELECT `+approvalColumns+` FROM approval WHERE requester_id = $1
		ORDER BY created_at DESC LIMIT $2 OFFSET $3`, personID, page.limit(), page.Offset))
}

// ListDecidedBy returns approvals a person decided, newest first.
func (s *Store) ListDecidedBy(ctx context.Context, personID string, page Page) ([]Approval, error) {
	return collectApprovals(s.pool.Query(ctx, `SELECT `+approvalColumns+` FROM approval WHERE decided_by_id = $1
		ORDER BY decided_at DESC LIMIT $2 OFFSET $3`, personID, page.limit(), page.Offset))
}

// ListUndelivered returns decided approvals whose answer has not yet reached the task.
func (s *Store) ListUndelivered(ctx context.Context, limit int) ([]Approval, error) {
	return collectApprovals(s.pool.Query(ctx, `SELECT `+approvalColumns+` FROM approval
		WHERE state IN ('approved', 'rejected') AND NOT delivered ORDER BY decided_at LIMIT $1`, limit))
}

// Decide records a decision on a pending approval. It fails with ErrConflict
// if the approval is no longer pending, so two deciders cannot both win.
func (s *Store) Decide(ctx context.Context, id, deciderID string, approved bool, decisions []hitl.Decision, reason string) (Approval, error) {
	state := ApprovalRejected
	if approved {
		state = ApprovalApproved
	}
	d, err := json.Marshal(decisions)
	if err != nil {
		return Approval{}, err
	}
	a, err := scanApproval(s.pool.QueryRow(ctx, `
		UPDATE approval SET state = $2, decided_by_id = $3, decisions = $4, reason = $5, decided_at = now()
		WHERE id = $1 AND state = 'pending'
		RETURNING `+approvalColumns, id, state, deciderID, d, reason))
	if err == ErrNotFound {
		if _, getErr := s.GetApproval(ctx, id); getErr != nil {
			return Approval{}, getErr
		}
		return Approval{}, fmt.Errorf("%w: approval %s is no longer pending", ErrConflict, id)
	}
	return a, err
}

// MarkDelivered records that a decision reached the paused task.
func (s *Store) MarkDelivered(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE approval SET delivered = true WHERE id = $1`, id)
	return err
}

// SupersedePending marks a task's pending approvals superseded, except the
// one with keepFingerprint. It returns the approvals it changed.
func (s *Store) SupersedePending(ctx context.Context, taskID, keepFingerprint string) ([]Approval, error) {
	return collectApprovals(s.pool.Query(ctx, `UPDATE approval SET state = 'superseded', decided_at = now()
		WHERE task_id = $1 AND state = 'pending' AND fingerprint <> $2 RETURNING `+approvalColumns, taskID, keepFingerprint))
}

// Reassign adds a named approver to a pending approval.
func (s *Store) Reassign(ctx context.Context, id, assigneeID string) (Approval, error) {
	a, err := scanApproval(s.pool.QueryRow(ctx, `
		UPDATE approval SET approver_ids = array_append(array_remove(approver_ids, $2), $2)
		WHERE id = $1 AND state = 'pending' RETURNING `+approvalColumns, id, assigneeID))
	if err == ErrNotFound {
		return Approval{}, fmt.Errorf("%w: approval %s is no longer pending", ErrConflict, id)
	}
	return a, err
}

// LatestApprovalForApp returns the newest approval tied to an app, if any.
func (s *Store) LatestApprovalForApp(ctx context.Context, appID string) (Approval, error) {
	return scanApproval(s.pool.QueryRow(ctx, `SELECT `+approvalColumns+` FROM approval WHERE app_id = $1 ORDER BY created_at DESC LIMIT 1`, appID))
}

// RememberedAnswer is an answer applied automatically to identical later calls.
type RememberedAnswer struct {
	ID        string
	PersonID  string
	Scope     string // "conversation" or "always"
	ToolName  string
	Agent     string
	SessionID string
	Approved  bool
	CreatedAt time.Time
}

// Remember stores (or replaces) a remembered answer.
func (s *Store) Remember(ctx context.Context, r RememberedAnswer) (RememberedAnswer, error) {
	if r.Scope == "always" {
		r.SessionID = ""
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO remembered_answer (id, person_id, scope, tool_name, agent, session_id, approved)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (person_id, scope, tool_name, agent, session_id) DO UPDATE SET approved = EXCLUDED.approved, created_at = now()
		RETURNING id, created_at`, NewID("rem"), r.PersonID, r.Scope, r.ToolName, r.Agent, r.SessionID, r.Approved).
		Scan(&r.ID, &r.CreatedAt)
	return r, mapErr(err)
}

// FindRemembered returns the answer that applies to a call, preferring the
// conversation-scoped one.
func (s *Store) FindRemembered(ctx context.Context, personID, agent, sessionID, tool string) (RememberedAnswer, error) {
	var r RememberedAnswer
	err := s.pool.QueryRow(ctx, `
		SELECT id, person_id, scope, tool_name, agent, session_id, approved, created_at FROM remembered_answer
		WHERE person_id = $1 AND agent = $2 AND tool_name = $4 AND (scope = 'always' OR session_id = $3)
		ORDER BY (scope = 'conversation') DESC LIMIT 1`, personID, agent, sessionID, tool).
		Scan(&r.ID, &r.PersonID, &r.Scope, &r.ToolName, &r.Agent, &r.SessionID, &r.Approved, &r.CreatedAt)
	return r, mapErr(err)
}

// ListRemembered returns a person's remembered answers.
func (s *Store) ListRemembered(ctx context.Context, personID string) ([]RememberedAnswer, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, person_id, scope, tool_name, agent, session_id, approved, created_at
		FROM remembered_answer WHERE person_id = $1 ORDER BY created_at DESC`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RememberedAnswer
	for rows.Next() {
		var r RememberedAnswer
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Scope, &r.ToolName, &r.Agent, &r.SessionID, &r.Approved, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Forget deletes one of a person's remembered answers.
func (s *Store) Forget(ctx context.Context, personID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM remembered_answer WHERE person_id = $1 AND id = $2`, personID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
