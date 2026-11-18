package api

import (
	"context"

	wb "github.com/thehaymaker/kagent-porcelain/server/gen/workbench/v1"
	"github.com/thehaymaker/kagent-porcelain/server/internal/authz"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

// ListAuditEvents implements AuditService. Auditors and platform engineers
// see everything; everyone else sees events on their own conversations,
// apps and approvals, and what they did themselves.
func (s *Server) ListAuditEvents(ctx context.Context, req *wb.ListAuditEventsRequest) (*wb.ListAuditEventsResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	f := store.AuditFilter{SessionID: req.GetSessionId(), AppID: req.GetAppId(), ActorID: req.GetActorId(), TypePrefix: req.GetTypePrefix()}
	if req.GetSince() != nil {
		f.Since = req.GetSince().AsTime()
	}
	if req.GetUntil() != nil {
		f.Until = req.GetUntil().AsTime()
	}
	if !s.has(c, authz.Auditor) {
		f.VisibleTo = c.ID
	}
	pg := page(req.GetPage())
	events, err := s.Store.ListAudit(ctx, f, pg)
	if err != nil {
		return nil, toStatus(err)
	}
	var ids []string
	for _, e := range events {
		ids = append(ids, e.ActorID, e.OnBehalfOfID)
	}
	p := s.people(ctx, ids...)
	out := &wb.ListAuditEventsResponse{Page: nextPage(pg, len(events))}
	for _, e := range events {
		out.Events = append(out.Events, p.auditEvent(e))
	}
	return out, nil
}
