package waker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fake struct {
	mu      sync.Mutex
	nodes   int
	resizes []int
	fail    bool
	pods    []map[string]any // on every node
	deleted []string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
		if r.URL.Query().Get("labelSelector") != "cloud.google.com/gke-nodepool=workload" || r.Header.Get("Authorization") != "Bearer kube" {
			w.WriteHeader(400)
			return
		}
		items := make([]map[string]any, f.nodes)
		for i := range items {
			items[i] = map[string]any{"metadata": map[string]any{"name": "node-" + string(rune('a'+i))}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/pods":
		if !strings.HasPrefix(r.URL.Query().Get("fieldSelector"), "spec.nodeName=node-") {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": f.pods})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/"):
		f.deleted = append(f.deleted, r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/p/locations/us-central1-a/clusters/c/nodePools/workload:setSize":
		if f.fail {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"operation in progress"}}`))
			return
		}
		var body struct{ NodeCount int }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.resizes = append(f.resizes, body.NodeCount)
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(404)
	}
}

func setup(t *testing.T, nodes int) (*fake, *Waker, *time.Time) {
	f := &fake{nodes: nodes}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := Config{Project: "p", Location: "us-central1-a", Cluster: "c", Pool: "workload", Nodes: 2, Idle: time.Hour,
		ContainerURL: srv.URL, KubeURL: srv.URL, KubeToken: func() (string, error) { return "kube", nil }}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	w := New(cfg, srv.Client(), srv.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	w.last = now
	return f, w, &now
}

func TestPausesAfterAnIdleHourOnly(t *testing.T) {
	f, w, now := setup(t, 2)
	ctx := context.Background()
	*now = now.Add(59 * time.Minute)
	w.Tick(ctx)
	if len(f.resizes) != 0 {
		t.Fatalf("paused early: %v", f.resizes)
	}
	w.Activity(ctx) // traffic resets the clock
	*now = now.Add(59 * time.Minute)
	w.Tick(ctx)
	if len(f.resizes) != 0 {
		t.Fatalf("paused despite traffic: %v", f.resizes)
	}
	*now = now.Add(2 * time.Minute)
	w.Tick(ctx)
	w.Tick(ctx) // debounced
	if len(f.resizes) != 1 || f.resizes[0] != 0 {
		t.Fatalf("resizes %v", f.resizes)
	}
	f.nodes = 0
	*now = now.Add(10 * time.Minute)
	w.Tick(ctx) // already paused
	if len(f.resizes) != 1 {
		t.Fatalf("resized a paused pool: %v", f.resizes)
	}
}

func TestTrafficWakesAPausedPoolOnce(t *testing.T) {
	f, w, now := setup(t, 0)
	ctx := context.Background()
	w.Activity(ctx)
	w.Activity(ctx)
	*now = now.Add(time.Minute)
	w.Activity(ctx)
	if len(f.resizes) != 1 || f.resizes[0] != 2 {
		t.Fatalf("resizes %v", f.resizes)
	}
	*now = now.Add(3 * time.Minute) // still booting after the debounce: ask again
	w.Activity(ctx)
	if len(f.resizes) != 2 {
		t.Fatalf("resizes %v", f.resizes)
	}
}

func TestFailedResizeIsRetried(t *testing.T) {
	f, w, _ := setup(t, 0)
	f.fail = true
	w.Activity(context.Background())
	f.fail = false
	w.Activity(context.Background())
	if len(f.resizes) != 1 || f.resizes[0] != 2 {
		t.Fatalf("resizes %v", f.resizes)
	}
}

func TestStartingAnswers503AndWakes(t *testing.T) {
	f, w, _ := setup(t, 0)
	rec := httptest.NewRecorder()
	w.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/starting", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("X-Workbench-Starting") != "1" || !strings.Contains(rec.Body.String(), "3 to 5 minutes") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if len(f.resizes) != 1 {
		t.Fatalf("resizes %v", f.resizes)
	}
	rec = httptest.NewRecorder()
	w.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/activity", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("activity %d", rec.Code)
	}
}

func TestConfigValidate(t *testing.T) {
	for _, c := range []Config{{}, {Project: "p", Location: "l", Cluster: "c", Pool: "x", Nodes: 0, Idle: time.Hour},
		{Project: "p", Location: "l", Cluster: "c", Pool: "x", Nodes: 2, Idle: time.Second}} {
		if err := c.Validate(); err == nil {
			t.Errorf("%+v validated", c)
		}
	}
}

func pod(ns, name, owner string, mirror bool) map[string]any {
	md := map[string]any{"name": name, "namespace": ns}
	if owner != "" {
		md["ownerReferences"] = []map[string]any{{"kind": owner}}
	}
	if mirror {
		md["annotations"] = map[string]string{"kubernetes.io/config.mirror": "x"}
	}
	return map[string]any{"metadata": md}
}

func TestForceDrainsPodsThatHoldUpThePause(t *testing.T) {
	f, w, now := setup(t, 1)
	f.pods = []map[string]any{
		pod("ate-system", "ate-api-server-1", "ReplicaSet", false),
		pod("kube-system", "fluentbit-1", "DaemonSet", false),
		pod("kube-system", "kube-proxy-x", "", true),
	}
	ctx := context.Background()
	*now = now.Add(61 * time.Minute)
	w.Tick(ctx) // asks for 0
	if len(f.resizes) != 1 || f.resizes[0] != 0 {
		t.Fatalf("resizes %v", f.resizes)
	}
	*now = now.Add(time.Minute)
	w.Tick(ctx) // node still there, but give GKE its drain time
	if len(f.deleted) != 0 {
		t.Fatalf("drained too early: %v", f.deleted)
	}
	*now = now.Add(2 * time.Minute)
	w.Tick(ctx)
	if len(f.deleted) != 1 || f.deleted[0] != "/api/v1/namespaces/ate-system/pods/ate-api-server-1" {
		t.Fatalf("deleted %v", f.deleted)
	}
	if len(f.resizes) != 1 {
		t.Fatalf("asked for 0 again while draining: %v", f.resizes)
	}
	// Traffic while draining: the pool is asked back up, and with traffic
	// the waker no longer drains.
	f.deleted = nil
	w.Activity(ctx)
	if len(f.resizes) != 2 || f.resizes[1] != 2 {
		t.Fatalf("resizes %v", f.resizes)
	}
	*now = now.Add(5 * time.Minute)
	w.Tick(ctx)
	if len(f.deleted) != 0 {
		t.Fatalf("drained after traffic: %v", f.deleted)
	}
}
