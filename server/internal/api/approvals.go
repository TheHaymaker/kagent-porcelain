package api

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	wb "github.com/thehaymaker/kagent-porcelain/server/gen/workbench/v1"
	"github.com/thehaymaker/kagent-porcelain/server/internal/approval"
	"github.com/thehaymaker/kagent-porcelain/server/internal/authz"
	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
	"github.com/thehaymaker/kagent-porcelain/server/internal/identity"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

func (s *Server) views(ctx context.Context, c identity.Caller, list []store.Approval) []*wb.Approval {
	p := s.people(ctx, approvalIDs(list)...)
	out := make([]*wb.Approval, 0, len(list))
	for _, a := range list {
		out = append(out, p.approval(approval.ViewFor(c, a)))
	}
	return out
}

// ListInbox implements ApprovalService.
func (s *Server) ListInbox(ctx context.Context, req *wb.ListInboxRequest) (*wb.ListInboxResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	pg := page(req.GetPage())
	var list []store.Approval
	switch req.GetView() {
	case wb.InboxView_INBOX_VIEW_MY_REQUESTS:
		list, err = s.Store.ListByRequester(ctx, c.ID, pg)
	case wb.InboxView_INBOX_VIEW_DECIDED_BY_ME:
		list, err = s.Store.ListDecidedBy(ctx, c.ID, pg)
	default:
		list, err = s.Store.ListPendingForDecider(ctx, c.ID, c.Groups, pg)
		// The query narrows candidates; the routing rule has the last word.
		kept := list[:0]
		for _, a := range list {
			if approval.ViewFor(c, a).CallerMayDecide {
				kept = append(kept, a)
			}
		}
		list = kept
	}
	if err != nil {
		return nil, toStatus(err)
	}
	return &wb.ListInboxResponse{Approvals: s.views(ctx, c, list), Page: nextPage(pg, len(list))}, nil
}

func (s *Server) viewable(ctx context.Context, c identity.Caller, id string) (store.Approval, error) {
	a, err := s.Store.GetApproval(ctx, id)
	if err != nil {
		return a, toStatus(err)
	}
	if !s.Approvals.Viewable(c, a, s.has(c, authz.Auditor)) {
		return a, status.Error(codes.NotFound, "not found")
	}
	return a, nil
}

// GetApproval implements ApprovalService.
func (s *Server) GetApproval(ctx context.Context, req *wb.GetApprovalRequest) (*wb.GetApprovalResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.viewable(ctx, c, req.GetId())
	if err != nil {
		return nil, err
	}
	return &wb.GetApprovalResponse{Approval: s.views(ctx, c, []store.Approval{a})[0]}, nil
}

// ListSessionApprovals implements ApprovalService. It first syncs the
// session's paused tasks from kagent, so a client that just saw a task pause
// always finds its approval here.
func (s *Server) ListSessionApprovals(ctx context.Context, req *wb.ListSessionApprovalsRequest) (*wb.ListSessionApprovalsResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	ws, err := s.Store.GetWatchedSession(ctx, req.GetSessionId())
	owner := err == nil && ws.RequesterID == c.ID
	if err != nil || owner {
		// Unknown sessions are learned from kagent; SyncSession refuses
		// anyone but the owner.
		if err := s.Approvals.SyncSession(ctx, c, req.GetSessionId()); err != nil {
			switch {
			case errors.Is(err, approval.ErrPermission), status.Code(err) == codes.NotFound:
				// Hide sessions the caller may not see.
				return nil, status.Error(codes.NotFound, "not found")
			case owner:
				// The stored list is still useful; a push may catch up.
				slog.WarnContext(ctx, "sync session approvals", "session", req.GetSessionId(), "error", err)
			default:
				slog.ErrorContext(ctx, "sync session approvals", "session", req.GetSessionId(), "error", err)
				return nil, status.Error(codes.Unavailable, "could not read the session from kagent")
			}
		}
	}
	list, err := s.Store.ListSessionApprovals(ctx, req.GetSessionId())
	if err != nil {
		return nil, toStatus(err)
	}
	auditor := s.has(c, authz.Auditor)
	kept := list[:0]
	for _, a := range list {
		if s.Approvals.Viewable(c, a, auditor) {
			kept = append(kept, a)
		}
	}
	return &wb.ListSessionApprovalsResponse{Approvals: s.views(ctx, c, kept)}, nil
}

