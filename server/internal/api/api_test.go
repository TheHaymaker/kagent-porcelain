package api

import (
	"context"
	"testing"

	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	wb "github.com/thehaymaker/kagent-porcelain/server/gen/workbench/v1"
	"github.com/thehaymaker/kagent-porcelain/server/internal/approval"
	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/authz"
	"github.com/thehaymaker/kagent-porcelain/server/internal/components"
	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
	"github.com/thehaymaker/kagent-porcelain/server/internal/identity"
	"github.com/thehaymaker/kagent-porcelain/server/internal/kagent"
	"github.com/thehaymaker/kagent-porcelain/server/internal/notify"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/registry"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/testdb"
)

func TestEveryMethodHasAPolicy(t *testing.T) {
	p := MethodPolicies()
	if len(p) < 30 {
		t.Fatalf("expected every Workbench method, got %d", len(p))
	}
	for m, mode := range p {
		if mode == auth.AccessPublic || mode == auth.AccessRuntime {
			t.Errorf("%s must require a signed-in person, got %s", m, mode)
		}
	}
	if p["/workbench.v1.ApprovalService/Decide"] != auth.AccessUpdate || p["/workbench.v1.AppService/ListApps"] != auth.AccessRead {
		t.Fatal("access modes")
	}
}

type fakeKagent struct{ tasks map[string]kagent.Task }

func (f fakeKagent) Session(_ context.Context, _, id string) (kagent.SessionInfo, error) {
	return kagent.SessionInfo{ID: id, Agent: "workbench/builder", Creator: "alice"}, nil
}
func (f fakeKagent) Task(_ context.Context, _, _, id string) (kagent.Task, error) {
	return f.tasks[id], nil
}
func (f fakeKagent) PausedTasks(context.Context, string, string, string) ([]kagent.Task, error) {
	var out []kagent.Task
	for _, t := range f.tasks {
		out = append(out, t)
	}
	return out, nil
}
func (fakeKagent) Send(context.Context, string, string, string, hitl.Message) error { return nil }
func (fakeKagent) Watch(context.Context, string, string, string, string) error      { return nil }

type nopNotify struct{}

func (nopNotify) Notify(context.Context, []string, notify.Notice) {}

func newServer(t *testing.T) (*Server, *store.Store, *identity.Caller) {
	s := testdb.New(t)
	ctx := context.Background()
	_ = s.UpsertPerson(ctx, store.Person{ID: "alice", Groups: []string{"requesters"}})
	_ = s.UpsertPerson(ctx, store.Person{ID: "bob", Groups: []string{"requesters", "approvers"}})
	az, _ := authz.New(authz.Config{PlatformGroups: []string{"platform"}, AgentNamespaces: []string{"workbench"}}, []string{"approvers"})
	pol := policy.Default("approvers")
	rec := &audit.Memory{}
	reg := &registry.Registry{Store: s, Deployer: registry.LogDeployer{}, Audit: rec}
	k := fakeKagent{tasks: map[string]kagent.Task{"t1": {ID: "t1", ContextID: "ctx", State: hitl.StateInputRequired, Message: &hitl.Message{
		Extensions: []string{hitl.ExtensionURI}, Metadata: map[string]any{hitl.ExtensionURI: map[string]any{"type": "tool_approval_request",
			"tools": []any{map[string]any{"id": "c1", "name": "cloudrun_set_iam_invoker", "args": map[string]any{"member": "group:x"}}}}}}}}}
	svc := &approval.Service{Store: s, Policy: pol, Kagent: k, Audit: rec, Notify: nopNotify{}, Promoter: reg}
	v, _ := components.NewValidator()
	current := &identity.Caller{}
	srv := &Server{Store: s, Authz: az, Approvals: svc, Registry: reg, Components: v, Kagent: k, Audit: rec, Policy: pol,
		callerFrom: func(context.Context) (identity.Caller, bool) { return *current, current.ID != "" }}
	return srv, s, current
}

func code(err error) codes.Code { return status.Code(err) }

