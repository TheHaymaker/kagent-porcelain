package store

import (
	"context"
	"encoding/json"
	"time"
)

// Surface is an A2UI surface an agent emitted in a conversation.
type Surface struct {
	SurfaceID string
	SessionID string
	TaskID    string
	CatalogID string
	Messages  []map[string]any
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AppendSurface adds messages to a surface, creating it if needed. A
// deleteSurface message removes the surface.
func (s *Store) AppendSurface(ctx context.Context, sf Surface) error {
	msgs, err := json.Marshal(sf.Messages)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO surface (surface_id, session_id, task_id, catalog_id, messages) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (session_id, surface_id) DO UPDATE SET messages = surface.messages || EXCLUDED.messages,
			task_id = EXCLUDED.task_id, updated_at = now()`,
		sf.SurfaceID, sf.SessionID, sf.TaskID, sf.CatalogID, msgs)
	return mapErr(err)
}

// DeleteSurface removes a surface.
func (s *Store) DeleteSurface(ctx context.Context, sessionID, surfaceID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM surface WHERE session_id = $1 AND surface_id = $2`, sessionID, surfaceID)
	return err
}

// GetSurface returns one surface.
func (s *Store) GetSurface(ctx context.Context, sessionID, surfaceID string) (Surface, error) {
	var sf Surface
	var msgs []byte
	err := s.pool.QueryRow(ctx, `SELECT surface_id, session_id, task_id, catalog_id, messages, created_at, updated_at
		FROM surface WHERE session_id = $1 AND surface_id = $2`, sessionID, surfaceID).
		Scan(&sf.SurfaceID, &sf.SessionID, &sf.TaskID, &sf.CatalogID, &msgs, &sf.CreatedAt, &sf.UpdatedAt)
	if err != nil {
		return sf, mapErr(err)
	}
	return sf, json.Unmarshal(msgs, &sf.Messages)
}

// FindSurface finds a surface by ID across sessions. Surface IDs are chosen
// by agents, so callers must check the session they get back.
func (s *Store) FindSurface(ctx context.Context, surfaceID string) ([]Surface, error) {
	return s.listSurfaces(ctx, `WHERE surface_id = $1`, surfaceID)
}

// ListSurfaces returns a session's surfaces in the order they were created.
func (s *Store) ListSurfaces(ctx context.Context, sessionID string) ([]Surface, error) {
	return s.listSurfaces(ctx, `WHERE session_id = $1`, sessionID)
}

func (s *Store) listSurfaces(ctx context.Context, where string, arg string) ([]Surface, error) {
	rows, err := s.pool.Query(ctx, `SELECT surface_id, session_id, task_id, catalog_id, messages, created_at, updated_at
		FROM surface `+where+` ORDER BY created_at LIMIT 500`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Surface
	for rows.Next() {
		var sf Surface
		var msgs []byte
		if err := rows.Scan(&sf.SurfaceID, &sf.SessionID, &sf.TaskID, &sf.CatalogID, &msgs, &sf.CreatedAt, &sf.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(msgs, &sf.Messages); err != nil {
			return nil, err
		}
		out = append(out, sf)
	}
	return out, rows.Err()
}
