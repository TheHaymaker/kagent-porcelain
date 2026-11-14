package approval

import (
	"context"
	"errors"
	"testing"

	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
	"github.com/thehaymaker/kagent-porcelain/server/internal/identity"
	"github.com/thehaymaker/kagent-porcelain/server/internal/kagent"
	"github.com/thehaymaker/kagent-porcelain/server/internal/notify"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/testdb"
)

type sent struct {
	as, actor, agent string
	msg              hitl.Message
}

// fakeKagent holds the paused task state and records what was sent.
type fakeKagent struct {
	tasks map[string]kagent.Task
	sent  []sent
	fail  error
}

func (f *fakeKagent) Session(_ context.Context, as, id string) (kagent.SessionInfo, error) {
	if as != identity.SystemPrincipal {
		return kagent.SessionInfo{}, errors.New("session lookups must use the system principal")
	}
	return kagent.SessionInfo{ID: id, Agent: "workbench/builder", Creator: "alice"}, nil
}
func (f *fakeKagent) Task(_ context.Context, as, agent, id string) (kagent.Task, error) {
	if as != "alice" {
		return kagent.Task{}, errors.New("tasks must be read as their owner")
	}
	return f.tasks[id], nil
}
func (f *fakeKagent) Send(_ context.Context, as, actor, agent string, msg hitl.Message) error {
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, sent{as, actor, agent, msg})
	return nil
}
func (f *fakeKagent) PausedTasks(_ context.Context, as, agent, session string) ([]kagent.Task, error) {
	var out []kagent.Task
	for _, t := range f.tasks {
		if t.State == hitl.StateInputRequired {
			out = append(out, t)
		}
	}
	return out, nil
}
func (f *fakeKagent) Watch(context.Context, string, string, string, string) error { return nil }

type fakeNotify struct{ notices map[string][]notify.Notice }

func (f *fakeNotify) Notify(_ context.Context, ids []string, n notify.Notice) {
	for _, id := range ids {
		f.notices[id] = append(f.notices[id], n)
	}
}

type fakePromoter struct{ promoted []string }

func (f *fakePromoter) Promote(_ context.Context, a store.Approval) (store.Deployment, error) {
	f.promoted = append(f.promoted, a.ID)
	return store.Deployment{}, nil
}

func pause(taskID string, tools ...hitl.Tool) *hitl.Message {
	raw := make([]any, len(tools))
	for i, t := range tools {
		raw[i] = map[string]any{"id": t.ID, "name": t.Name, "args": t.Args}
	}
	return &hitl.Message{Extensions: []string{hitl.ExtensionURI}, Metadata: map[string]any{hitl.ExtensionURI: map[string]any{
		"type": "tool_approval_request", "hint": "Approval is required", "tools": raw}}}
}

type env struct {
	svc   *Service
	k     *fakeKagent
	n     *fakeNotify
	p     *fakePromoter
	audit *audit.Memory
	s     *store.Store
}

