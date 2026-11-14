package store

import (
	"context"
	"time"
)

// Person is someone Workbench has seen sign in.
type Person struct {
	ID          string
	Email       string
	DisplayName string
	Groups      []string
	LastSeenAt  time.Time
}

// UpsertPerson records a sign-in, refreshing email, name and groups.
func (s *Store) UpsertPerson(ctx context.Context, p Person) error {
	if p.Groups == nil {
		p.Groups = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO person (id, email, display_name, groups) VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET email = EXCLUDED.email, display_name = EXCLUDED.display_name,
			groups = EXCLUDED.groups, last_seen_at = now()`,
		p.ID, p.Email, p.DisplayName, p.Groups)
	return mapErr(err)
}

// GetPerson returns a person by principal ID.
func (s *Store) GetPerson(ctx context.Context, id string) (Person, error) {
	var p Person
	err := s.pool.QueryRow(ctx, `SELECT id, email, display_name, groups, last_seen_at FROM person WHERE id = $1`, id).
		Scan(&p.ID, &p.Email, &p.DisplayName, &p.Groups, &p.LastSeenAt)
	return p, mapErr(err)
}

// FindPerson resolves an email address or principal ID to a person.
func (s *Store) FindPerson(ctx context.Context, emailOrID string) (Person, error) {
	var p Person
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, display_name, groups, last_seen_at FROM person
		WHERE id = $1 OR lower(email) = lower($1) ORDER BY (id = $1) DESC, last_seen_at DESC LIMIT 1`, emailOrID).
		Scan(&p.ID, &p.Email, &p.DisplayName, &p.Groups, &p.LastSeenAt)
	return p, mapErr(err)
}

// People returns the people with the given IDs, keyed by ID. Unknown IDs are omitted.
func (s *Store) People(ctx context.Context, ids []string) (map[string]Person, error) {
	out := map[string]Person{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id, email, display_name, groups, last_seen_at FROM person WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Email, &p.DisplayName, &p.Groups, &p.LastSeenAt); err != nil {
			return nil, err
		}
		out[p.ID] = p
	}
	return out, rows.Err()
}

// PeopleInGroup returns everyone last seen in a group.
func (s *Store) PeopleInGroup(ctx context.Context, group string) ([]Person, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, email, display_name, groups, last_seen_at FROM person WHERE $1 = ANY(groups) ORDER BY id`, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Email, &p.DisplayName, &p.Groups, &p.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// IdentityLink ties an external identity to a person.
type IdentityLink struct {
	Issuer      string
	Subject     string
	PersonID    string
	DisplayHint string
	LinkedAt    time.Time
}

// LinkIdentity records a link. Linking an identity already linked to someone
// else is a conflict; relinking to the same person is a no-op.
func (s *Store) LinkIdentity(ctx context.Context, l IdentityLink) (IdentityLink, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO identity_link (issuer, subject, person_id, display_hint) VALUES ($1, $2, $3, $4)
		ON CONFLICT (issuer, subject) DO UPDATE SET display_hint = EXCLUDED.display_hint
			WHERE identity_link.person_id = EXCLUDED.person_id
		RETURNING issuer, subject, person_id, display_hint, linked_at`,
		l.Issuer, l.Subject, l.PersonID, l.DisplayHint).
		Scan(&l.Issuer, &l.Subject, &l.PersonID, &l.DisplayHint, &l.LinkedAt)
	if err := mapErr(err); err == ErrNotFound {
		return IdentityLink{}, ErrConflict
	} else if err != nil {
		return IdentityLink{}, err
	}
	return l, nil
}

// ResolveLink returns the person an external identity is linked to.
func (s *Store) ResolveLink(ctx context.Context, issuer, subject string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT person_id FROM identity_link WHERE issuer = $1 AND subject = $2`, issuer, subject).Scan(&id)
	return id, mapErr(err)
}

// ListLinks returns a person's linked identities.
func (s *Store) ListLinks(ctx context.Context, personID string) ([]IdentityLink, error) {
	rows, err := s.pool.Query(ctx, `SELECT issuer, subject, person_id, display_hint, linked_at FROM identity_link WHERE person_id = $1 ORDER BY linked_at`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdentityLink
	for rows.Next() {
		var l IdentityLink
		if err := rows.Scan(&l.Issuer, &l.Subject, &l.PersonID, &l.DisplayHint, &l.LinkedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Unlink removes one of a person's links.
func (s *Store) Unlink(ctx context.Context, personID, issuer, subject string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM identity_link WHERE person_id = $1 AND issuer = $2 AND subject = $3`, personID, issuer, subject)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// WatchedSession records which agent and requester a kagent session belongs to.
type WatchedSession struct {
	SessionID   string
	Agent       string
	RequesterID string
}

// WatchSession records a session the first time it is seen.
func (s *Store) WatchSession(ctx context.Context, w WatchedSession) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO watched_session (session_id, agent, requester_id) VALUES ($1, $2, $3) ON CONFLICT (session_id) DO NOTHING`,
		w.SessionID, w.Agent, w.RequesterID)
	return mapErr(err)
}

// GetWatchedSession returns a recorded session.
func (s *Store) GetWatchedSession(ctx context.Context, id string) (WatchedSession, error) {
	var w WatchedSession
	err := s.pool.QueryRow(ctx, `SELECT session_id, agent, requester_id FROM watched_session WHERE session_id = $1`, id).
		Scan(&w.SessionID, &w.Agent, &w.RequesterID)
	return w, mapErr(err)
}
