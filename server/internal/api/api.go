// Package api serves Workbench's gRPC services on kagent's own gRPC server,
// so they share its transport (gRPC and gRPC-Web), authenticator and
// interceptors. Each handler translates between the wire and the domain
// packages; no rule lives here.
package api

import (
	"context"
	"errors"
	"strconv"

	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	wb "github.com/thehaymaker/kagent-porcelain/server/gen/workbench/v1"
	"github.com/thehaymaker/kagent-porcelain/server/internal/approval"
	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/authz"
	"github.com/thehaymaker/kagent-porcelain/server/internal/components"
	"github.com/thehaymaker/kagent-porcelain/server/internal/identity"
	"github.com/thehaymaker/kagent-porcelain/server/internal/kagent"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/registry"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/usage"
)

// LinkVerifier checks tokens presented to LinkIdentity.
type LinkVerifier interface {
	VerifyLinkToken(ctx context.Context, raw string) (issuer, subject, hint string, err error)
}

// Server implements every Workbench service.
type Server struct {
	wb.UnimplementedIdentityServiceServer
	wb.UnimplementedApprovalServiceServer
	wb.UnimplementedAppServiceServer
	wb.UnimplementedAuditServiceServer
	wb.UnimplementedNotificationServiceServer
	wb.UnimplementedUsageServiceServer
	wb.UnimplementedComponentServiceServer

	Store      *store.Store
	Authz      *authz.Authorizer
	Approvals  *approval.Service
	Registry   *registry.Registry
	Meter      *usage.Meter
	Components *components.Validator
	Links      LinkVerifier
	Kagent     kagent.Client
	Audit      audit.Recorder
	Policy     policy.Policy
	// callerFrom is replaced in tests.
	callerFrom func(context.Context) (identity.Caller, bool)
}

// Register adds the services to kagent's gRPC server. It is app.Options.GRPCServices.
func (s *Server) Register(r grpc.ServiceRegistrar) {
	wb.RegisterIdentityServiceServer(r, s)
	wb.RegisterApprovalServiceServer(r, s)
	wb.RegisterAppServiceServer(r, s)
	wb.RegisterAuditServiceServer(r, s)
	wb.RegisterNotificationServiceServer(r, s)
	wb.RegisterUsageServiceServer(r, s)
	wb.RegisterComponentServiceServer(r, s)
}

// MethodPolicies declares access for every method Register adds. kagent's
// authentication interceptor refuses a method without a policy, so a method
// missing here is closed, never open. Workbench's handlers then apply their
// own per-object checks.
func MethodPolicies() map[string]auth.AccessMode {
	out := map[string]auth.AccessMode{}
	for _, sd := range []grpc.ServiceDesc{wb.IdentityService_ServiceDesc, wb.ApprovalService_ServiceDesc, wb.AppService_ServiceDesc,
		wb.AuditService_ServiceDesc, wb.NotificationService_ServiceDesc, wb.UsageService_ServiceDesc, wb.ComponentService_ServiceDesc} {
		for _, m := range sd.Methods {
			out["/"+sd.ServiceName+"/"+m.MethodName] = accessFor(m.MethodName)
		}
	}
	return out
}

func accessFor(method string) auth.AccessMode {
	for _, p := range []struct {
		prefix string
		mode   auth.AccessMode
	}{{"Get", auth.AccessRead}, {"List", auth.AccessRead}, {"Unlink", auth.AccessDelete}, {"Forget", auth.AccessDelete}, {"Archive", auth.AccessDelete}, {"Create", auth.AccessCreate}, {"Request", auth.AccessCreate}, {"Register", auth.AccessCreate}, {"Link", auth.AccessCreate}} {
		if len(method) >= len(p.prefix) && method[:len(p.prefix)] == p.prefix {
			return p.mode
		}
	}
	return auth.AccessUpdate
}

func (s *Server) caller(ctx context.Context) (identity.Caller, error) {
	from := s.callerFrom
	if from == nil {
		from = identity.FromContext
	}
	c, ok := from(ctx)
	if !ok || c.ID == "" {
		return c, status.Error(codes.Unauthenticated, "sign in to Workbench")
	}
	if c.IsRuntime() {
		return c, status.Error(codes.PermissionDenied, "agents use Workbench through its MCP tools")
	}
	return c, nil
}

func (s *Server) has(c identity.Caller, r authz.Role) bool { return s.Authz.Has(c.Groups, r) }

// toStatus maps domain errors to gRPC status codes, keeping their messages,
// which are written for the person who will read them.
func toStatus(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, approval.ErrPermission):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, approval.ErrInvalid), errors.Is(err, registry.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrConflict), errors.Is(err, registry.ErrNotAllowed):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "canceled")
	}
	return status.Error(codes.Internal, "internal error")
}

func page(p *wb.PageRequest) store.Page {
	off, _ := strconv.Atoi(p.GetPageToken())
	if off < 0 {
		off = 0
	}
	return store.Page{Size: int(p.GetPageSize()), Offset: off}
}

func nextPage(p store.Page, got int) *wb.PageResponse {
	size := p.Size
	if size <= 0 || size > 200 {
		size = 50
	}
	if got < size {
		return &wb.PageResponse{}
	}
	return &wb.PageResponse{NextPageToken: strconv.Itoa(p.Offset + got)}
}

func toStruct(m map[string]any) *structpb.Struct {
	if m == nil {
		return nil
	}
	st, err := structpb.NewStruct(normalize(m).(map[string]any))
	if err != nil {
		return nil
	}
	return st
}

// normalize converts values structpb cannot take (typed slices, ints) into
// JSON-like ones.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalize(val)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = val
		}
		return out
	case []map[string]any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalize(val)
		}
		return out
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case int32:
		return float64(x)
	}
	return v
}
