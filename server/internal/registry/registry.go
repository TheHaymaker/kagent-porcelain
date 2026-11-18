// Package registry is the catalogue of every app Workbench has built, its
// environments, and the one way into production: an approved promotion.
package registry

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/notify"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

// Deployer puts an image into production. It runs inside the Workbench
// server with production credentials that agents and sandboxes never hold.
type Deployer interface {
	DeployProduction(ctx context.Context, app store.App, image, revision, approvalID string) (url string, err error)
}

// Notifier is the part of notify.Notifier the registry uses.
type Notifier interface {
	Notify(ctx context.Context, personIDs []string, n notify.Notice)
}

// Registry implements the app catalogue.
type Registry struct {
	Store    *store.Store
	Deployer Deployer
	Audit    audit.Recorder
	Notify   Notifier
}

// ErrInvalid marks bad input.
var ErrInvalid = errors.New("invalid")

// ErrNotAllowed marks a promotion that its approval does not permit.
var ErrNotAllowed = errors.New("not allowed")

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

// Registration is what a builder supplies about a new app.
type Registration struct {
	Name          string
	Description   string
	Scaffold      string
	RepositoryURL string
	OwnerID       string
	SessionID     string
	Agent         string
	ActorID       string
}

// Register adds an app. Its owner is the person the conversation belongs to.
func (r *Registry) Register(ctx context.Context, reg Registration) (store.App, error) {
	name := strings.ToLower(strings.TrimSpace(reg.Name))
	if !nameRE.MatchString(name) {
		return store.App{}, fmt.Errorf("%w: app name %q must be 3-40 lowercase letters, digits and dashes, starting with a letter", ErrInvalid, reg.Name)
	}
	if reg.OwnerID == "" {
		return store.App{}, fmt.Errorf("%w: an app needs an owner", ErrInvalid)
	}
	if reg.RepositoryURL != "" && !strings.HasPrefix(reg.RepositoryURL, "https://") {
		return store.App{}, fmt.Errorf("%w: repository URL must be https", ErrInvalid)
	}
	app, err := r.Store.CreateApp(ctx, store.App{Name: name, Description: reg.Description, OwnerID: reg.OwnerID,
		Scaffold: reg.Scaffold, RepositoryURL: reg.RepositoryURL, SessionID: reg.SessionID, Agent: reg.Agent})
	if err != nil {
		return app, err
	}
	r.Audit.Record(ctx, store.AuditEvent{Type: audit.AppRegistered, ActorID: reg.ActorID, OnBehalfOfID: reg.OwnerID,
		SessionID: reg.SessionID, AppID: app.ID, Summary: fmt.Sprintf("Registered app %s", app.Name),
		Detail: map[string]any{"scaffold": reg.Scaffold, "repository": reg.RepositoryURL}})
	return app, nil
}

// RecordPreview records a preview deployment the builder made.
func (r *Registry) RecordPreview(ctx context.Context, appID, url, revision, image, actorID, sessionID string) (store.Deployment, error) {
	if revision == "" {
		return store.Deployment{}, fmt.Errorf("%w: revision is required", ErrInvalid)
	}
	if url != "" && !strings.HasPrefix(url, "https://") {
		return store.Deployment{}, fmt.Errorf("%w: preview URL must be https", ErrInvalid)
	}
	d, err := r.Store.RecordDeployment(ctx, store.Deployment{AppID: appID, Environment: store.EnvPreview, Revision: revision,
		Image: image, URL: url, DeployedBy: actorID})
	if err != nil {
		return d, err
	}
	r.Audit.Record(ctx, store.AuditEvent{Type: audit.AppDeployed, ActorID: actorID, SessionID: sessionID, AppID: appID,
		Summary: fmt.Sprintf("Deployed revision %s to preview", revision), Detail: map[string]any{"url": url, "image": image}})
	return d, nil
}

// PromotionTarget reads which app and revision an approval promotes.
func PromotionTarget(a store.Approval) (appID, revision string) {
	appID = a.AppID
	for _, c := range a.Calls {
		if c.Kind != policy.ProductionDeploy {
			continue
		}
		if v, ok := c.Args["appId"].(string); ok && appID == "" {
			appID = v
		}
		if v, ok := c.Args["revision"].(string); ok && v != "" {
			revision = v
		}
	}
	return appID, revision
}

