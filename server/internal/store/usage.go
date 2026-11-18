package store

import (
	"context"
	"fmt"
	"time"
)

// UsageRecord is one model call as the gateway reported it.
type UsageRecord struct {
	Time         time.Time
	PersonID     string
	SessionID    string
	AppID        string
	Model        string
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
	// RequestID makes a repeated report of the same call a no-op.
	RequestID string
}

// RecordUsage stores a usage record. A repeated request ID is ignored.
func (s *Store) RecordUsage(ctx context.Context, u UsageRecord) error {
	if u.Time.IsZero() {
		u.Time = s.now()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO usage_record (time, person_id, session_id, app_id, model, input_tokens, output_tokens, cost_micros, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (request_id) WHERE request_id <> '' DO NOTHING`,
		u.Time, u.PersonID, u.SessionID, u.AppID, u.Model, u.InputTokens, u.OutputTokens, u.CostMicros, u.RequestID)
	return mapErr(err)
}

// UsageLine is one row of a usage summary.
type UsageLine struct {
	Key          string
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
}

// UsageGroup is a grouping for SummariseUsage.
type UsageGroup string

const (
	ByPerson UsageGroup = "person_id"
	ByApp    UsageGroup = "app_id"
	ByModel  UsageGroup = "model"
	ByDay    UsageGroup = "day"
)

// SummariseUsage totals usage in [since, until), optionally for one person.
func (s *Store) SummariseUsage(ctx context.Context, since, until time.Time, group UsageGroup, personID string) ([]UsageLine, error) {
	key := ""
	switch group {
	case ByPerson, ByApp, ByModel:
		key = string(group)
	case ByDay, "":
		key = "to_char(date_trunc('day', time), 'YYYY-MM-DD')"
	default:
		return nil, fmt.Errorf("unknown usage grouping %q", group)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+key+` AS k, sum(input_tokens), sum(output_tokens), sum(cost_micros)
		FROM usage_record WHERE time >= $1 AND time < $2 AND ($3 = '' OR person_id = $3) GROUP BY k ORDER BY k`, since, until, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageLine
	for rows.Next() {
		var l UsageLine
		if err := rows.Scan(&l.Key, &l.InputTokens, &l.OutputTokens, &l.CostMicros); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SpentMicros returns what a set of people spent since a time.
func (s *Store) SpentMicros(ctx context.Context, personIDs []string, since time.Time) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(cost_micros), 0) FROM usage_record WHERE person_id = ANY($1) AND time >= $2`, personIDs, since).Scan(&total)
	return total, err
}

// AppSpentMicros returns what an app's builds cost since a time.
func (s *Store) AppSpentMicros(ctx context.Context, appIDs []string, since time.Time) (map[string]int64, error) {
	out := map[string]int64{}
	if len(appIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT app_id, sum(cost_micros) FROM usage_record WHERE app_id = ANY($1) AND time >= $2 GROUP BY app_id`, appIDs, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var v int64
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

// Limit is a monthly spend limit.
type Limit struct {
	Subject      string // "person" or "team"
	SubjectID    string
	MonthlyCents int64
}

// SetLimit stores a limit.
func (s *Store) SetLimit(ctx context.Context, l Limit) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO spend_limit (subject, subject_id, monthly_cents) VALUES ($1, $2, $3)
		ON CONFLICT (subject, subject_id) DO UPDATE SET monthly_cents = EXCLUDED.monthly_cents`, l.Subject, l.SubjectID, l.MonthlyCents)
	return mapErr(err)
}

// Limits lists all limits.
func (s *Store) Limits(ctx context.Context) ([]Limit, error) {
	rows, err := s.pool.Query(ctx, `SELECT subject, subject_id, monthly_cents FROM spend_limit ORDER BY subject, subject_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Limit
	for rows.Next() {
		var l Limit
		if err := rows.Scan(&l.Subject, &l.SubjectID, &l.MonthlyCents); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
