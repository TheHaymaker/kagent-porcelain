package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/testdb"
)

type countingDeployer struct{ calls int }

func (c *countingDeployer) DeployProduction(_ context.Context, app store.App, image, _, _ string) (string, error) {
	c.calls++
	return "https://" + app.Name + ".prod", nil
}

func approvedPromotion(t *testing.T, s *store.Store, appID, decider string) store.Approval {
	t.Helper()
	ctx := context.Background()
	a, _, err := s.CreateApproval(ctx, store.Approval{Fingerprint: store.NewID("fp"), SessionID: "s", TaskID: "t", Agent: "a",
		RequesterID: "alice", Kind: policy.ProductionDeploy, ApproverGroups: []string{"approvers"}, AppID: appID,
		Calls: []store.Call{{Tool: hitl.Tool{ID: "1", Name: "workbench_request_promotion", Args: map[string]any{"appId": appID, "revision": "r1"}}, Kind: policy.ProductionDeploy}}})
	if err != nil {
		t.Fatal(err)
	}
	a, err = s.Decide(ctx, a.ID, decider, true, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPromote(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	dep := &countingDeployer{}
	rec := &audit.Memory{}
	r := &Registry{Store: s, Deployer: dep, Audit: rec}
	if _, err := r.Register(ctx, Registration{Name: "Bad Name!", OwnerID: "alice"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad names must be refused")
	}
	app, err := r.Register(ctx, Registration{Name: "expense-tracker", OwnerID: "alice", ActorID: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	a := approvedPromotion(t, s, app.ID, "bob")
	if _, err := r.Promote(ctx, a); !errors.Is(err, ErrInvalid) {
		t.Fatalf("promoting a revision never previewed must fail: %v", err)
	}
	if _, err := r.RecordPreview(ctx, app.ID, "https://p", "r1", "europe-docker.pkg.dev/p/r/app@sha256:abc", "runtime", "s"); err != nil {
		t.Fatal(err)
	}
	d, err := r.Promote(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if d.URL != "https://expense-tracker.prod" || d.ApprovalID != a.ID || d.Image == "" {
		t.Fatalf("%+v", d)
	}
	again, err := r.Promote(ctx, a)
	if err != nil || again.ID != d.ID || dep.calls != 1 {
		t.Fatalf("promotion must be idempotent per approval: %v %d", err, dep.calls)
	}
	got, _ := s.GetApp(ctx, app.ID)
	if got.Status != store.AppLive {
		t.Fatal("app must be live")
	}
	if rec.Types()[len(rec.Types())-1] != audit.AppPromoted {
		t.Fatalf("audit %v", rec.Types())
	}

	pending, _, _ := s.CreateApproval(ctx, store.Approval{Fingerprint: "x", RequesterID: "alice", Kind: policy.ProductionDeploy, AppID: app.ID, ApproverGroups: []string{"g"}})
	if _, err := r.Promote(ctx, pending); !errors.Is(err, ErrNotAllowed) {
		t.Fatal("a pending approval must not promote")
	}
	forged := a
	forged.DecidedByID = "alice"
	if _, err := r.Promote(ctx, forged); !errors.Is(err, ErrNotAllowed) {
		t.Fatal("a self-decided production approval must not promote")
	}
}

func TestCloudRunDeployer(t *testing.T) {
	var methods []string
	ready := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		switch r.Method {
		case http.MethodGet:
			if !ready && len(methods) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"uri": "https://wb-app-xyz.run.app", "reconciling": !ready})
			ready = true
		case http.MethodPost:
			var svc map[string]any
			_ = json.NewDecoder(r.Body).Decode(&svc)
			if !strings.Contains(r.URL.RawQuery, "serviceId=wb-app") || svc["labels"].(map[string]any)["wb-approval"] != "apr_1" {
				http.Error(w, "bad create", 400)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	c := &CloudRun{Config: CloudRunConfig{Project: "p", Region: "r"}, Client: srv.Client(), Endpoint: srv.URL, PollInterval: time.Millisecond}
	if _, err := c.DeployProduction(context.Background(), store.App{Name: "app"}, "img:latest", "r1", "apr_1"); err == nil {
		t.Fatal("tags must be refused; production pins digests")
	}
	url, err := c.DeployProduction(context.Background(), store.App{Name: "app"}, "img@sha256:abc", "r1", "apr_1")
	if err != nil || url != "https://wb-app-xyz.run.app" {
		t.Fatalf("%s %v %v", url, err, methods)
	}
	if methods[1] != "POST /v2/projects/p/locations/r/services" {
		t.Fatalf("missing service must be created: %v", methods)
	}
}
