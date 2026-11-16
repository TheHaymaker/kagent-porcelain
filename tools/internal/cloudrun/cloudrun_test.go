package cloudrun

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx/mcpxtest"
)

const (
	previewProject = "wb-preview-proj"
	prodProject    = "wb-prod-proj"
	region         = "europe-west1"
	imagePrefix    = "europe-west1-docker.pkg.dev/wb-builds/builds/"
	parent         = "projects/" + previewProject + "/locations/" + region
)

var goodImage = imagePrefix + "my-app@sha256:" + strings.Repeat("a", 64)

type req struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
}

type fakeGCP struct {
	mu       sync.Mutex
	reqs     []req
	services map[string]map[string]any // name -> service
	policy   map[string]any
	opPolls  int
	opError  string
	logs     []map[string]any
}

func (f *fakeGCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rq := req{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &rq.Body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, rq)
	enc := json.NewEncoder(w)
	p := r.URL.Path
	switch {
	case p == "/v2/entries:list":
		_ = enc.Encode(map[string]any{"entries": f.logs, "nextPageToken": "more"})
	case strings.HasPrefix(p, "/v2/"+parent+"/operations/"):
		f.opPolls++
		op := map[string]any{"name": strings.TrimPrefix(p, "/v2/"), "done": true}
		if f.opError != "" {
			op["error"] = map[string]any{"code": 9, "message": f.opError}
		}
		_ = enc.Encode(op)
	case strings.HasSuffix(p, ":getIamPolicy"):
		_ = enc.Encode(f.policy)
	case strings.HasSuffix(p, ":setIamPolicy"):
		f.policy = map[string]any{}
		for k, v := range rq.Body["policy"].(map[string]any) {
			f.policy[k] = v
		}
		f.policy["etag"] = "new"
		_ = enc.Encode(f.policy)
	case strings.HasSuffix(p, "/revisions"):
		if r.URL.Query().Get("pageToken") == "" {
			_ = enc.Encode(map[string]any{"revisions": []map[string]any{{
				"name": parent + "/services/wb-preview-my-app/revisions/wb-preview-my-app-00002-abc", "createTime": "2026-11-14T10:00:00Z",
				"labels": map[string]any{"wb-requester": "alice_example_com"}, "containers": []map[string]any{{"image": goodImage}},
				"conditions": []map[string]any{{"type": "Ready", "state": "CONDITION_FAILED", "message": "container failed to start"}},
			}}, "nextPageToken": "tok2"})
		} else {
			_ = enc.Encode(map[string]any{"revisions": []map[string]any{}})
		}
	case p == "/v2/"+parent+"/services" && r.Method == http.MethodPost:
		name := r.URL.Query().Get("serviceId")
		svc := rq.Body
		svc["name"] = parent + "/services/" + name
		f.services[name] = svc
		_ = enc.Encode(map[string]any{"name": parent + "/operations/op-create", "done": false})
	case strings.HasPrefix(p, "/v2/"+parent+"/services/"):
		name := strings.TrimPrefix(p, "/v2/"+parent+"/services/")
		svc, ok := f.services[name]
		if !ok {
			w.WriteHeader(404)
			_ = enc.Encode(map[string]any{"error": map[string]any{"code": 404, "status": "NOT_FOUND", "message": "Resource '" + name + "' was not found"}})
			return
		}
		switch r.Method {
		case http.MethodGet:
			out := map[string]any{}
			for k, v := range svc {
				out[k] = v
			}
			out["uri"] = "https://" + name + "-xyz.a.run.app"
			out["latestReadyRevision"] = parent + "/services/" + name + "/revisions/" + name + "-00001-aaa"
			out["terminalCondition"] = map[string]any{"type": "Ready", "state": "CONDITION_SUCCEEDED"}
			out["trafficStatuses"] = []map[string]any{{"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST", "percent": 100}}
			_ = enc.Encode(out)
		case http.MethodPatch:
			rq.Body["name"] = svc["name"]
			f.services[name] = rq.Body
			_ = enc.Encode(map[string]any{"name": parent + "/operations/op-update", "done": false})
		case http.MethodDelete:
			delete(f.services, name)
			_ = enc.Encode(map[string]any{"name": parent + "/operations/op-delete", "done": false})
		}
	default:
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"no route"}}`))
	}
}

func (f *fakeGCP) requests() []req {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]req(nil), f.reqs...)
}

func (f *fakeGCP) find(method, suffix string) (req, bool) {
	for _, r := range f.requests() {
		if r.Method == method && strings.HasSuffix(r.Path, suffix) {
			return r, true
		}
	}
	return req{}, false
}

func managedService(name string) map[string]any {
	return map[string]any{
		"name":     parent + "/services/" + name,
		"labels":   map[string]any{"wb-managed": "true", "wb-requester": "bob"},
		"template": map[string]any{"containers": []map[string]any{{"image": goodImage}}},
	}
}

func testConfig(gcpURL string) Config {
	return Config{
		PreviewProject: previewProject, Region: region, ProductionProjects: []string{prodProject},
		ImagePrefix: imagePrefix, MaxInstances: 3, InvokerDomains: []string{"example.com"},
		RunBaseURL: gcpURL, LoggingBaseURL: gcpURL, PollInterval: time.Millisecond, DeployWait: 2 * time.Second,
	}
}

func setup(t *testing.T, mut ...func(*Config)) (*fakeGCP, *mcp.ClientSession) {
	t.Helper()
	f := &fakeGCP{services: map[string]map[string]any{"wb-preview-my-app": managedService("wb-preview-my-app")}, policy: map[string]any{"etag": "e1"}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := testConfig(srv.URL)
	for _, m := range mut {
		m(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s := mcpx.NewServer(mcpx.Options{Name: "cloudrun-mcp", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	Register(s, NewClient(cfg, srv.Client()))
	t.Cleanup(func() { assertPreviewOnly(t, f) })
	return f, mcpxtest.Connect(t, s, "alice@example.com")
}

// assertPreviewOnly checks that no request ever addressed anything but the
// preview project and region.
func assertPreviewOnly(t *testing.T, f *fakeGCP) {
	for _, r := range f.requests() {
		if r.Path == "/v2/entries:list" {
			rn, _ := json.Marshal(r.Body["resourceNames"])
			if string(rn) != `["projects/`+previewProject+`"]` {
				t.Errorf("logging query against %s", rn)
			}
			continue
		}
		iapParent := "/v1/projects/" + previewProject + "/iap_web/cloud_run-" + region + "/services/"
		if !strings.HasPrefix(r.Path, "/v2/"+parent+"/") && !strings.HasPrefix(r.Path, iapParent) {
			t.Errorf("request outside preview project: %s %s", r.Method, r.Path)
		}
		if strings.Contains(r.Path+r.Query, prodProject) {
			t.Errorf("request mentions production project: %s", r.Path)
		}
	}
}

func TestToolsAndAnnotations(t *testing.T) {
	_, cs := setup(t)
	tools := mcpxtest.ToolMap(t, cs)
	want := map[string]mcpxtest.Hints{
		"cloudrun_deploy_preview":         {ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false},
		"cloudrun_get_service":            {ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
		"cloudrun_list_revisions":         {ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
		"cloudrun_read_logs":              {ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
		"cloudrun_delete_preview_service": {ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false},
		"cloudrun_set_iam_invoker":        {ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false},
		"cloudrun_share_preview":          {ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false},
	}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tools), len(want))
	}
	for name, h := range want {
		tl, ok := tools[name]
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if got := mcpxtest.HintsOf(tl); got != h {
			t.Errorf("%s hints %+v, want %+v", name, got, h)
		}
		if strings.Contains(strings.ToLower(name), "prod") {
			t.Errorf("no production tool may exist: %s", name)
		}
		b, _ := json.Marshal(tl.InputSchema)
		if strings.Contains(string(b), `"project"`) || strings.Contains(string(b), `"region"`) {
			t.Errorf("%s must not accept project/region arguments: %s", name, b)
		}
	}
}

func TestServiceName(t *testing.T) {
	good := map[string]string{"my-app": "wb-preview-my-app", "wb-preview-my-app": "wb-preview-my-app", "a": "wb-preview-a"}
	for in, want := range good {
		got, err := ServiceName(in)
		if err != nil || got != want {
			t.Errorf("ServiceName(%q) = %q, %v", in, got, err)
		}
	}
	bad := []string{"", "My-App", "-x", "x-", "a/b", "../prod", "projects/wb-prod-proj/locations/europe-west1/services/x",
		"x?y", "x%2Fy", "1abc", strings.Repeat("a", 39), "wb-preview-", "a_b"}
	for _, in := range bad {
		if got, err := ServiceName(in); err == nil {
			t.Errorf("ServiceName(%q) = %q, want error", in, got)
		}
	}
}

func TestConfigRefusesProductionProject(t *testing.T) {
	cfg := testConfig("http://x")
	cfg.PreviewProject = prodProject
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "production") {
		t.Fatalf("expected refusal, got %v", err)
	}
	cfg = testConfig("http://x")
	cfg.ImagePrefix = ""
	if cfg.Validate() == nil {
		t.Fatal("image prefix required")
	}
}

func TestDeployUpdatesExistingService(t *testing.T) {
	f, cs := setup(t)
	var out DeployOut
	mcpxtest.MustOK(t, cs, "cloudrun_deploy_preview", map[string]any{
		"service": "my-app", "image": goodImage, "env": map[string]any{"B": "2", "A": "1"}, "max_instances": 100,
	}, &out)
	if out.Created || !out.Done || out.Service != "wb-preview-my-app" || out.Project != previewProject || out.Status == nil || !out.Status.Ready || out.Status.URL == "" {
		t.Fatalf("unexpected %+v", out)
	}
	patch, ok := f.find(http.MethodPatch, "/services/wb-preview-my-app")
	if !ok {
		t.Fatal("no PATCH")
	}
	b, _ := json.Marshal(patch.Body)
	for _, want := range []string{`"wb-requester":"alice_example_com"`, `"wb-managed":"true"`, `"maxInstanceCount":3`, `"minInstanceCount":0`,
		`"image":"` + goodImage + `"`, `"env":[{"name":"A","value":"1"},{"name":"B","value":"2"}]`, `"memory":"512Mi"`, `"containerPort":8080`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("deploy body lacks %s: %s", want, b)
		}
	}
	if f.opPolls == 0 {
		t.Error("deploy did not wait for the operation")
	}
}

func TestDeployCreateRequiresAllowCreate(t *testing.T) {
	f, cs := setup(t)
	mcpxtest.MustErr(t, cs, "cloudrun_deploy_preview", map[string]any{"service": "new-app", "image": goodImage}, "allow_create=true")
	if _, ok := f.find(http.MethodPost, "/services"); ok {
		t.Fatal("service created without allow_create")
	}
	var out DeployOut
	mcpxtest.MustOK(t, cs, "cloudrun_deploy_preview", map[string]any{"service": "new-app", "image": goodImage, "allow_create": true, "wait": false}, &out)
	if !out.Created || out.Done || out.Operation != parent+"/operations/op-create" {
		t.Fatalf("unexpected %+v", out)
	}
	post, _ := f.find(http.MethodPost, "/services")
	if post.Query != "serviceId=wb-preview-new-app" {
		t.Fatalf("query %s", post.Query)
	}
}

func TestDeployRejectsInjectedProject(t *testing.T) {
	_, cs := setup(t)
	// Unknown properties are rejected by the input schema; either way the
	// cleanup check asserts nothing reached a non-preview project.
	res := mcpxtest.Call(t, cs, "cloudrun_deploy_preview", map[string]any{"service": "my-app", "image": goodImage, "project": prodProject, "region": "us-central1"})
	if !res.IsError {
		t.Fatalf("expected unknown project/region arguments to be rejected, got %s", mcpxtest.Text(res))
	}
}

func TestDeployValidation(t *testing.T) {
	f, cs := setup(t)
	cases := []map[string]any{
		{"service": "my-app", "image": "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64)},
		{"service": "my-app", "image": imagePrefix + "my-app:latest"},
		{"service": "my-app", "image": goodImage, "env": map[string]any{"PORT": "1"}},
		{"service": "my-app", "image": goodImage, "env": map[string]any{"GOOGLE_APPLICATION_CREDENTIALS": "/x"}},
		{"service": "my-app", "image": goodImage, "env": map[string]any{"bad-name": "1"}},
		{"service": "my-app", "image": goodImage, "memory": "64Gi"},
		{"service": "my-app", "image": goodImage, "cpu": "8"},
		{"service": "my-app", "image": goodImage, "port": 70000},
		{"service": "../../prod", "image": goodImage},
	}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			mcpxtest.MustErr(t, cs, "cloudrun_deploy_preview", c, "invalid argument")
		})
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("validation failures reached the API (%d requests)", n)
	}
}

func TestDeployRefusesUnmanagedService(t *testing.T) {
	f, cs := setup(t)
	f.services["wb-preview-hand-made"] = map[string]any{"name": parent + "/services/wb-preview-hand-made"}
	mcpxtest.MustErr(t, cs, "cloudrun_deploy_preview", map[string]any{"service": "hand-made", "image": goodImage}, "not managed by Workbench")
	mcpxtest.MustErr(t, cs, "cloudrun_delete_preview_service", map[string]any{"service": "hand-made"}, "not managed by Workbench")
}

func TestDeployOperationError(t *testing.T) {
	f, cs := setup(t)
	f.opError = "Revision 'x' is not ready: container failed to listen on PORT=8080"
	mcpxtest.MustErr(t, cs, "cloudrun_deploy_preview", map[string]any{"service": "my-app", "image": goodImage}, "failed to listen")
}

func TestGetServiceAndRevisions(t *testing.T) {
	_, cs := setup(t)
	var svc Service
	mcpxtest.MustOK(t, cs, "cloudrun_get_service", map[string]any{"service": "wb-preview-my-app"}, &svc)
	if svc.Name != "wb-preview-my-app" || !svc.Ready || svc.LatestReadyRevision != "wb-preview-my-app-00001-aaa" || svc.Image != goodImage || svc.Requester != "bob" || len(svc.Traffic) != 1 {
		t.Fatalf("unexpected %+v", svc)
	}
	mcpxtest.MustErr(t, cs, "cloudrun_get_service", map[string]any{"service": "nope"}, "not found")

	var revs ListRevisionsOut
	mcpxtest.MustOK(t, cs, "cloudrun_list_revisions", map[string]any{"service": "my-app", "page_size": 500}, &revs)
	if len(revs.Revisions) != 1 || revs.NextPageToken != "tok2" || revs.Revisions[0].Ready || revs.Revisions[0].Message == "" || revs.Revisions[0].Name != "wb-preview-my-app-00002-abc" {
		t.Fatalf("unexpected %+v", revs)
	}
	revs = ListRevisionsOut{}
	mcpxtest.MustOK(t, cs, "cloudrun_list_revisions", map[string]any{"service": "my-app", "page_token": "tok2"}, &revs)
	if len(revs.Revisions) != 0 || revs.NextPageToken != "" {
		t.Fatalf("unexpected page 2 %+v", revs)
	}
	mcpxtest.MustErr(t, cs, "cloudrun_list_revisions", map[string]any{"service": "my-app", "page_token": "a b&c"}, "page_token")
}

func TestReadLogs(t *testing.T) {
	f, cs := setup(t)
	now = func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	f.logs = []map[string]any{
		{"timestamp": "t1", "severity": "ERROR", "textPayload": strings.Repeat("x", 5000), "resource": map[string]any{"labels": map[string]any{"revision_name": "wb-preview-my-app-00001-aaa"}}},
		{"timestamp": "t2", "severity": "INFO", "jsonPayload": map[string]any{"message": "hello"}},
		{"timestamp": "t3", "httpRequest": map[string]any{"requestMethod": "GET", "requestUrl": "/", "status": 500, "latency": "0.1s"}},
	}
	var out ReadLogsOut
	mcpxtest.MustOK(t, cs, "cloudrun_read_logs", map[string]any{
		"service": "my-app", "since_minutes": 30, "min_severity": "warning", "contains": `x" OR resource.labels.service_name="other`, "limit": 3,
	}, &out)
	if len(out.Entries) != 3 || !out.Truncated || len(out.Entries[0].Message) > MaxLogEntryBytes+20 || out.Entries[1].Message != "hello" || !strings.Contains(out.Entries[2].Message, "-> 500") {
		t.Fatalf("unexpected %+v", out)
	}
	r, _ := f.find(http.MethodPost, "/v2/entries:list")
	filter := r.Body["filter"].(string)
	for _, want := range []string{`resource.type="cloud_run_revision"`, `resource.labels.service_name="wb-preview-my-app"`, `resource.labels.location="europe-west1"`,
		`timestamp>="2026-11-14T11:30:00Z"`, `severity>=WARNING`, `SEARCH("x\" OR resource.labels.service_name=\"other")`} {
		if !strings.Contains(filter, want) {
			t.Errorf("filter lacks %s: %s", want, filter)
		}
	}
	if r.Body["pageSize"] != float64(3) {
		t.Errorf("pageSize %v", r.Body["pageSize"])
	}
	mcpxtest.MustErr(t, cs, "cloudrun_read_logs", map[string]any{"service": "my-app", "min_severity": "LOUD"}, "min_severity")
	mcpxtest.MustErr(t, cs, "cloudrun_read_logs", map[string]any{"service": "my-app", "revision": "wb-preview-other-00001"}, "revision")
}

func TestReadLogsTotalBound(t *testing.T) {
	f, cs := setup(t)
	for i := 0; i < MaxLogEntries; i++ {
		f.logs = append(f.logs, map[string]any{"timestamp": "t", "textPayload": strings.Repeat("y", MaxLogEntryBytes)})
	}
	var out ReadLogsOut
	mcpxtest.MustOK(t, cs, "cloudrun_read_logs", map[string]any{"service": "my-app", "limit": 1000}, &out)
	total := 0
	for _, e := range out.Entries {
		total += len(e.Message)
	}
	if total > MaxLogTotalBytes || !out.Truncated {
		t.Fatalf("total %d truncated=%v", total, out.Truncated)
	}
}

func TestDeletePreviewService(t *testing.T) {
	f, cs := setup(t)
	var out DeleteOut
	mcpxtest.MustOK(t, cs, "cloudrun_delete_preview_service", map[string]any{"service": "my-app"}, &out)
	if !out.Deleted || out.Service != "wb-preview-my-app" {
		t.Fatalf("unexpected %+v", out)
	}
	if _, ok := f.services["wb-preview-my-app"]; ok {
		t.Fatal("not deleted")
	}
	mcpxtest.MustErr(t, cs, "cloudrun_delete_preview_service", map[string]any{"service": "my-app"}, "not found")
}

func TestSetIAMInvoker(t *testing.T) {
	f, cs := setup(t)
	var out SetInvokerOut
	mcpxtest.MustOK(t, cs, "cloudrun_set_iam_invoker", map[string]any{"service": "my-app", "member": "user:carol@example.com"}, &out)
	if !out.Changed || len(out.Members) != 1 || out.Members[0] != "user:carol@example.com" {
		t.Fatalf("unexpected %+v", out)
	}
	set, _ := f.find(http.MethodPost, ":setIamPolicy")
	if pol := set.Body["policy"].(map[string]any); pol["etag"] != "e1" {
		t.Fatalf("etag not round-tripped: %+v", pol)
	}
	get, _ := f.find(http.MethodGet, ":getIamPolicy")
	if get.Query != "options.requestedPolicyVersion=3" {
		t.Fatalf("query %s", get.Query)
	}
	n := len(f.requests())
	mcpxtest.MustOK(t, cs, "cloudrun_set_iam_invoker", map[string]any{"service": "my-app", "member": "user:carol@example.com"}, &out)
	if out.Changed {
		t.Fatal("second add should be a no-op")
	}
	if len(f.requests()) != n+1 { // only getIamPolicy
		t.Fatal("no-op add must not call setIamPolicy")
	}
	mcpxtest.MustOK(t, cs, "cloudrun_set_iam_invoker", map[string]any{"service": "my-app", "member": "serviceAccount:ci@" + previewProject + ".iam.gserviceaccount.com"}, &out)
	mcpxtest.MustOK(t, cs, "cloudrun_set_iam_invoker", map[string]any{"service": "my-app", "member": "user:carol@example.com", "action": "remove"}, &out)
	if !out.Changed || len(out.Members) != 1 {
		t.Fatalf("unexpected %+v", out)
	}
	for _, m := range []string{"allUsers", "allAuthenticatedUsers", "user:mallory@evil.com", "carol@example.com", "user:carol@example.com\nx", "deleted:user:x@example.com"} {
		mcpxtest.MustErr(t, cs, "cloudrun_set_iam_invoker", map[string]any{"service": "my-app", "member": m}, "invalid argument")
	}
	mcpxtest.MustErr(t, cs, "cloudrun_set_iam_invoker", map[string]any{"service": "my-app", "member": "user:carol@example.com", "action": "own"}, "action")
}

func TestPublicInvokerWhenAllowed(t *testing.T) {
	_, cs := setup(t, func(c *Config) { c.AllowPublicInvoker = true })
	var out SetInvokerOut
	mcpxtest.MustOK(t, cs, "cloudrun_set_iam_invoker", map[string]any{"service": "my-app", "member": "allUsers"}, &out)
	if !out.Changed {
		t.Fatal("expected change")
	}
}

func iapOn(c *Config) { c.IAP = true; c.IAPBaseURL = c.RunBaseURL }

func TestDeployWithIAPGivesViewersAccess(t *testing.T) {
	f, cs := setup(t, iapOn)
	var out DeployOut
	mcpxtest.MustOK(t, cs, "cloudrun_deploy_preview", map[string]any{
		"service": "my-app", "image": imagePrefix + "my-app@sha256:" + strings.Repeat("a", 64),
		"viewers": []string{"user:alice@example.com"},
	}, &out)
	if len(out.Viewers) != 1 {
		t.Fatalf("viewers not granted: %+v", out)
	}
	patch, _ := f.find(http.MethodPatch, "/v2/projects/"+previewProject+"/locations/"+region+"/services/wb-preview-my-app")
	if patch.Body["iapEnabled"] != true {
		t.Fatalf("IAP not enabled on deploy: %+v", patch.Body)
	}
	set, _ := f.find(http.MethodPost, "/v1/projects/"+previewProject+"/iap_web/cloud_run-"+region+"/services/wb-preview-my-app:setIamPolicy")
	b, _ := json.Marshal(set.Body)
	if !strings.Contains(string(b), "roles/iap.httpsResourceAccessor") || !strings.Contains(string(b), "user:alice@example.com") {
		t.Fatalf("IAP policy %s", b)
	}
}

func TestSharePreview(t *testing.T) {
	_, cs := setup(t, iapOn)
	var out ShareOut
	mcpxtest.MustOK(t, cs, "cloudrun_share_preview", map[string]any{"service": "my-app", "member": "group:team@example.com"}, &out)
	if !out.Changed || len(out.Members) != 1 {
		t.Fatalf("%+v", out)
	}
	for _, m := range []string{"allUsers", "allAuthenticatedUsers", "serviceAccount:x@" + previewProject + ".iam.gserviceaccount.com", "user:mallory@evil.test"} {
		mcpxtest.MustErr(t, cs, "cloudrun_share_preview", map[string]any{"service": "my-app", "member": m}, "member")
	}
}

func TestViewersNeedIAP(t *testing.T) {
	_, cs := setup(t)
	mcpxtest.MustErr(t, cs, "cloudrun_deploy_preview", map[string]any{
		"service": "my-app", "image": imagePrefix + "my-app@sha256:" + strings.Repeat("a", 64), "viewers": []string{"user:alice@example.com"},
	}, "IAP")
	mcpxtest.MustErr(t, cs, "cloudrun_share_preview", map[string]any{"service": "my-app", "member": "user:alice@example.com"}, "IAP")
}
