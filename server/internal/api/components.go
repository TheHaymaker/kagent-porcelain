package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	wb "github.com/thehaymaker/kagent-porcelain/server/gen/workbench/v1"
	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/authz"
	"github.com/thehaymaker/kagent-porcelain/server/internal/components"
	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

// ListSurfaces implements ComponentService.
func (s *Server) ListSurfaces(ctx context.Context, req *wb.ListSurfacesRequest) (*wb.ListSurfacesResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	ws, err := s.Store.GetWatchedSession(ctx, req.GetSessionId())
	if err != nil || (ws.RequesterID != c.ID && !s.has(c, authz.Auditor)) {
		// A session Workbench has never seen has no surfaces either.
		return &wb.ListSurfacesResponse{}, nil
	}
	list, err := s.Store.ListSurfaces(ctx, req.GetSessionId())
	if err != nil {
		return nil, toStatus(err)
	}
	out := &wb.ListSurfacesResponse{}
	for _, sf := range list {
		p := &wb.Surface{SurfaceId: sf.SurfaceID, SessionId: sf.SessionID, TaskId: sf.TaskID, CatalogId: sf.CatalogID, CreatedAt: tsp(sf.CreatedAt), UpdatedAt: tsp(sf.UpdatedAt)}
		for _, m := range sf.Messages {
			p.Messages = append(p.Messages, toStruct(m))
		}
		out.Surfaces = append(out.Surfaces, p)
	}
	return out, nil
}

// GetCatalog implements ComponentService.
func (s *Server) GetCatalog(ctx context.Context, _ *wb.GetCatalogRequest) (*wb.GetCatalogResponse, error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(components.Catalog, &doc); err != nil {
		return nil, status.Error(codes.Internal, "catalogue unreadable")
	}
	st, err := structpb.NewStruct(doc)
	if err != nil {
		return nil, status.Error(codes.Internal, "catalogue unreadable")
	}
	return &wb.GetCatalogResponse{CatalogId: s.Components.CatalogID, Catalog: st}, nil
}

// SendAction implements ComponentService: a person pressed a Button on a
// surface. The action goes back to the conversation as a user message with
// an A2UI action DataPart, sent as the conversation's owner.
func (s *Server) SendAction(ctx context.Context, req *wb.SendActionRequest) (*wb.SendActionResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetName() == "" || len(req.GetName()) > 128 {
		return nil, status.Error(codes.InvalidArgument, "action name is required")
	}
	candidates, err := s.Store.FindSurface(ctx, req.GetSurfaceId())
	if err != nil {
		return nil, toStatus(err)
	}
	var sf *store.Surface
	for i := range candidates {
		if ws, err := s.Store.GetWatchedSession(ctx, candidates[i].SessionID); err == nil && ws.RequesterID == c.ID {
			sf = &candidates[i]
			break
		}
	}
	if sf == nil {
		return nil, status.Error(codes.NotFound, "not found")
	}
	ws, _ := s.Store.GetWatchedSession(ctx, sf.SessionID)
	action := map[string]any{"version": components.Version, "action": map[string]any{
		"name": req.GetName(), "surfaceId": sf.SurfaceID, "sourceComponentId": req.GetSourceComponentId(), "context": req.GetContext().AsMap(),
	}}
	msg := hitl.Message{MessageID: uuid.NewString(), ContextID: sf.SessionID, Role: "ROLE_USER", Parts: []hitl.Part{
		{Text: fmt.Sprintf("(pressed %q)", req.GetName())},
		{Data: []any{action}, Metadata: map[string]any{"mimeType": components.MimeType}},
	}}
	if err := s.Kagent.Send(ctx, c.ID, c.ID, ws.Agent, msg); err != nil {
		return nil, status.Error(codes.Unavailable, "could not reach the agent: "+err.Error())
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.ComponentAction, ActorID: c.ID, SessionID: sf.SessionID, Surface: c.Surface,
		Summary: fmt.Sprintf("Pressed %q", req.GetName())})
	return &wb.SendActionResponse{}, nil
}
