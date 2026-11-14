package api

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	wb "github.com/thehaymaker/kagent-porcelain/server/gen/workbench/v1"
	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/authz"
	"github.com/thehaymaker/kagent-porcelain/server/internal/identity"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/usage"
)

var appStatusNames = map[wb.AppStatus]string{
	wb.AppStatus_APP_STATUS_BUILDING: store.AppBuilding, wb.AppStatus_APP_STATUS_PREVIEW: store.AppPreview,
	wb.AppStatus_APP_STATUS_LIVE: store.AppLive, wb.AppStatus_APP_STATUS_FAILED: store.AppFailed, wb.AppStatus_APP_STATUS_ARCHIVED: store.AppArchived,
}

func (s *Server) appsOut(ctx context.Context, apps []store.App) []*wb.App {
	ids := make([]string, 0, len(apps))
	owners := make([]string, 0, len(apps))
	for _, a := range apps {
		ids = append(ids, a.ID)
		owners = append(owners, a.OwnerID)
	}
	cost, _ := s.Store.AppSpentMicros(ctx, ids, usage.MonthStart(timeNow()))
	p := s.people(ctx, owners...)
	out := make([]*wb.App, 0, len(apps))
	for _, a := range apps {
		pending := ""
		if latest, err := s.Store.LatestApprovalForApp(ctx, a.ID); err == nil && latest.State == store.ApprovalPending {
			pending = latest.ID
		}
		out = append(out, p.app(a, cost[a.ID], pending))
	}
	return out
}

// ListApps implements AppService. The catalogue is visible to everyone who
// may use Workbench: knowing an app exists, who owns it and where it runs is
// the point of the catalogue.
func (s *Server) ListApps(ctx context.Context, req *wb.ListAppsRequest) (*wb.ListAppsResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	f := store.AppFilter{Status: appStatusNames[req.GetStatus()], Query: req.GetQuery()}
	if req.GetMine() {
		f.OwnerID = c.ID
	}
	pg := page(req.GetPage())
	apps, err := s.Store.ListApps(ctx, f, pg)
	if err != nil {
		return nil, toStatus(err)
	}
	return &wb.ListAppsResponse{Apps: s.appsOut(ctx, apps), Page: nextPage(pg, len(apps))}, nil
}

// GetApp implements AppService.
func (s *Server) GetApp(ctx context.Context, req *wb.GetAppRequest) (*wb.GetAppResponse, error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}
	a, err := s.Store.GetApp(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &wb.GetAppResponse{App: s.appsOut(ctx, []store.App{a})[0]}, nil
}

func (s *Server) ownedApp(ctx context.Context, c identity.Caller, id string) (store.App, error) {
	a, err := s.Store.GetApp(ctx, id)
	if err != nil {
		return a, toStatus(err)
	}
	if a.OwnerID != c.ID && !s.has(c, authz.PlatformEngineer) {
		return a, status.Error(codes.PermissionDenied, "only the app's owner can change it")
	}
	return a, nil
}

// UpdateApp implements AppService.
func (s *Server) UpdateApp(ctx context.Context, req *wb.UpdateAppRequest) (*wb.UpdateAppResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.ownedApp(ctx, c, req.GetId()); err != nil {
		return nil, err
	}
	if req.GetName() != "" {
		return nil, status.Error(codes.InvalidArgument, "an app's name is its address and cannot change; update the description instead")
	}
	a, err := s.Store.UpdateApp(ctx, req.GetId(), "", req.GetDescription())
	if err != nil {
		return nil, toStatus(err)
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.AppUpdated, ActorID: c.ID, AppID: a.ID, Surface: c.Surface, Summary: "Updated the description of " + a.Name})
	return &wb.UpdateAppResponse{App: s.appsOut(ctx, []store.App{a})[0]}, nil
}

// ArchiveApp implements AppService. Archiving removes the app from the
// catalogue; tearing down its services is a platform task.
func (s *Server) ArchiveApp(ctx context.Context, req *wb.ArchiveAppRequest) (*wb.ArchiveAppResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.ownedApp(ctx, c, req.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.Store.SetAppStatus(ctx, a.ID, store.AppArchived); err != nil {
		return nil, toStatus(err)
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.AppArchived, ActorID: c.ID, AppID: a.ID, Surface: c.Surface, Summary: "Archived " + a.Name})
	a, _ = s.Store.GetApp(ctx, a.ID)
	return &wb.ArchiveAppResponse{App: s.appsOut(ctx, []store.App{a})[0]}, nil
}

// ListDeployments implements AppService.
func (s *Server) ListDeployments(ctx context.Context, req *wb.ListDeploymentsRequest) (*wb.ListDeploymentsResponse, error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}
	deps, err := s.Store.ListDeployments(ctx, req.GetAppId())
	if err != nil {
		return nil, toStatus(err)
	}
	ids := make([]string, 0, len(deps))
	for _, d := range deps {
		ids = append(ids, d.DeployedBy)
	}
	p := s.people(ctx, ids...)
	out := &wb.ListDeploymentsResponse{}
	for _, d := range deps {
		out.Deployments = append(out.Deployments, &wb.Deployment{Id: d.ID, AppId: d.AppID, Environment: envToProto[d.Environment],
			Revision: d.Revision, Image: d.Image, Url: d.URL, DeployedBy: p.person(d.DeployedBy), ApprovalId: d.ApprovalID, CreatedAt: tsp(d.CreatedAt)})
	}
	return out, nil
}

// RequestPromotion implements AppService.
func (s *Server) RequestPromotion(ctx context.Context, req *wb.RequestPromotionRequest) (*wb.RequestPromotionResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.ownedApp(ctx, c, req.GetAppId())
	if err != nil {
		return nil, err
	}
	ap, err := s.Approvals.RequestPromotion(ctx, c, a, req.GetRevision(), req.GetNote(), a.SessionID)
	if err != nil {
		return nil, toStatus(err)
	}
	return &wb.RequestPromotionResponse{ApprovalId: ap.ID}, nil
}
