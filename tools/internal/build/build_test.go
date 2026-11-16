package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/gitlab"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx/mcpxtest"
)

const (
	sha     = "0123456789abcdef0123456789abcdef01234567"
	buildID = "11111111-2222-3333-4444-555555555555"
	prefix  = "us-central1-docker.pkg.dev/wb-builds/builds/"
)

var digest = "sha256:" + strings.Repeat("c", 64)

func tgz(t *testing.T, files ...string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte("x"))
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

type fakeSource struct{ trees map[string][]byte }

func (f fakeSource) Source(_ context.Context, project, ref string) (gitlab.Snapshot, error) {
	if tree, ok := f.trees[project]; ok {
		return gitlab.Snapshot{Project: "wb/apps/" + project, Name: project, SHA: sha, Tarball: tree}, nil
	}
	if project != "expense-tracker" {
		return gitlab.Snapshot{}, errors.New(`project "` + project + `" is outside the Workbench group`)
	}
	return gitlab.Snapshot{Project: "wb/apps/expense-tracker", Name: "Expense-Tracker", SHA: sha, Tarball: []byte("tgz")}, nil
}

type fakeGCP struct {
	mu       sync.Mutex
	uploaded map[string]string
	created  map[string]any
	status   string
	tags     []string
	steps    []map[string]any
	logs     []string // newest first, as entries:list returns them
	logQuery map[string]any
}

func (f *fakeGCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/wb-staging/o"):
		b, _ := io.ReadAll(r.Body)
		name := r.URL.Query().Get("name")
		if _, ok := f.uploaded[name]; ok {
			// As GCS answers a create-only write to an existing object.
			if r.URL.Query().Get("ifGenerationMatch") != "0" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		f.uploaded[name] = string(b)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/wb-builds/builds":
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"build": map[string]any{"id": buildID, "logUrl": "https://console/logs"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/wb-builds/builds/"+buildID:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": buildID, "status": f.status, "statusDetail": "step 0 failed", "tags": f.tags, "steps": f.steps,
			"results": map[string]any{"images": []map[string]any{{"name": prefix + "expense-tracker:0123456789ab", "digest": digest}}},
		})
	case r.Method == http.MethodPost && r.URL.Path == "/v2/entries:list":
		_ = json.NewDecoder(r.Body).Decode(&f.logQuery)
		var entries []map[string]any
		for _, l := range f.logs {
			entries = append(entries, map[string]any{"textPayload": l})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
	default:
		w.WriteHeader(404)
	}
}

