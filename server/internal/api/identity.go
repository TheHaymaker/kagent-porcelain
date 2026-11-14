package api

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	wb "github.com/thehaymaker/kagent-porcelain/server/gen/workbench/v1"
	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/authz"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

var timeNow = time.Now

var roleToProto = map[authz.Role]wb.Role{
	authz.Requester: wb.Role_ROLE_REQUESTER, authz.Approver: wb.Role_ROLE_APPROVER,
	authz.PlatformEngineer: wb.Role_ROLE_PLATFORM_ENGINEER, authz.Auditor: wb.Role_ROLE_AUDITOR,
}

// GetMe implements IdentityService.
func (s *Server) GetMe(ctx context.Context, _ *wb.GetMeRequest) (*wb.GetMeResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	out := &wb.GetMeResponse{Person: &wb.Person{Id: c.ID, Email: c.Email, DisplayName: c.Name}, Groups: c.Groups, Surface: c.Surface}
	for _, r := range s.Authz.Roles(c.Groups) {
		out.Roles = append(out.Roles, roleToProto[r])
	}
	return out, nil
}

// LinkIdentity implements IdentityService. Only a company sign-in may link:
// a linked identity cannot link further identities to itself.
func (s *Server) LinkIdentity(ctx context.Context, req *wb.LinkIdentityRequest) (*wb.LinkIdentityResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if c.Surface == "teams" || c.Delegated {
		return nil, status.Error(codes.PermissionDenied, "link identities from a company sign-in")
	}
	iss, sub, hint, err := s.Links.VerifyLinkToken(ctx, req.GetIdToken())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "that token cannot be linked: "+err.Error())
	}
	l, err := s.Store.LinkIdentity(ctx, store.IdentityLink{Issuer: iss, Subject: sub, PersonID: c.ID, DisplayHint: hint})
	if errors.Is(err, store.ErrConflict) {
		return nil, status.Error(codes.AlreadyExists, "that identity is already linked to someone else")
	}
	if err != nil {
		return nil, toStatus(err)
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.IdentityLinked, ActorID: c.ID, Surface: c.Surface, Summary: "Linked " + hint,
		Detail: map[string]any{"issuer": iss}})
	return &wb.LinkIdentityResponse{Identity: &wb.LinkedIdentity{Issuer: l.Issuer, Subject: l.Subject, DisplayHint: l.DisplayHint, LinkedAt: tsp(l.LinkedAt)}}, nil
}

// ListLinkedIdentities implements IdentityService.
func (s *Server) ListLinkedIdentities(ctx context.Context, _ *wb.ListLinkedIdentitiesRequest) (*wb.ListLinkedIdentitiesResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	links, err := s.Store.ListLinks(ctx, c.ID)
	if err != nil {
		return nil, toStatus(err)
	}
	out := &wb.ListLinkedIdentitiesResponse{}
	for _, l := range links {
		out.Identities = append(out.Identities, &wb.LinkedIdentity{Issuer: l.Issuer, Subject: l.Subject, DisplayHint: l.DisplayHint, LinkedAt: tsp(l.LinkedAt)})
	}
	return out, nil
}

// UnlinkIdentity implements IdentityService.
func (s *Server) UnlinkIdentity(ctx context.Context, req *wb.UnlinkIdentityRequest) (*wb.UnlinkIdentityResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Store.Unlink(ctx, c.ID, req.GetIssuer(), req.GetSubject()); err != nil {
		return nil, toStatus(err)
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.IdentityUnlinked, ActorID: c.ID, Surface: c.Surface, Summary: "Unlinked an identity",
		Detail: map[string]any{"issuer": req.GetIssuer()}})
	return &wb.UnlinkIdentityResponse{}, nil
}
