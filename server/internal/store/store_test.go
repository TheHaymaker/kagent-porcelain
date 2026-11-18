package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/testdb"
)

var ctx = context.Background()

func pendingApproval(fp string) store.Approval {
	return store.Approval{
		Fingerprint: fp, SessionID: "ctx-1", TaskID: "task-1", Agent: "workbench/builder", RequesterID: "alice",
		Kind: policy.ProductionDeploy, Calls: []store.Call{{Tool: hitl.Tool{ID: "t1", Name: "workbench_request_promotion"}, Kind: policy.ProductionDeploy}},
		ApproverGroups: []string{"approvers"}, RequesterMayDecide: false,
	}
}

func TestApprovalLifecycle(t *testing.T) {
	s := testdb.New(t)
	a, created, err := s.CreateApproval(ctx, pendingApproval("fp1"))
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	again, created, err := s.CreateApproval(ctx, pendingApproval("fp1"))
	if err != nil || created || again.ID != a.ID {
		t.Fatalf("second observation of the same pause must return the first approval: %v %v", created, err)
	}

	inbox, err := s.ListPendingForDecider(ctx, "bob", []string{"approvers"}, store.Page{})
	if err != nil || len(inbox) != 1 {
		t.Fatalf("approver inbox: %v %d", err, len(inbox))
	}
	mine, err := s.ListPendingForDecider(ctx, "alice", nil, store.Page{})
	if err != nil || len(mine) != 0 {
		t.Fatalf("requester barred from deciding must not see it to decide: %d", len(mine))
	}

	// The database refuses a self-approval even if code above it slipped.
	if _, err := s.Decide(ctx, a.ID, "alice", true, nil, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("self-approval must violate the constraint, got %v", err)
	}
	d, err := s.Decide(ctx, a.ID, "bob", true, []hitl.Decision{{ID: "t1", Approved: true}}, "looks good")
	if err != nil || d.State != store.ApprovalApproved || d.DecidedAt == nil {
		t.Fatalf("decide: %v %+v", err, d)
	}
	if _, err := s.Decide(ctx, a.ID, "carol", false, nil, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second decision must conflict, got %v", err)
	}
	und, _ := s.ListUndelivered(ctx, 10)
	if len(und) != 1 {
		t.Fatal("decided approval must await delivery")
	}
	if err := s.MarkDelivered(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	und, _ = s.ListUndelivered(ctx, 10)
	if len(und) != 0 {
		t.Fatal("delivered approval must not be redelivered")
	}
}

func TestSupersede(t *testing.T) {
	s := testdb.New(t)
	old, _, _ := s.CreateApproval(ctx, pendingApproval("old"))
	cur, _, _ := s.CreateApproval(ctx, pendingApproval("new"))
	changed, err := s.SupersedePending(ctx, "task-1", "new")
	if err != nil || len(changed) != 1 || changed[0].ID != old.ID {
		t.Fatalf("supersede: %v %+v", err, changed)
	}
	got, _ := s.GetApproval(ctx, cur.ID)
	if got.State != store.ApprovalPending {
		t.Fatal("current pause must stay pending")
	}
}

func TestRemembered(t *testing.T) {
	s := testdb.New(t)
	if _, err := s.Remember(ctx, store.RememberedAnswer{PersonID: "alice", Scope: "always", ToolName: "gitlab_commit_files", Agent: "a", Approved: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remember(ctx, store.RememberedAnswer{PersonID: "alice", Scope: "conversation", SessionID: "s1", ToolName: "gitlab_commit_files", Agent: "a", Approved: false}); err != nil {
		t.Fatal(err)
	}
	r, err := s.FindRemembered(ctx, "alice", "a", "s1", "gitlab_commit_files")
	if err != nil || r.Approved || r.Scope != "conversation" {
		t.Fatalf("conversation answer must win in its conversation: %+v %v", r, err)
	}
	r, err = s.FindRemembered(ctx, "alice", "a", "s2", "gitlab_commit_files")
	if err != nil || !r.Approved {
		t.Fatalf("always answer must apply elsewhere: %+v %v", r, err)
	}
	if _, err := s.FindRemembered(ctx, "bob", "a", "s1", "gitlab_commit_files"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("answers are per person")
	}
	list, _ := s.ListRemembered(ctx, "alice")
	if err := s.Forget(ctx, "bob", list[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("cannot forget someone else's answer")
	}
	if err := s.Forget(ctx, "alice", list[0].ID); err != nil {
		t.Fatal(err)
	}
}

func TestAppsAndDeployments(t *testing.T) {
	s := testdb.New(t)
	app, err := s.CreateApp(ctx, store.App{Name: "expense-tracker", OwnerID: "alice", Scaffold: "python-fastapi-cloudrun"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateApp(ctx, store.App{Name: "Expense-Tracker", OwnerID: "bob"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("names are unique case-insensitively: %v", err)
	}
	if _, err := s.RecordDeployment(ctx, store.Deployment{AppID: app.ID, Environment: store.EnvProduction, Revision: "r1", DeployedBy: "x"}); err == nil {
		t.Fatal("a production deployment without an approval must be refused by the table")
	}
	if _, err := s.RecordDeployment(ctx, store.Deployment{AppID: app.ID, Environment: store.EnvPreview, Revision: "r1", URL: "https://p", DeployedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetApp(ctx, app.ID)
	if got.Status != store.AppPreview {
		t.Fatalf("status %s", got.Status)
	}
	if e, ok := got.Env(store.EnvPreview); !ok || e.Revision != "r1" {
		t.Fatal("preview env not recorded")
	}
	if _, err := s.RecordDeployment(ctx, store.Deployment{AppID: app.ID, Environment: store.EnvProduction, Revision: "r1", DeployedBy: "workbench", ApprovalID: "apr_1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordDeployment(ctx, store.Deployment{AppID: app.ID, Environment: store.EnvPreview, Revision: "r2", DeployedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetApp(ctx, app.ID)
	if got.Status != store.AppLive {
		t.Fatal("a new preview must not demote a live app")
	}
	apps, err := s.ListApps(ctx, store.AppFilter{OwnerID: "alice", Query: "expense"}, store.Page{})
	if err != nil || len(apps) != 1 || len(apps[0].Environments) != 2 {
		t.Fatalf("list: %v %+v", err, apps)
	}
	deps, _ := s.ListDeployments(ctx, app.ID)
	if len(deps) != 3 {
		t.Fatalf("deployments %d", len(deps))
	}
}

func TestAuditIsAppendOnly(t *testing.T) {
	s := testdb.New(t)
	e, err := s.AppendAudit(ctx, store.AuditEvent{Type: "approval.decided", ActorID: "bob", SessionID: "s1", Summary: "Approved"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE audit_event SET summary = 'x' WHERE id = $1`, e.ID); err == nil {
		t.Fatal("audit rows must not be updatable")
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM audit_event WHERE id = $1`, e.ID); err == nil {
		t.Fatal("audit rows must not be deletable")
	}
	_ = s.WatchSession(ctx, store.WatchedSession{SessionID: "s1", Agent: "a", RequesterID: "alice"})
	visible, _ := s.ListAudit(ctx, store.AuditFilter{VisibleTo: "alice"}, store.Page{})
	if len(visible) != 1 {
		t.Fatal("requester must see events on their own session")
	}
	hidden, _ := s.ListAudit(ctx, store.AuditFilter{VisibleTo: "mallory"}, store.Page{})
	if len(hidden) != 0 {
		t.Fatal("others must not")
	}
	after, _ := s.ListAudit(ctx, store.AuditFilter{AfterSeq: e.Seq}, store.Page{})
	if len(after) != 0 {
		t.Fatal("cursor")
	}
}

func TestIdentityLinks(t *testing.T) {
	s := testdb.New(t)
	for _, id := range []string{"alice", "bob"} {
		if err := s.UpsertPerson(ctx, store.Person{ID: id, Email: id + "@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.LinkIdentity(ctx, store.IdentityLink{Issuer: "ms", Subject: "oid-1", PersonID: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkIdentity(ctx, store.IdentityLink{Issuer: "ms", Subject: "oid-1", PersonID: "alice", DisplayHint: "Alice (Teams)"}); err != nil {
		t.Fatalf("relinking to the same person is fine: %v", err)
	}
	if _, err := s.LinkIdentity(ctx, store.IdentityLink{Issuer: "ms", Subject: "oid-1", PersonID: "bob"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("an identity linked to someone else must not move: %v", err)
	}
	id, err := s.ResolveLink(ctx, "ms", "oid-1")
	if err != nil || id != "alice" {
		t.Fatal("resolve")
	}
	p, err := s.FindPerson(ctx, "BOB@example.com")
	if err != nil || p.ID != "bob" {
		t.Fatal("find by email")
	}
}

func TestNotificationsDedupe(t *testing.T) {
	s := testdb.New(t)
	_, created, err := s.AddNotification(ctx, store.Notification{PersonID: "alice", Kind: "approval_needed", Title: "x", DedupeKey: "k"})
	if err != nil || !created {
		t.Fatal(err)
	}
	_, created, err = s.AddNotification(ctx, store.Notification{PersonID: "alice", Kind: "approval_needed", Title: "x", DedupeKey: "k"})
	if err != nil || created {
		t.Fatal("dedupe")
	}
	list, unread, err := s.ListNotifications(ctx, "alice", false, store.Page{})
	if err != nil || len(list) != 1 || unread != 1 {
		t.Fatal("list")
	}
	_ = s.MarkRead(ctx, "alice", nil, true)
	_, unread, _ = s.ListNotifications(ctx, "alice", false, store.Page{})
	if unread != 0 {
		t.Fatal("mark read")
	}
}

func TestUsage(t *testing.T) {
	s := testdb.New(t)
	now := time.Now()
	for i, r := range []store.UsageRecord{
		{PersonID: "alice", Model: "m", CostMicros: 1_000_000, RequestID: "r1", AppID: "app1"},
		{PersonID: "alice", Model: "m", CostMicros: 1_000_000, RequestID: "r1", AppID: "app1"},
		{PersonID: "bob", Model: "n", CostMicros: 500_000, RequestID: "r2"},
	} {
		r.Time = now
		if err := s.RecordUsage(ctx, r); err != nil {
			t.Fatal(i, err)
		}
	}
	spent, _ := s.SpentMicros(ctx, []string{"alice"}, now.Add(-time.Hour))
	if spent != 1_000_000 {
		t.Fatalf("repeated request must count once, got %d", spent)
	}
	lines, err := s.SummariseUsage(ctx, now.Add(-time.Hour), now.Add(time.Hour), store.ByModel, "")
	if err != nil || len(lines) != 2 {
		t.Fatalf("%v %+v", err, lines)
	}
}

func TestSurfaces(t *testing.T) {
	s := testdb.New(t)
	sf := store.Surface{SurfaceID: "s", SessionID: "c", CatalogID: "cat", Messages: []map[string]any{{"createSurface": map[string]any{}}}}
	if err := s.AppendSurface(ctx, sf); err != nil {
		t.Fatal(err)
	}
	sf.Messages = []map[string]any{{"updateComponents": map[string]any{}}}
	if err := s.AppendSurface(ctx, sf); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSurface(ctx, "c", "s")
	if err != nil || len(got.Messages) != 2 {
		t.Fatalf("append: %v %d", err, len(got.Messages))
	}
}
