package authz

import (
	"context"
	"testing"

	"github.com/kagent-dev/kagent/go/api/authorization"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func principal(groups ...string) auth.Principal {
	g := make([]any, len(groups))
	for i, x := range groups {
		g[i] = x
	}
	return auth.Principal{ID: "p", Claims: map[string]any{"groups": g}}
}

func newAuthz(t *testing.T) *Authorizer {
	a, err := New(Config{PlatformGroups: []string{"platform"}, RequesterGroups: []string{"requesters"},
		AuditorGroups: []string{"audit"}, AgentNamespaces: []string{"workbench"}}, []string{"approvers"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRequester(t *testing.T) {
	a := newAuthz(t)
	ctx := context.Background()
	p := principal("requesters")
	allow := []struct {
		v auth.Verb
		r auth.Resource
	}{
		{auth.VerbList, auth.Resource{Type: "Agent", Namespace: "workbench"}},
		{auth.VerbCreate, auth.Resource{Type: "Session", Namespace: "workbench"}},
		{auth.VerbCreate, auth.Resource{Type: "Checkpoint"}},
		{auth.VerbCreate, auth.Resource{Type: "ScheduledRun", Namespace: "workbench"}},
	}
	for _, c := range allow {
		if err := a.Check(ctx, p, c.v, c.r); err != nil {
			t.Errorf("%s %+v: %v", c.v, c.r, err)
		}
	}
	deny := []struct {
		v auth.Verb
		r auth.Resource
	}{
		{auth.VerbCreate, auth.Resource{Type: "Agent", Namespace: "workbench"}},
		{auth.VerbGet, auth.Resource{Type: "Agent", Namespace: "kagent-system"}},
		{auth.VerbCreate, auth.Resource{Type: "Session", Namespace: "platform-agents"}},
		{auth.VerbCreate, auth.Resource{Type: "ToolServer"}},
		{auth.VerbUpdate, auth.Resource{Type: "ModelConfig"}},
		{auth.VerbList, auth.Resource{Type: "SessionAllCreators"}},
		{auth.VerbList, auth.Resource{Type: "SystemNamespaces"}},
		{auth.VerbCreate, auth.Resource{Type: "Sandbox"}},
	}
	for _, c := range deny {
		if err := a.Check(ctx, p, c.v, c.r); err == nil {
			t.Errorf("%s %+v allowed", c.v, c.r)
		}
	}
	if err := a.Check(ctx, principal("marketing"), auth.VerbList, auth.Resource{Type: "Agent", Namespace: "workbench"}); err == nil {
		t.Error("people outside the requester groups must be refused")
	}
	if err := a.Check(ctx, auth.Principal{}, auth.VerbList, auth.Resource{Type: "Agent"}); err == nil {
		t.Error("anonymous principal allowed")
	}
}

func TestPlatformAndAuditor(t *testing.T) {
	a := newAuthz(t)
	ctx := context.Background()
	if err := a.Check(ctx, principal("platform"), auth.VerbCreate, auth.Resource{Type: "ToolServer"}); err != nil {
		t.Error(err)
	}
	if err := a.Check(ctx, principal("audit"), auth.VerbList, auth.Resource{Type: "SessionAllCreators"}); err != nil {
		t.Error(err)
	}
	if err := a.Check(ctx, principal("audit"), auth.VerbCreate, auth.Resource{Type: "Session", Namespace: "workbench"}); err == nil {
		t.Error("auditors are not requesters by default")
	}
}

func TestScope(t *testing.T) {
	a := newAuthz(t)
	ctx := context.Background()
	s, _ := a.Scope(ctx, principal("requesters"), auth.VerbList, "Agent")
	if s.Kind != authorization.ScopeAnyOf || s.AnyOf[0].All[0].Values[0] != "workbench" {
		t.Fatalf("requester agent scope %+v", s)
	}
	s, _ = a.Scope(ctx, principal("platform"), auth.VerbList, "Agent")
	if s.Kind != authorization.ScopeAll {
		t.Fatal("platform sees all")
	}
	s, _ = a.Scope(ctx, principal("requesters"), auth.VerbList, "Harness")
	if s.Kind != authorization.ScopeNone {
		t.Fatal("requesters see no harnesses")
	}
}

func TestRoles(t *testing.T) {
	a := newAuthz(t)
	r := a.Roles([]string{"requesters", "approvers"})
	if !a.Has([]string{"requesters", "approvers"}, Approver) || len(r) != 2 {
		t.Fatalf("roles %v", r)
	}
	open, _ := New(Config{PlatformGroups: []string{"p"}, AgentNamespaces: []string{"w"}}, nil)
	if !open.Has(nil, Requester) {
		t.Fatal("empty requester groups admit everyone signed in")
	}
}
