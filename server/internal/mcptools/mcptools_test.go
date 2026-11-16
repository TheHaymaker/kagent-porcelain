package mcptools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/server/internal/approval"
	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
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

type nopKagent struct{}

func (nopKagent) Session(context.Context, string, string) (kagent.SessionInfo, error) {
	return kagent.SessionInfo{ID: "ctx", Agent: "workbench/builder", Creator: "alice"}, nil
}
func (nopKagent) Task(context.Context, string, string, string) (kagent.Task, error) {
	return kagent.Task{}, nil
}
func (nopKagent) PausedTasks(context.Context, string, string, string) ([]kagent.Task, error) {
	return nil, nil
}
func (nopKagent) Send(context.Context, string, string, string, hitl.Message) error { return nil }
func (nopKagent) Watch(context.Context, string, string, string, string) error      { return nil }

type nopNotify struct{}

func (nopNotify) Notify(context.Context, []string, notify.Notice) {}

func connect(t *testing.T, tools *Tools, caller identity.Caller) *mcp.ClientSession {
	t.Helper()
	tools.callerFrom = func(context.Context) (identity.Caller, bool) { return caller, true }
	server := mcp.NewServer(&mcp.Implementation{Name: "kagent"}, nil)
	if err := tools.Register(server); err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "agent"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out)
	}
	if res.IsError {
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				out["error"] = tc.Text
			}
		}
	}
	return out, res.IsError
}

func setup(t *testing.T) (*Tools, *store.Store, *approval.Service) {
	s := testdb.New(t)
	ctx := context.Background()
	_ = s.UpsertPerson(ctx, store.Person{ID: "alice"})
	_ = s.UpsertPerson(ctx, store.Person{ID: "bob", Groups: []string{"approvers"}})
	_ = s.WatchSession(ctx, store.WatchedSession{SessionID: "ctx", Agent: "workbench/builder", RequesterID: "alice"})
	v, _ := components.NewValidator()
	rec := &audit.Memory{}
	reg := &registry.Registry{Store: s, Deployer: registry.LogDeployer{}, Audit: rec}
	svc := &approval.Service{Store: s, Policy: policy.Default("approvers"), Kagent: nopKagent{}, Audit: rec, Notify: nopNotify{}, Promoter: reg}
	return &Tools{Store: s, Registry: reg, Approvals: svc, Components: v, Audit: rec}, s, svc
}

var runtime = identity.Caller{ID: "runtime:ctx", RuntimeSessionID: "ctx", CreatorID: "alice"}

func TestBuildToProduction(t *testing.T) {
	tools, s, svc := setup(t)
	ctx := context.Background()
	cs := connect(t, tools, runtime)

	out, isErr := call(t, cs, ToolRegisterApp, map[string]any{"name": "expense-tracker", "description": "Track team expenses", "scaffold": "python-fastapi-cloudrun"})
	if isErr {
		t.Fatal(out)
	}
	appID := out["appId"].(string)
	app, _ := s.GetApp(ctx, appID)
	if app.OwnerID != "alice" || app.SessionID != "ctx" || app.Agent != "workbench/builder" {
		t.Fatalf("owner must be the requester, not the runtime: %+v", app)
	}

	if out, isErr := call(t, cs, ToolRecordDeployment, map[string]any{"appId": appID, "environment": "production", "url": "https://x", "revision": "r1", "image": "i@sha256:1"}); !isErr {
		t.Fatalf("agents must not record production: %v", out)
	}
	if out, isErr := call(t, cs, ToolRecordDeployment, map[string]any{"appId": appID, "environment": "preview", "url": "https://preview", "revision": "r1", "image": "img@sha256:1"}); isErr {
		t.Fatal(out)
	}

	out, isErr = call(t, cs, ToolRequestPromotion, map[string]any{"appId": appID, "note": "ready"})
	if isErr || out["status"] != "pending" {
		t.Fatalf("ungated promotion must open an approval: %v", out)
	}
	approvalID := out["approvalId"].(string)
	again, _ := call(t, cs, ToolRequestPromotion, map[string]any{"appId": appID})
	if again["approvalId"] != approvalID {
		t.Fatal("asking again must not open a second approval")
	}

	if _, err := svc.Decide(ctx, identity.Caller{ID: "alice", Groups: []string{"approvers"}}, approval.Decision{ApprovalID: approvalID, Approved: true}); err == nil {
		t.Fatal("requester must not approve")
	}
	if _, err := svc.Decide(ctx, identity.Caller{ID: "bob", Groups: []string{"approvers"}}, approval.Decision{ApprovalID: approvalID, Approved: true}); err != nil {
		t.Fatal(err)
	}
	out, _ = call(t, cs, ToolRequestPromotion, map[string]any{"appId": appID})
	if out["status"] != "promoted" || out["productionUrl"] == "" {
		t.Fatalf("after approval the tool must report the promotion: %v", out)
	}
	app, _ = s.GetApp(ctx, appID)
	if app.Status != store.AppLive {
		t.Fatal("app must be live")
	}
}