func setup(t *testing.T) env {
	s := testdb.New(t)
	ctx := context.Background()
	for _, p := range []store.Person{{ID: "alice", Groups: []string{"requesters", "approvers"}}, {ID: "bob", Groups: []string{"approvers"}}, {ID: "carol", Groups: []string{"requesters"}}} {
		if err := s.UpsertPerson(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	e := env{k: &fakeKagent{tasks: map[string]kagent.Task{}}, n: &fakeNotify{notices: map[string][]notify.Notice{}}, p: &fakePromoter{}, audit: &audit.Memory{}, s: s}
	e.svc = &Service{Store: s, Policy: policy.Default("approvers"), Kagent: e.k, Audit: e.audit, Notify: e.n, Promoter: e.p}
	return e
}

func (e env) observe(t *testing.T, taskID string, msg *hitl.Message) store.Approval {
	t.Helper()
	e.k.tasks[taskID] = kagent.Task{ID: taskID, ContextID: "ctx", State: hitl.StateInputRequired, Message: msg}
	if err := e.svc.Observe(context.Background(), Observation{SessionID: "ctx", TaskID: taskID, State: hitl.StateInputRequired, Message: msg}); err != nil {
		t.Fatal(err)
	}
	list, _ := e.s.ListSessionApprovals(context.Background(), "ctx")
	return list[len(list)-1]
}

var (
	alice = identity.Caller{ID: "alice", Groups: []string{"requesters", "approvers"}}
	bob   = identity.Caller{ID: "bob", Groups: []string{"approvers"}, Surface: "teams"}
	carol = identity.Caller{ID: "carol", Groups: []string{"requesters"}}
)

func TestProductionDeployNeedsSomeoneElse(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	msg := pause("t1", hitl.Tool{ID: "c1", Name: "workbench_request_promotion", Args: map[string]any{"appId": "app_1", "revision": "r7"}})
	a := e.observe(t, "t1", msg)
	again := e.observe(t, "t1", msg)
	if again.ID != a.ID {
		t.Fatal("the same pause must yield one approval")
	}
	if a.Kind != policy.ProductionDeploy || a.RequesterMayDecide || a.AppID != "app_1" {
		t.Fatalf("approval %+v", a)
	}
	if len(e.n.notices["bob"]) != 1 || len(e.n.notices["alice"]) != 1 || e.n.notices["alice"][0].Title[:7] != "Waiting" {
		t.Fatalf("approver must be asked and requester told it is waiting: %+v", e.n.notices)
	}
	if ViewFor(alice, a).CallerMayDecide || !ViewFor(bob, a).CallerMayDecide || ViewFor(carol, a).CallerMayDecide {
		t.Fatal("view permissions")
	}

	if _, err := e.svc.Decide(ctx, alice, Decision{ApprovalID: a.ID, Approved: true}); !errors.Is(err, ErrPermission) {
		t.Fatalf("requester must not approve their own production deploy, even as an approver: %v", err)
	}
	if _, err := e.svc.Decide(ctx, carol, Decision{ApprovalID: a.ID, Approved: true}); !errors.Is(err, ErrPermission) {
		t.Fatal("non-approver must not decide")
	}
	if _, err := e.svc.Decide(ctx, bob, Decision{ApprovalID: a.ID, Approved: true, Remember: "always"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("always-ask answers must not be rememberable")
	}
	if _, err := e.svc.Decide(ctx, bob, Decision{ApprovalID: a.ID, Approved: false}); !errors.Is(err, ErrInvalid) {
		t.Fatal("refusing an always-ask action needs a reason")
	}
	got, err := e.svc.Decide(ctx, bob, Decision{ApprovalID: a.ID, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.ApprovalApproved || !got.Delivered || got.DecidedByID != "bob" {
		t.Fatalf("decided %+v", got)
	}
	if len(e.p.promoted) != 1 {
		t.Fatal("approved production deploy must promote")
	}
	if len(e.k.sent) != 1 {
		t.Fatal("decision must be sent into the task")
	}
	s := e.k.sent[0]
	if s.as != "alice" || s.actor != "bob" || s.msg.TaskID != "t1" || s.msg.ContextID != "ctx" {
		t.Fatalf("decision must continue alice's task, recorded as bob's: %+v", s)
	}
	payload := s.msg.Metadata[hitl.ExtensionURI].(map[string]any)
	if payload["type"] != "tool_approval_response" {
		t.Fatal("wrong payload")
	}
	if _, err := e.svc.Decide(ctx, bob, Decision{ApprovalID: a.ID, Approved: true}); !errors.Is(err, ErrConflict) {
		t.Fatal("deciding twice must conflict")
	}
	last := e.audit.Events[len(e.audit.Events)-1]
	if last.Type != audit.ApprovalDelivered {
		t.Fatalf("audit %v", e.audit.Types())
	}
	for _, ev := range e.audit.Events {
		if ev.Type == audit.ApprovalDecided && (ev.ActorID != "bob" || ev.OnBehalfOfID != "alice" || ev.Surface != "teams") {
			t.Fatalf("decision audit must name the decider and the requester: %+v", ev)
		}
	}
}

func TestRememberedAnswers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.observe(t, "t1", pause("t1", hitl.Tool{ID: "c1", Name: "gitlab_commit_files"}))
	if a.Kind != policy.Ordinary || !a.RequesterMayDecide {
		t.Fatalf("%+v", a)
	}
	if len(e.n.notices["bob"]) != 0 {
		t.Fatal("ordinary approvals are not sent to approvers")
	}
	if _, err := e.svc.Decide(ctx, bob, Decision{ApprovalID: a.ID, Approved: true}); !errors.Is(err, ErrPermission) {
		t.Fatal("ordinary approvals are the requester's")
	}
	if _, err := e.svc.Decide(ctx, alice, Decision{ApprovalID: a.ID, Approved: true, Remember: "conversation"}); err != nil {
		t.Fatal(err)
	}
	next := e.observe(t, "t2", pause("t2", hitl.Tool{ID: "c9", Name: "gitlab_commit_files"}))
	if next.State != store.ApprovalApproved || next.Reason != "remembered answer" || !next.Delivered {
		t.Fatalf("remembered answer must apply: %+v", next)
	}
	if len(e.k.sent) != 2 || e.k.sent[1].msg.TaskID != "t2" {
		t.Fatal("remembered decision must be sent")
	}
	mixed := e.observe(t, "t3", pause("t3", hitl.Tool{ID: "a", Name: "gitlab_commit_files"}, hitl.Tool{ID: "b", Name: "gitlab_create_merge_request"}))
	if mixed.State != store.ApprovalPending {
		t.Fatal("a remembered answer applies only when every call has one")
	}
}

func TestPerCallDecisions(t *testing.T) {
	e := setup(t)
	a := e.observe(t, "t1", pause("t1", hitl.Tool{ID: "a", Name: "read_x"}, hitl.Tool{ID: "b", Name: "write_y"}))
	got, err := e.svc.Decide(context.Background(), alice, Decision{ApprovalID: a.ID, Approved: true, Reason: "not y",
		Calls: []hitl.Decision{{ID: "b", Approved: false}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.ApprovalRejected || got.Decisions[0].Approved != true || got.Decisions[1].Reason != "not y" {
		t.Fatalf("%+v", got.Decisions)
	}
	if _, err := e.svc.Decide(context.Background(), alice, Decision{ApprovalID: a.ID, Calls: []hitl.Decision{{ID: "zzz"}}}); err == nil {
		t.Fatal("decided approval")
	}
}

func TestStaleDeliveryAndRetry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.observe(t, "t1", pause("t1", hitl.Tool{ID: "a", Name: "x"}))
	e.k.fail = errors.New("kagent down")
	got, err := e.svc.Decide(ctx, alice, Decision{ApprovalID: a.ID, Approved: true})
	if err != nil || got.Delivered {
		t.Fatalf("decision stands even when delivery fails: %v %+v", err, got)
	}
	e.k.fail = nil
	und, _ := e.s.ListUndelivered(ctx, 10)
	for _, u := range und {
		e.svc.deliver(ctx, u)
	}
	got, _ = e.s.GetApproval(ctx, a.ID)
	if !got.Delivered || len(e.k.sent) != 1 {
		t.Fatal("retry must deliver")
	}

	b := e.observe(t, "t2", pause("t2", hitl.Tool{ID: "a", Name: "x"}))
	e.k.tasks["t2"] = kagent.Task{ID: "t2", State: hitl.StateCanceled}
	got, _ = e.svc.Decide(ctx, alice, Decision{ApprovalID: b.ID, Approved: true})
	if !got.Delivered || len(e.k.sent) != 1 {
		t.Fatal("a decision for a task that moved on must not be sent")
	}
	if e.audit.Types()[len(e.audit.Events)-1] != audit.ApprovalDeliveryStale {
		t.Fatal("stale delivery must be recorded")
	}
}

func TestTerminalSupersedesAndNotifies(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.observe(t, "t1", pause("t1", hitl.Tool{ID: "a", Name: "x"}))
	if err := e.svc.Observe(ctx, Observation{SessionID: "ctx", TaskID: "t1", State: hitl.StateCompleted}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.s.GetApproval(ctx, a.ID)
	if got.State != store.ApprovalSuperseded {
		t.Fatal("pending approval on a finished task must be superseded")
	}
	ns := e.n.notices["alice"]
	if ns[len(ns)-1].Kind != notify.TaskFinished {
		t.Fatal("requester must hear the task finished")
	}
}

func TestAskUserNotifiesOnly(t *testing.T) {
	e := setup(t)
	msg := &hitl.Message{Extensions: []string{hitl.ExtensionURI}, Metadata: map[string]any{hitl.ExtensionURI: map[string]any{"type": "ask_user_request", "id": "q1"}}}
	if err := e.svc.Observe(context.Background(), Observation{SessionID: "ctx", TaskID: "t", State: hitl.StateInputRequired, Message: msg}); err != nil {
		t.Fatal(err)
	}
	list, _ := e.s.ListSessionApprovals(context.Background(), "ctx")
	if len(list) != 0 || e.n.notices["alice"][0].Kind != notify.InputNeeded {
		t.Fatal("questions are answered in chat, not as approvals")
	}
}

func TestReassign(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.observe(t, "t1", pause("t1", hitl.Tool{ID: "a", Name: "bigquery_delete_table"}))
	if _, err := e.svc.Reassign(ctx, carol, a.ID, "bob", ""); !errors.Is(err, ErrPermission) {
		t.Fatal("a bystander must not reassign")
	}
	if _, err := e.svc.Reassign(ctx, alice, a.ID, "alice", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("cannot assign to the requester")
	}
	got, err := e.svc.Reassign(ctx, alice, a.ID, "carol", "you own that dataset")
	if err != nil || got.ApproverIDs[0] != "carol" {
		t.Fatal(err)
	}
	if !ViewFor(carol, got).CallerMayDecide {
		t.Fatal("named approver must be able to decide")
	}
}

func TestRequestPromotionFromCatalogue(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	app, _ := e.s.CreateApp(ctx, store.App{Name: "tracker", OwnerID: "alice"})
	if _, err := e.svc.RequestPromotion(ctx, alice, app, "", "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("nothing to promote yet")
	}
	_, _ = e.s.RecordDeployment(ctx, store.Deployment{AppID: app.ID, Environment: store.EnvPreview, Revision: "r1", DeployedBy: "x"})
	app, _ = e.s.GetApp(ctx, app.ID)
	a, err := e.svc.RequestPromotion(ctx, alice, app, "", "please", "")
	if err != nil || a.Kind != policy.ProductionDeploy || a.TaskID != "" {
		t.Fatalf("%v %+v", err, a)
	}
	again, _ := e.svc.RequestPromotion(ctx, alice, app, "r1", "", "")
	if again.ID != a.ID {
		t.Fatal("one open request per revision")
	}
	got, err := e.svc.Decide(ctx, bob, Decision{ApprovalID: a.ID, Approved: true})
	if err != nil || !got.Delivered || len(e.p.promoted) != 1 || len(e.k.sent) != 0 {
		t.Fatalf("catalogue promotion: %v %+v", err, got)
	}
}

func TestSyncSessionFindsPausesWithoutPush(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.k.tasks["t9"] = kagent.Task{ID: "t9", ContextID: "ctx", State: hitl.StateInputRequired, Message: pause("t9", hitl.Tool{ID: "a", Name: "bigquery_delete_table"})}
	if err := e.svc.SyncSession(ctx, carol, "ctx"); !errors.Is(err, ErrPermission) {
		t.Fatal("only the requester may sync their session")
	}
	if err := e.svc.SyncSession(ctx, alice, "ctx"); err != nil {
		t.Fatal(err)
	}
	list, _ := e.s.ListSessionApprovals(ctx, "ctx")
	if len(list) != 1 || list[0].Kind != policy.DataDeletion {
		t.Fatalf("%+v", list)
	}
}
