package artifactregistry

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx/mcpxtest"
)

const repo = "/v1/projects/wb-builds/locations/europe-west1/repositories/builds"

var digest = "sha256:" + strings.Repeat("b", 64)

type fakeAR struct {
	mu   sync.Mutex
	uris []string // escaped path + query
}

func (f *fakeAR) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.uris = append(f.uris, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
	f.mu.Unlock()
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	enc := json.NewEncoder(w)
	p := r.URL.EscapedPath()
	switch {
	case p == repo+"/packages":
		if r.URL.Query().Get("pageToken") == "" {
			_ = enc.Encode(map[string]any{"packages": []map[string]any{
				{"name": repo[4:] + "/packages/my-app", "updateTime": "2026-11-14T00:00:00Z"},
				{"name": repo[4:] + "/packages/team%2Fsvc"},
			}, "nextPageToken": "p2"})
		} else {
			_ = enc.Encode(map[string]any{})
		}
	case p == repo+"/packages/team%2Fsvc/tags":
		_ = enc.Encode(map[string]any{"tags": []map[string]any{
			{"name": repo[4:] + "/packages/team%2Fsvc/tags/v1", "version": repo[4:] + "/packages/team%2Fsvc/versions/" + digest},
		}})
	case p == repo+"/packages/my-app/tags/abc123":
		_ = enc.Encode(map[string]any{"name": repo[4:] + "/packages/my-app/tags/abc123", "version": repo[4:] + "/packages/my-app/versions/" + digest})
	case p == repo+"/packages/my-app/tags/weird":
		_ = enc.Encode(map[string]any{"name": "x/tags/weird", "version": "x/versions/notadigest"})
	default:
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"not found"}}`))
	}
}

func setup(t *testing.T) (*fakeAR, *mcp.ClientSession) {
	f := &fakeAR{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := Config{Project: "wb-builds", Location: "europe-west1", Repository: "builds", BaseURL: srv.URL}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s := mcpx.NewServer(mcpx.Options{Name: "artifactregistry-mcp", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	Register(s, NewClient(cfg, srv.Client()))
	return f, mcpxtest.Connect(t, s, "alice")
}

func TestToolsAreReadOnly(t *testing.T) {
	_, cs := setup(t)
	tools := mcpxtest.ToolMap(t, cs)
	if len(tools) != 3 {
		t.Fatalf("got %d tools", len(tools))
	}
	for name, tl := range tools {
		if h := mcpxtest.HintsOf(tl); h != (mcpxtest.Hints{ReadOnly: true, Idempotent: true}) {
			t.Errorf("%s: %+v", name, h)
		}
		if strings.Contains(name, "delete") {
			t.Errorf("no delete tool allowed: %s", name)
		}
	}
}

func TestListImagesPagination(t *testing.T) {
	f, cs := setup(t)
	var out ListImagesOut
	mcpxtest.MustOK(t, cs, "artifactregistry_list_images", map[string]any{"page_size": 999}, &out)
	if len(out.Images) != 2 || out.NextPageToken != "p2" || out.Images[1].Name != "team/svc" ||
		out.Images[0].URI != "europe-west1-docker.pkg.dev/wb-builds/builds/my-app" {
		t.Fatalf("unexpected %+v", out)
	}
	if !strings.Contains(f.uris[0], "pageSize=200") || !strings.Contains(f.uris[0], "orderBy=update_time+desc") {
		t.Fatalf("query %s", f.uris[0])
	}
	out = ListImagesOut{}
	mcpxtest.MustOK(t, cs, "artifactregistry_list_images", map[string]any{"page_token": "p2"}, &out)
	if len(out.Images) != 0 || out.NextPageToken != "" {
		t.Fatalf("unexpected %+v", out)
	}
	mcpxtest.MustErr(t, cs, "artifactregistry_list_images", map[string]any{"page_token": "x&y=z"}, "invalid argument")
}

func TestListTagsNestedImage(t *testing.T) {
	_, cs := setup(t)
	var out ListTagsOut
	mcpxtest.MustOK(t, cs, "artifactregistry_list_tags", map[string]any{"image": "europe-west1-docker.pkg.dev/wb-builds/builds/team/svc"}, &out)
	if len(out.Tags) != 1 || out.Tags[0].Tag != "v1" || out.Tags[0].Digest != digest ||
		out.Tags[0].Ref != "europe-west1-docker.pkg.dev/wb-builds/builds/team/svc@"+digest {
		t.Fatalf("unexpected %+v", out)
	}
	mcpxtest.MustErr(t, cs, "artifactregistry_list_tags", map[string]any{"image": "missing"}, "not found")
}

func TestGetDigest(t *testing.T) {
	_, cs := setup(t)
	var out Tag
	mcpxtest.MustOK(t, cs, "artifactregistry_get_digest", map[string]any{"image": "my-app", "tag": "abc123"}, &out)
	if out.Digest != digest || out.Ref != "europe-west1-docker.pkg.dev/wb-builds/builds/my-app@"+digest {
		t.Fatalf("unexpected %+v", out)
	}
	mcpxtest.MustErr(t, cs, "artifactregistry_get_digest", map[string]any{"image": "my-app", "tag": "weird"}, "unexpected version")
	mcpxtest.MustErr(t, cs, "artifactregistry_get_digest", map[string]any{"image": "my-app", "tag": "nope"}, "not found")
}

func TestValidation(t *testing.T) {
	f, cs := setup(t)
	n := len(f.uris)
	for _, args := range []map[string]any{
		{"image": "us-docker.pkg.dev/other-proj/repo/app", "tag": "v1"},
		{"image": "my-app:latest", "tag": "v1"},
		{"image": "../secrets", "tag": "v1"},
		{"image": "My-App", "tag": "v1"},
		{"image": "my-app", "tag": "../x"},
		{"image": "my-app", "tag": ""},
	} {
		mcpxtest.MustErr(t, cs, "artifactregistry_get_digest", args, "invalid argument")
	}
	if len(f.uris) != n {
		t.Fatal("invalid input reached the API")
	}
}