var rememberNames = map[wb.RememberScope]string{
	wb.RememberScope_REMEMBER_SCOPE_UNSPECIFIED: "", wb.RememberScope_REMEMBER_SCOPE_ONCE: "once",
	wb.RememberScope_REMEMBER_SCOPE_CONVERSATION: "conversation", wb.RememberScope_REMEMBER_SCOPE_ALWAYS: "always",
}

// Decide implements ApprovalService.
func (s *Server) Decide(ctx context.Context, req *wb.DecideRequest) (*wb.DecideResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.viewable(ctx, c, req.GetApprovalId()); err != nil {
		return nil, err
	}
	d := approval.Decision{ApprovalID: req.GetApprovalId(), Approved: req.GetApproved(), Reason: req.GetReason(), Remember: rememberNames[req.GetRemember()]}
	for _, cd := range req.GetCalls() {
		d.Calls = append(d.Calls, hitl.Decision{ID: cd.GetId(), Approved: cd.GetApproved(), Reason: cd.GetReason()})
	}
	a, err := s.Approvals.Decide(ctx, c, d)
	if err != nil {
		return nil, toStatus(err)
	}
	return &wb.DecideResponse{Approval: s.views(ctx, c, []store.Approval{a})[0]}, nil
}

// Reassign implements ApprovalService.
func (s *Server) Reassign(ctx context.Context, req *wb.ReassignRequest) (*wb.ReassignResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.viewable(ctx, c, req.GetApprovalId()); err != nil {
		return nil, err
	}
	a, err := s.Approvals.Reassign(ctx, c, req.GetApprovalId(), req.GetAssignee(), req.GetNote())
	if err != nil {
		return nil, toStatus(err)
	}
	return &wb.ReassignResponse{Approval: s.views(ctx, c, []store.Approval{a})[0]}, nil
}

// ListRememberedAnswers implements ApprovalService.
func (s *Server) ListRememberedAnswers(ctx context.Context, _ *wb.ListRememberedAnswersRequest) (*wb.ListRememberedAnswersResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.Store.ListRemembered(ctx, c.ID)
	if err != nil {
		return nil, toStatus(err)
	}
	out := &wb.ListRememberedAnswersResponse{}
	for _, r := range list {
		scope := wb.RememberScope_REMEMBER_SCOPE_ALWAYS
		if r.Scope == "conversation" {
			scope = wb.RememberScope_REMEMBER_SCOPE_CONVERSATION
		}
		out.Answers = append(out.Answers, &wb.RememberedAnswer{Id: r.ID, Scope: scope, ToolName: r.ToolName, Agent: r.Agent,
			SessionId: r.SessionID, Approved: r.Approved, CreatedAt: tsp(r.CreatedAt)})
	}
	return out, nil
}

// ForgetRememberedAnswer implements ApprovalService.
func (s *Server) ForgetRememberedAnswer(ctx context.Context, req *wb.ForgetRememberedAnswerRequest) (*wb.ForgetRememberedAnswerResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Store.Forget(ctx, c.ID, req.GetId()); err != nil {
		return nil, toStatus(err)
	}
	return &wb.ForgetRememberedAnswerResponse{}, nil
}

// GetPolicy implements ApprovalService.
func (s *Server) GetPolicy(ctx context.Context, _ *wb.GetPolicyRequest) (*wb.GetPolicyResponse, error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}
	out := &wb.GetPolicyResponse{}
	for _, k := range s.Policy.Kinds {
		out.Kinds = append(out.Kinds, &wb.KindPolicy{Kind: kindToProto[k.Kind], Description: k.Description,
			ApproverGroups: k.ApproverGroups, RequesterMayDecide: k.RequesterMayDecide})
	}
	ord := s.Policy.For(policy.Ordinary)
	out.Kinds = append(out.Kinds, &wb.KindPolicy{Kind: wb.ActionKind_ACTION_KIND_ORDINARY, Description: ord.Description, RequesterMayDecide: true})
	return out, nil
}