func setup(t *testing.T) (*fakeGCP, *mcp.ClientSession) {
	f := &fakeGCP{uploaded: map[string]string{}, status: "SUCCESS", tags: []string{"workbench"}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := Config{Project: "wb-builds", Location: "us-central1", Repository: "builds", Bucket: "wb-staging",
		ServiceAccount: "builds@wb-builds.iam.gserviceaccount.com", CloudBuildURL: srv.URL, StorageURL: srv.URL, LoggingURL: srv.URL}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s := mcpx.NewServer(mcpx.Options{Name: "build-mcp", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	src := fakeSource{trees: map[string][]byte{
		"py-app":   tgz(t, "uv.lock", "pyproject.toml", "app/main.py"),
		"node-app": tgz(t, "package-lock.json", "package.json"),
		"bare-app": tgz(t, "README.md", "sub/uv.lock"),
	}}
	Register(s, NewClient(cfg, src, srv.Client()))
	return f, mcpxtest.Connect(t, s, "alice")
}

func TestStartUploadsAndBuildsIntoTheBuildsRepository(t *testing.T) {
	f, cs := setup(t)
	var out StartOut
	mcpxtest.MustOK(t, cs, "build_start_image", map[string]any{"project": "expense-tracker"}, &out)
	if out.BuildID != buildID || out.Commit != sha || out.Image != prefix+"expense-tracker:0123456789ab" {
		t.Fatalf("%+v", out)
	}
	if f.uploaded["sources/expense-tracker/"+sha+".tgz"] != "tgz" {
		t.Fatalf("uploaded %v", f.uploaded)
	}
	b, _ := json.Marshal(f.created)
	for _, want := range []string{`"images":["` + prefix + `expense-tracker:0123456789ab"]`, `"bucket":"wb-staging"`,
		`serviceAccounts/builds@wb-builds.iam.gserviceaccount.com`, `"workbench"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("build request lacks %s: %s", want, b)
		}
	}
}

func TestStartRefusesProjectsTheSourceRefuses(t *testing.T) {
	_, cs := setup(t)
	mcpxtest.MustErr(t, cs, "build_start_image", map[string]any{"project": "../other/app"}, "outside the Workbench group")
}

func TestStatusReturnsDigestPinnedRef(t *testing.T) {
	_, cs := setup(t)
	var out StatusOut
	mcpxtest.MustOK(t, cs, "build_get_status", map[string]any{"build_id": buildID}, &out)
	if !out.Done || out.ImageRef != prefix+"expense-tracker@"+digest {
		t.Fatalf("%+v", out)
	}
}

func TestStatusReportsFailureAndRefusesForeignBuilds(t *testing.T) {
	f, cs := setup(t)
	f.status = "FAILURE"
	var out StatusOut
	mcpxtest.MustOK(t, cs, "build_get_status", map[string]any{"build_id": buildID}, &out)
	f.steps = []map[string]any{{"id": "", "status": "SUCCESS"}, {"id": "", "status": "FAILURE"}}
	f.logs = []string{"ERROR: failed to solve", "Step #0: COPY app ./app"}
	mcpxtest.MustOK(t, cs, "build_get_status", map[string]any{"build_id": buildID}, &out)
	if !out.Done || out.ImageRef != "" || out.Detail != "step 0 failed" || out.Output != "Step #0: COPY app ./app\nERROR: failed to solve" {
		t.Fatalf("%+v", out)
	}
	f.tags = nil
	mcpxtest.MustErr(t, cs, "build_get_status", map[string]any{"build_id": buildID}, "not started by Workbench")
	mcpxtest.MustErr(t, cs, "build_get_status", map[string]any{"build_id": "../../x"}, "build_id")
}

func TestRunChecksChoosesStepsFromTheProject(t *testing.T) {
	f, cs := setup(t)
	var out ChecksOut
	mcpxtest.MustOK(t, cs, "build_run_checks", map[string]any{"project": "py-app"}, &out)
	if out.BuildID != buildID || strings.Join(out.Checks, ",") != "install,lint,format,tests" {
		t.Fatalf("%+v", out)
	}
	b, _ := json.Marshal(f.created)
	for _, want := range []string{`"uv run pytest -q"`, `"wb-kind-checks"`, `"workbench"`, uvImage} {
		if !strings.Contains(string(b), want) {
			t.Errorf("checks request lacks %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), `"images"`) {
		t.Errorf("a check run must push no image: %s", b)
	}
	// A second run at the same commit reuses the uploaded source.
	mcpxtest.MustOK(t, cs, "build_run_checks", map[string]any{"project": "py-app"}, &out)
	mcpxtest.MustOK(t, cs, "build_run_checks", map[string]any{"project": "node-app"}, &out)
	if strings.Join(out.Checks, ",") != "install,lint,tests" {
		t.Fatalf("%+v", out)
	}
	mcpxtest.MustErr(t, cs, "build_run_checks", map[string]any{"project": "bare-app"}, "checks are unknown")
}

func TestStatusOfChecksReturnsOutputAndTheFailedStep(t *testing.T) {
	f, cs := setup(t)
	f.tags = []string{"workbench", "wb-kind-checks"}
	f.logs = []string{"Step #3 - \"tests\": 4 passed in 0.31s"}
	var out StatusOut
	mcpxtest.MustOK(t, cs, "build_get_status", map[string]any{"build_id": buildID}, &out)
	if !out.Done || out.ImageRef != "" || !strings.Contains(out.Output, "4 passed") {
		t.Fatalf("%+v", out)
	}
	if !strings.Contains(f.logQuery["filter"].(string), buildID) {
		t.Fatalf("log query %v", f.logQuery)
	}
	f.status = "FAILURE"
	f.steps = []map[string]any{{"id": "install", "status": "SUCCESS"}, {"id": "lint", "status": "FAILURE"}, {"id": "tests", "status": "QUEUED"}}
	f.logs = []string{"Step #1 - \"lint\": F401 unused import"}
	mcpxtest.MustOK(t, cs, "build_get_status", map[string]any{"build_id": buildID}, &out)
	if out.FailedStep != "lint" || !strings.Contains(out.Output, "F401") {
		t.Fatalf("%+v", out)
	}
}