func TestApprovalFlowOverAPI(t *testing.T) {
	srv, _, as := newServer(t)
	ctx := context.Background()
	*as = identity.Caller{ID: "alice", Groups: []string{"requesters"}}
	resp, err := srv.ListSessionApprovals(ctx, &wb.ListSessionApprovalsRequest{SessionId: "ctx"})
	if err != nil || len(resp.Approvals) != 1 {
		t.Fatalf("pause must surface as an approval on read: %v %v", err, resp)
	}
	a := resp.Approvals[0]
	if a.Kind != wb.ActionKind_ACTION_KIND_IAM_CHANGE || a.CallerMayDecide || a.Calls[0].Args.AsMap()["member"] != "group:x" {
		t.Fatalf("%+v", a)
	}
	if _, err := srv.Decide(ctx, &wb.DecideRequest{ApprovalId: a.Id, Approved: true}); code(err) != codes.PermissionDenied {
		t.Fatalf("requester must be refused: %v", err)
	}

	*as = identity.Caller{ID: "mallory", Groups: []string{"requesters"}}
	if _, err := srv.GetApproval(ctx, &wb.GetApprovalRequest{Id: a.Id}); code(err) != codes.NotFound {
		t.Fatal("a stranger must not see the approval")
	}
	if r, _ := srv.ListSessionApprovals(ctx, &wb.ListSessionApprovalsRequest{SessionId: "ctx"}); r != nil && len(r.Approvals) != 0 {
		t.Fatal("a stranger must not list someone else's session approvals")
	}

	*as = identity.Caller{ID: "bob", Groups: []string{"requesters", "approvers"}, Surface: "web"}
	inbox, err := srv.ListInbox(ctx, &wb.ListInboxRequest{View: wb.InboxView_INBOX_VIEW_TO_DECIDE})
	if err != nil || len(inbox.Approvals) != 1 || !inbox.Approvals[0].CallerMayDecide {
		t.Fatalf("approver inbox: %v %+v", err, inbox)
	}
	if _, err := srv.Decide(ctx, &wb.DecideRequest{ApprovalId: a.Id, Approved: true, Remember: wb.RememberScope_REMEMBER_SCOPE_ALWAYS}); code(err) != codes.InvalidArgument {
		t.Fatalf("always-ask cannot be remembered: %v", err)
	}
	d, err := srv.Decide(ctx, &wb.DecideRequest{ApprovalId: a.Id, Approved: true})
	if err != nil || d.Approval.State != wb.ApprovalState_APPROVAL_STATE_APPROVED || d.Approval.DecidedBy.Id != "bob" {
		t.Fatalf("%v %+v", err, d)
	}
	if _, err := srv.Decide(ctx, &wb.DecideRequest{ApprovalId: a.Id, Approved: true}); code(err) != codes.FailedPrecondition {
		t.Fatal("second decision")
	}
}

func TestRolesAndLimits(t *testing.T) {
	srv, _, as := newServer(t)
	ctx := context.Background()
	*as = identity.Caller{ID: "bob", Groups: []string{"approvers"}}
	me, err := srv.GetMe(ctx, &wb.GetMeRequest{})
	if err != nil || len(me.Roles) != 2 {
		t.Fatalf("%v %v", err, me.GetRoles())
	}
	if _, err := srv.SetLimit(ctx, &wb.SetLimitRequest{Limit: &wb.Limit{Subject: wb.LimitSubject_LIMIT_SUBJECT_PERSON, SubjectId: "x", MonthlyCents: 1}}); code(err) != codes.PermissionDenied {
		t.Fatal("only platform engineers set limits")
	}
	*as = identity.Caller{ID: "pat", Groups: []string{"platform"}}
	if _, err := srv.SetLimit(ctx, &wb.SetLimitRequest{Limit: &wb.Limit{Subject: wb.LimitSubject_LIMIT_SUBJECT_TEAM, SubjectId: "requesters", MonthlyCents: 500}}); err != nil {
		t.Fatal(err)
	}
	*as = identity.Caller{ID: "alice", Groups: []string{"requesters"}}
	l, _ := srv.ListLimits(ctx, &wb.ListLimitsRequest{})
	if len(l.Limits) != 1 {
		t.Fatal("a team member sees their team's limit")
	}
	*as = identity.Caller{ID: "x", RuntimeSessionID: "s", CreatorID: "alice"}
	if _, err := srv.GetMe(ctx, &wb.GetMeRequest{}); code(err) != codes.PermissionDenied {
		t.Fatal("agent runtimes use MCP tools, not the person API")
	}
}

func TestAppsOverAPI(t *testing.T) {
	srv, s, as := newServer(t)
	ctx := context.Background()
	app, _ := s.CreateApp(ctx, store.App{Name: "tracker", OwnerID: "alice"})
	_, _ = s.RecordDeployment(ctx, store.Deployment{AppID: app.ID, Environment: store.EnvPreview, Revision: "r1", DeployedBy: "x", URL: "https://p"})
	*as = identity.Caller{ID: "bob", Groups: []string{"approvers"}}
	if _, err := srv.RequestPromotion(ctx, &wb.RequestPromotionRequest{AppId: app.ID}); code(err) != codes.PermissionDenied {
		t.Fatal("only the owner may request promotion")
	}
	*as = identity.Caller{ID: "alice"}
	r, err := srv.RequestPromotion(ctx, &wb.RequestPromotionRequest{AppId: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := srv.GetApp(ctx, &wb.GetAppRequest{Id: app.ID})
	if got.App.PendingApprovalId != r.ApprovalId || got.App.Owner.Id != "alice" || got.App.Status != wb.AppStatus_APP_STATUS_PREVIEW {
		t.Fatalf("%+v", got.App)
	}
	list, _ := srv.ListApps(ctx, &wb.ListAppsRequest{Mine: true})
	if len(list.Apps) != 1 {
		t.Fatal("mine")
	}
}

func TestNotificationPreferences(t *testing.T) {
	srv, _, as := newServer(t)
	ctx := context.Background()
	*as = identity.Caller{ID: "alice"}
	got, err := srv.SetPreferences(ctx, &wb.SetPreferencesRequest{Preferences: []*wb.Preference{{Kind: wb.NotificationKind_NOTIFICATION_KIND_TASK_FINISHED, Channels: []wb.Channel{wb.Channel_CHANNEL_EMAIL}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got.Preferences {
		if p.Kind == wb.NotificationKind_NOTIFICATION_KIND_TASK_FINISHED && (len(p.Channels) != 2 || p.Channels[0] != wb.Channel_CHANNEL_IN_APP) {
			t.Fatalf("in-app is always kept: %v", p.Channels)
		}
	}
}