func TestShowValidatesAndStores(t *testing.T) {
	tools, s, _ := setup(t)
	cs := connect(t, tools, runtime)
	msgs := []any{
		map[string]any{"createSurface": map[string]any{"surfaceId": "build", "catalogId": tools.Components.CatalogID}},
		map[string]any{"updateComponents": map[string]any{"surfaceId": "build", "components": []any{
			map[string]any{"id": "root", "component": "Progress", "steps": []any{map[string]any{"label": "Scaffold", "state": "done"}}}}}},
	}
	out, isErr := call(t, cs, ToolShow, map[string]any{"messages": msgs})
	if isErr {
		t.Fatal(out)
	}
	a2ui := out["a2ui"].(map[string]any)
	if a2ui["surfaceId"] != "build" || len(a2ui["messages"].([]any)) != 2 {
		t.Fatalf("tool result must carry the surface: %v", out)
	}
	stored, err := s.ListSurfaces(context.Background(), "ctx")
	if err != nil || len(stored) != 1 {
		t.Fatal("surface must be stored for clients that poll")
	}
	bad := []any{map[string]any{"updateComponents": map[string]any{"surfaceId": "x", "components": []any{map[string]any{"id": "root", "component": "Script", "src": "x"}}}}}
	if out, isErr := call(t, cs, ToolShow, map[string]any{"messages": bad}); !isErr {
		t.Fatalf("components outside the catalogue must be refused: %v", out)
	}
	card := []any{map[string]any{"updateComponents": map[string]any{"surfaceId": "x", "components": []any{map[string]any{"id": "root", "component": "ApprovalCard", "approvalId": "apr_someone_else"}}}}}
	if _, isErr := call(t, cs, ToolShow, map[string]any{"messages": card}); !isErr {
		t.Fatal("an ApprovalCard for another conversation's approval must be refused")
	}
}

func TestOnlyRuntimesAndOwners(t *testing.T) {
	tools, s, _ := setup(t)
	person := connect(t, tools, identity.Caller{ID: "alice"})
	if _, isErr := call(t, person, ToolRegisterApp, map[string]any{"name": "abc", "description": "d", "scaffold": "s"}); !isErr {
		t.Fatal("a person calling /mcp directly must not use builder tools")
	}
	other, _ := s.CreateApp(context.Background(), store.App{Name: "carols-app", OwnerID: "carol"})
	cs := connect(t, tools, runtime)
	if _, isErr := call(t, cs, ToolGetApp, map[string]any{"appId": other.ID}); !isErr {
		t.Fatal("an agent must not read another requester's app")
	}
}

func TestGetRequester(t *testing.T) {
	tools, s, _ := setup(t)
	cs := connect(t, tools, runtime)
	if _, isErr := call(t, cs, ToolGetRequester, map[string]any{}); !isErr {
		t.Fatal("a requester with no known email must be reported, not invented")
	}
	_ = s.UpsertPerson(context.Background(), store.Person{ID: "alice", Email: "alice@example.com", DisplayName: "Alice"})
	out, isErr := call(t, cs, ToolGetRequester, map[string]any{})
	if isErr || out["email"] != "alice@example.com" || out["name"] != "Alice" {
		t.Fatalf("%v", out)
	}
}
