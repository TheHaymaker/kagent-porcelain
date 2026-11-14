package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// App statuses.
const (
	AppBuilding = "building"
	AppPreview  = "preview"
	AppLive     = "live"
	AppFailed   = "failed"
	AppArchived = "archived"
)

// Environment names.
const (
	EnvPreview    = "preview"
	EnvProduction = "production"
)

// App is a registry entry.
type App struct {
	ID            string
	Name          string
	Description   string
	OwnerID       string
	Status        string
	Scaffold      string
	RepositoryURL string
	SessionID     string
	Agent         string
	Environments  []Environment
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Environment is where an app runs.
type Environment struct {
	Name      string
	URL       string
	Revision  string
	Image     string
	UpdatedAt time.Time
}

// Env returns the named environment, if the app has it.
func (a App) Env(name string) (Environment, bool) {
	for _, e := range a.Environments {
		if e.Name == name {
			return e, true
		}
	}
	return Environment{}, false
}

// Deployment is one revision reaching one environment.
type Deployment struct {
	ID          string
	AppID       string
	Environment string
	Revision    string
	Image       string
	URL         string
	DeployedBy  string
	ApprovalID  string
	CreatedAt   time.Time
}

const appColumns = `id, name, description, owner_id, status, scaffold, repository_url, session_id, agent, created_at, updated_at`

func scanApp(row pgx.Row) (App, error) {
	var a App
	err := row.Scan(&a.ID, &a.Name, &a.Description, &a.OwnerID, &a.Status, &a.Scaffold, &a.RepositoryURL, &a.SessionID, &a.Agent, &a.CreatedAt, &a.UpdatedAt)
	return a, mapErr(err)
}

// CreateApp registers an app. Names are unique among unarchived apps.
func (s *Store) CreateApp(ctx context.Context, a App) (App, error) {
	if a.ID == "" {
		a.ID = NewID("app")
	}
	if a.Status == "" {
		a.Status = AppBuilding
	}
	return scanApp(s.pool.QueryRow(ctx, `
		INSERT INTO app (id, name, description, owner_id, status, scaffold, repository_url, session_id, agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+appColumns,
		a.ID, a.Name, a.Description, a.OwnerID, a.Status, a.Scaffold, a.RepositoryURL, a.SessionID, a.Agent))
}

// GetApp returns an app with its environments.
func (s *Store) GetApp(ctx context.Context, id string) (App, error) {
	a, err := scanApp(s.pool.QueryRow(ctx, `SELECT `+appColumns+` FROM app WHERE id = $1`, id))
	if err != nil {
		return a, err
	}
	envs, err := s.environments(ctx, []string{a.ID})
	a.Environments = envs[a.ID]
	return a, err
}

// AppFilter narrows ListApps.
type AppFilter struct {
	OwnerID string
	Status  string
	Query   string
	// IncludeArchived lists archived apps when Status is empty.
	IncludeArchived bool
}

// ListApps lists apps, most recently updated first.
func (s *Store) ListApps(ctx context.Context, f AppFilter, page Page) ([]App, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+appColumns+` FROM app
		WHERE ($1 = '' OR owner_id = $1)
		  AND (($2 = '' AND ($3 OR status <> 'archived')) OR status = $2)
		  AND ($4 = '' OR name ILIKE '%' || $4 || '%' OR description ILIKE '%' || $4 || '%')
		ORDER BY updated_at DESC LIMIT $5 OFFSET $6`,
		f.OwnerID, f.Status, f.IncludeArchived, f.Query, page.limit(), page.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []App
	var ids []string
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, a)
		ids = append(ids, a.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	envs, err := s.environments(ctx, ids)
	for i := range apps {
		apps[i].Environments = envs[apps[i].ID]
	}
	return apps, err
}

func (s *Store) environments(ctx context.Context, appIDs []string) (map[string][]Environment, error) {
	out := map[string][]Environment{}
	if len(appIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT app_id, name, url, revision, image, updated_at FROM app_environment WHERE app_id = ANY($1) ORDER BY name DESC`, appIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var e Environment
		if err := rows.Scan(&id, &e.Name, &e.URL, &e.Revision, &e.Image, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out[id] = append(out[id], e)
	}
	return out, rows.Err()
}

// UpdateApp changes an app's name and description.
func (s *Store) UpdateApp(ctx context.Context, id, name, description string) (App, error) {
	if _, err := s.pool.Exec(ctx, `UPDATE app SET name = COALESCE(NULLIF($2, ''), name), description = $3, updated_at = now() WHERE id = $1`, id, name, description); err != nil {
		return App{}, mapErr(err)
	}
	return s.GetApp(ctx, id)
}

// SetAppStatus changes an app's status.
func (s *Store) SetAppStatus(ctx context.Context, id, status string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE app SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordDeployment records a revision reaching an environment and makes it
// the environment's current revision. A production deployment must name the
// approval that allowed it; the table refuses one that does not.
func (s *Store) RecordDeployment(ctx context.Context, d Deployment) (Deployment, error) {
	if d.ID == "" {
		d.ID = NewID("dep")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO deployment (id, app_id, environment, revision, image, url, deployed_by_id, approval_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING created_at`,
			d.ID, d.AppID, d.Environment, d.Revision, d.Image, d.URL, d.DeployedBy, d.ApprovalID).Scan(&d.CreatedAt); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO app_environment (app_id, name, url, revision, image) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (app_id, name) DO UPDATE SET url = EXCLUDED.url, revision = EXCLUDED.revision, image = EXCLUDED.image, updated_at = now()`,
			d.AppID, d.Environment, d.URL, d.Revision, d.Image); err != nil {
			return mapErr(err)
		}
		status := AppPreview
		if d.Environment == EnvProduction {
			status = AppLive
		}
		// Never step a live app back to preview because a new preview shipped.
		_, err := tx.Exec(ctx, `UPDATE app SET status = CASE WHEN status = 'live' THEN 'live' ELSE $2 END, updated_at = now() WHERE id = $1`, d.AppID, status)
		return err
	})
	if err != nil {
		return Deployment{}, fmt.Errorf("record deployment: %w", err)
	}
	return d, nil
}

// ListDeployments lists an app's deployments, newest first.
func (s *Store) ListDeployments(ctx context.Context, appID string) ([]Deployment, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, app_id, environment, revision, image, url, deployed_by_id, approval_id, created_at
		FROM deployment WHERE app_id = $1 ORDER BY created_at DESC LIMIT 200`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.AppID, &d.Environment, &d.Revision, &d.Image, &d.URL, &d.DeployedBy, &d.ApprovalID, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// FindPreviewDeployment returns the preview deployment of a revision.
func (s *Store) FindPreviewDeployment(ctx context.Context, appID, revision string) (Deployment, error) {
	var d Deployment
	err := s.pool.QueryRow(ctx, `SELECT id, app_id, environment, revision, image, url, deployed_by_id, approval_id, created_at
		FROM deployment WHERE app_id = $1 AND environment = 'preview' AND revision = $2 ORDER BY created_at DESC LIMIT 1`, appID, revision).
		Scan(&d.ID, &d.AppID, &d.Environment, &d.Revision, &d.Image, &d.URL, &d.DeployedBy, &d.ApprovalID, &d.CreatedAt)
	return d, mapErr(err)
}
