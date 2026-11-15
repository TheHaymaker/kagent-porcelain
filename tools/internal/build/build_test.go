package build

import (
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

type fakeSource struct{}

func (fakeSource) Source(_ context.Context, project, ref string) (gitlab.Snapshot, error) {
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
}

func (f *fakeGCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/wb-staging/o"):
		b, _ := io.ReadAll(r.Body)
		f.uploaded[r.URL.Query().Get("name")] = string(b)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/wb-builds/builds":
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"build": map[string]any{"id": buildID, "logUrl": "https://console/logs"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/wb-builds/builds/"+buildID:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": buildID, "status": f.status, "statusDetail": "step 0 failed", "tags": f.tags,
			"results": map[string]any{"images": []map[string]any{{"name": prefix + "expense-tracker:0123456789ab", "digest": digest}}},
		})
	default:
		w.WriteHeader(404)
	}
}

func setup(t *testing.T) (*fakeGCP, *mcp.ClientSession) {
	f := &fakeGCP{uploaded: map[string]string{}, status: "SUCCESS", tags: []string{"workbench"}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := Config{Project: "wb-builds", Location: "us-central1", Repository: "builds", Bucket: "wb-staging",
		ServiceAccount: "builds@wb-builds.iam.gserviceaccount.com", CloudBuildURL: srv.URL, StorageURL: srv.URL}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s := mcpx.NewServer(mcpx.Options{Name: "build-mcp", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	Register(s, NewClient(cfg, fakeSource{}, srv.Client()))
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
	if !out.Done || out.ImageRef != "" || out.Detail != "step 0 failed" {
		t.Fatalf("%+v", out)
	}
	f.tags = nil
	mcpxtest.MustErr(t, cs, "build_get_status", map[string]any{"build_id": buildID}, "not started by Workbench")
	mcpxtest.MustErr(t, cs, "build_get_status", map[string]any{"build_id": "../../x"}, "build_id")
}
