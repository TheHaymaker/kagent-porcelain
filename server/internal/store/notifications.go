package store

import (
	"context"
	"time"
)

// Notification is an in-app notice, and the record other channels send from.
type Notification struct {
	ID         string
	PersonID   string
	Kind       string
	Title      string
	Body       string
	Link       string
	SessionID  string
	ApprovalID string
	AppID      string
	// DedupeKey, when set, makes a second notification with the same key a no-op.
	DedupeKey string
	Read      bool
	CreatedAt time.Time
}

// AddNotification stores a notification. It returns created=false when the
// dedupe key was already used for this person.
func (s *Store) AddNotification(ctx context.Context, n Notification) (Notification, bool, error) {
	if n.ID == "" {
		n.ID = NewID("ntf")
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO notification (id, person_id, kind, title, body, link, session_id, approval_id, app_id, dedupe_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (person_id, dedupe_key) WHERE dedupe_key <> '' DO NOTHING RETURNING created_at`,
		n.ID, n.PersonID, n.Kind, n.Title, n.Body, n.Link, n.SessionID, n.ApprovalID, n.AppID, n.DedupeKey).Scan(&n.CreatedAt)
	if err := mapErr(err); err == ErrNotFound {
		return n, false, nil
	} else if err != nil {
		return n, false, err
	}
	return n, true, nil
}

// ListNotifications lists a person's notifications, newest first, with their unread count.
func (s *Store) ListNotifications(ctx context.Context, personID string, unreadOnly bool, page Page) ([]Notification, int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, person_id, kind, title, body, link, session_id, approval_id, app_id, dedupe_key, read, created_at
		FROM notification WHERE person_id = $1 AND (NOT $2 OR NOT read) ORDER BY created_at DESC LIMIT $3 OFFSET $4`,
		personID, unreadOnly, page.limit(), page.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.PersonID, &n.Kind, &n.Title, &n.Body, &n.Link, &n.SessionID, &n.ApprovalID, &n.AppID, &n.DedupeKey, &n.Read, &n.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unread int
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM notification WHERE person_id = $1 AND NOT read`, personID).Scan(&unread)
	return out, unread, err
}

// MarkRead marks some (or, with all, every) notification read.
func (s *Store) MarkRead(ctx context.Context, personID string, ids []string, all bool) error {
	if ids == nil {
		ids = []string{}
	}
	_, err := s.pool.Exec(ctx, `UPDATE notification SET read = true WHERE person_id = $1 AND ($3 OR id = ANY($2))`, personID, ids, all)
	return err
}

// Preferences returns a person's channel choices per kind.
func (s *Store) Preferences(ctx context.Context, personID string) (map[string][]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT kind, channels FROM notification_preference WHERE person_id = $1`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var k string
		var ch []string
		if err := rows.Scan(&k, &ch); err != nil {
			return nil, err
		}
		out[k] = ch
	}
	return out, rows.Err()
}

// SetPreference stores a person's channels for one kind.
func (s *Store) SetPreference(ctx context.Context, personID, kind string, channels []string) error {
	if channels == nil {
		channels = []string{}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO notification_preference (person_id, kind, channels) VALUES ($1, $2, $3)
		ON CONFLICT (person_id, kind) DO UPDATE SET channels = EXCLUDED.channels`, personID, kind, channels)
	return err
}

// Device is a registered mobile push target.
type Device struct {
	PushToken  string
	PersonID   string
	Platform   string
	DeviceName string
}

// RegisterDevice records (or moves) a push token.
func (s *Store) RegisterDevice(ctx context.Context, d Device) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO device (push_token, person_id, platform, device_name) VALUES ($1, $2, $3, $4)
		ON CONFLICT (push_token) DO UPDATE SET person_id = EXCLUDED.person_id, platform = EXCLUDED.platform, device_name = EXCLUDED.device_name`,
		d.PushToken, d.PersonID, d.Platform, d.DeviceName)
	return mapErr(err)
}

// Devices returns a person's push targets.
func (s *Store) Devices(ctx context.Context, personID string) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT push_token, person_id, platform, device_name FROM device WHERE person_id = $1`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.PushToken, &d.PersonID, &d.Platform, &d.DeviceName); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RemoveDevice forgets a push token the push service reported as dead.
func (s *Store) RemoveDevice(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM device WHERE push_token = $1`, token)
	return err
}