// Promote puts the approved revision into production. It is idempotent: an
// approval allows exactly one production deployment, and promoting again
// returns that deployment.
func (r *Registry) Promote(ctx context.Context, a store.Approval) (store.Deployment, error) {
	if a.State != store.ApprovalApproved || a.Kind != policy.ProductionDeploy {
		return store.Deployment{}, fmt.Errorf("%w: approval %s is %s %s, not an approved production deploy", ErrNotAllowed, a.ID, a.State, a.Kind)
	}
	if a.DecidedByID == "" || (a.DecidedByID == a.RequesterID && !a.RequesterMayDecide) {
		return store.Deployment{}, fmt.Errorf("%w: approval %s was not decided by an eligible approver", ErrNotAllowed, a.ID)
	}
	appID, revision := PromotionTarget(a)
	if appID == "" {
		return store.Deployment{}, fmt.Errorf("%w: approval %s names no app", ErrInvalid, a.ID)
	}
	if existing, ok, err := r.deploymentFor(ctx, appID, a.ID); err != nil || ok {
		return existing, err
	}
	app, err := r.Store.GetApp(ctx, appID)
	if err != nil {
		return store.Deployment{}, err
	}
	if app.Status == store.AppArchived {
		return store.Deployment{}, fmt.Errorf("%w: app %s is archived", ErrNotAllowed, app.Name)
	}
	if revision == "" {
		env, ok := app.Env(store.EnvPreview)
		if !ok {
			return store.Deployment{}, fmt.Errorf("%w: app %s has no preview to promote", ErrInvalid, app.Name)
		}
		revision = env.Revision
	}
	preview, err := r.Store.FindPreviewDeployment(ctx, app.ID, revision)
	if err != nil {
		return store.Deployment{}, fmt.Errorf("%w: revision %s of %s was never deployed to preview", ErrInvalid, revision, app.Name)
	}
	url, err := r.Deployer.DeployProduction(ctx, app, preview.Image, revision, a.ID)
	if err != nil {
		r.Audit.Record(ctx, store.AuditEvent{Type: audit.AppPromotionFailed, ActorID: a.DecidedByID, OnBehalfOfID: a.RequesterID,
			SessionID: a.SessionID, AppID: app.ID, ApprovalID: a.ID, Summary: fmt.Sprintf("Promotion of %s failed", app.Name),
			Detail: map[string]any{"error": err.Error(), "revision": revision}})
		return store.Deployment{}, fmt.Errorf("deploy %s to production: %w", app.Name, err)
	}
	d, err := r.Store.RecordDeployment(ctx, store.Deployment{AppID: app.ID, Environment: store.EnvProduction, Revision: revision,
		Image: preview.Image, URL: url, DeployedBy: a.DecidedByID, ApprovalID: a.ID})
	if errors.Is(err, store.ErrConflict) {
		if existing, ok, err := r.deploymentFor(ctx, appID, a.ID); err == nil && ok {
			return existing, nil
		}
	}
	if err != nil {
		return d, err
	}
	r.Audit.Record(ctx, store.AuditEvent{Type: audit.AppPromoted, ActorID: a.DecidedByID, OnBehalfOfID: a.RequesterID,
		SessionID: a.SessionID, AppID: app.ID, ApprovalID: a.ID,
		Summary: fmt.Sprintf("Promoted %s revision %s to production", app.Name, revision), Detail: map[string]any{"url": url, "image": preview.Image}})
	if r.Notify != nil {
		r.Notify.Notify(ctx, []string{app.OwnerID}, notify.Notice{Kind: notify.AppPromoted,
			Title: fmt.Sprintf("%s is live", app.Name), Body: "Your app is now in production at " + url,
			Link: "/apps/" + app.ID, AppID: app.ID, DedupeKey: "promoted:" + a.ID})
	}
	return d, nil
}

func (r *Registry) deploymentFor(ctx context.Context, appID, approvalID string) (store.Deployment, bool, error) {
	deps, err := r.Store.ListDeployments(ctx, appID)
	if err != nil {
		return store.Deployment{}, false, err
	}
	for _, d := range deps {
		if d.ApprovalID == approvalID && d.Environment == store.EnvProduction {
			return d, true, nil
		}
	}
	return store.Deployment{}, false, nil
}
