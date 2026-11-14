package gitlab

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx/mcpxtest"
)

// Built from pieces so secret scanners do not mistake the fixture for a real token.
const testToken = "glpat" + "-" + "TESTTOKENvalue123456"

type recorded struct {
	Method string
	Path   string // escaped path
	Query  string
	Body   map[string]any
	Token  string
}

type fakeGitLab struct {
	t        *testing.T
	mu       sync.Mutex
	reqs     []recorded
	projects map[string]map[string]any // escaped full path -> project
	handlers map[string]http.HandlerFunc
}

func newFake(t *testing.T) (*fakeGitLab, *httptest.Server) {
	f := &fakeGitLab{t: t, projects: map[string]map[string]any{}, handlers: map[string]http.HandlerFunc{}}
	f.addProject(10, "workbench/templates/node-app", "main")
	f.addProject(42, "workbench/apps/my-app", "main")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitLab) addProject(id int, path, def string) {
	f.projects["/api/v4/projects/"+strings.ReplaceAll(path, "/", "%2F")] = map[string]any{
		"id": id, "name": path[strings.LastIndex(path, "/")+1:], "path_with_namespace": path,
		"web_url": "https://gitlab.example.com/" + path, "default_branch": def, "visibility": "private",
	}
}

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := recorded{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Token: r.Header.Get("Private-Token")}
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &rec.Body)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, rec)
	f.mu.Unlock()
	if rec.Token != testToken {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
		return
	}
	if h, ok := f.handlers[r.Method+" "+rec.Path]; ok {
		h(w, r)
		return
	}
	if p, ok := f.projects[rec.Path]; ok && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(p)
		return
	}
	w.WriteHeader(404)
	_, _ = w.Write([]byte(`{"message":"404 Project Not Found"}`))
}

func (f *fakeGitLab) last() recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeGitLab) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func setup(t *testing.T) (*fakeGitLab, *mcp.ClientSession) {
	t.Helper()
	f, srv := newFake(t)
	dir := t.TempDir()
	tokFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokFile, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{BaseURL: srv.URL, AllowedGroup: "workbench/apps", TemplateGroup: "workbench/templates", TokenFile: tokFile}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	tok := FileToken(tokFile)
	red := mcpx.NewRedactor()
	red.AddSource(func() []string { v, _ := tok(); return []string{v} })
	s := mcpx.NewServer(mcpx.Options{Name: "gitlab-mcp", Redactor: red, MaxRequestBytes: MaxRequestBytes, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	Register(s, NewClient(cfg, srv.Client(), tok))
	return f, mcpxtest.Connect(t, s, "alice@example.com")
}

func TestToolsAndAnnotations(t *testing.T) {
	_, cs := setup(t)
	tools := mcpxtest.ToolMap(t, cs)
	want := map[string]mcpxtest.Hints{
		"gitlab_create_project":       {ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false},
		"gitlab_get_project":          {ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
		"gitlab_list_branches":        {ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
		"gitlab_create_branch":        {ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false},
		"gitlab_commit_files":         {ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false},
		"gitlab_create_merge_request": {ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false},
		"gitlab_get_pipeline_status":  {ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
	}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tools), len(want))
	}
	for name, h := range want {
		tl, ok := tools[name]
		if !ok {
			t.Fatalf("missing tool %s", name)
		}
		if got := mcpxtest.HintsOf(tl); got != h {
			t.Errorf("%s hints = %+v, want %+v", name, got, h)
		}
		if tl.OutputSchema == nil {
			t.Errorf("%s has no output schema", name)
		}
	}
}

func TestCreateProjectForksTemplateIntoGroup(t *testing.T) {
	f, cs := setup(t)
	f.handlers["POST /api/v4/projects/10/fork"] = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 99, "name": "new-app", "path_with_namespace": "workbench/apps/new-app",
			"web_url": "https://gitlab.example.com/workbench/apps/new-app", "import_status": "scheduled",
			"forked_from_project": map[string]any{"path_with_namespace": "workbench/templates/node-app"},
		})
	}
	var out Project
	mcpxtest.MustOK(t, cs, "gitlab_create_project", map[string]any{"name": "new-app", "template": "node-app", "description": "Expense tracker"}, &out)
	if out.ID != 99 || out.Path != "workbench/apps/new-app" || out.ImportStatus != "scheduled" || out.ForkedFrom != "workbench/templates/node-app" {
		t.Fatalf("unexpected %+v", out)
	}
	req := f.last()
	if req.Body["namespace_path"] != "workbench/apps" || req.Body["visibility"] != "private" || req.Body["path"] != "new-app" || req.Body["mr_default_target_self"] != true {
		t.Fatalf("fork body %+v", req.Body)
	}
	if !strings.Contains(req.Body["description"].(string), "alice@example.com") {
		t.Fatalf("requester not recorded: %v", req.Body["description"])
	}
}

func TestCreateProjectValidation(t *testing.T) {
	f, cs := setup(t)
	mcpxtest.MustErr(t, cs, "gitlab_create_project", map[string]any{"name": "Bad_Name", "template": "node-app"}, "invalid argument", "name")
	mcpxtest.MustErr(t, cs, "gitlab_create_project", map[string]any{"name": "ok", "template": "../../etc"}, "invalid argument", "template")
	if f.count() != 0 {
		t.Fatalf("validation errors must not reach GitLab (%d requests)", f.count())
	}
	mcpxtest.MustErr(t, cs, "gitlab_create_project", map[string]any{"name": "ok", "template": "missing"}, "template \"missing\"", "404")
}

func TestCreateProjectRejectsForkOutsideGroup(t *testing.T) {
	f, cs := setup(t)
	f.handlers["POST /api/v4/projects/10/fork"] = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "path_with_namespace": "someone/else"})
	}
	mcpxtest.MustErr(t, cs, "gitlab_create_project", map[string]any{"name": "x", "template": "node-app"}, "outside the Workbench group")
}

func TestGetProjectGroupEnforcement(t *testing.T) {
	f, cs := setup(t)
	var out Project
	mcpxtest.MustOK(t, cs, "gitlab_get_project", map[string]any{"project": "my-app"}, &out)
	if out.ID != 42 || out.DefaultBranch != "main" {
		t.Fatalf("unexpected %+v", out)
	}
	mcpxtest.MustOK(t, cs, "gitlab_get_project", map[string]any{"project": "workbench/apps/my-app"}, &out)

	n := f.count()
	for _, p := range []string{"workbench/templates/node-app", "other/group/app", "workbench/apps-evil/x", "workbench/apps/../x", "workbench/apps/a b", ""} {
		mcpxtest.MustErr(t, cs, "gitlab_get_project", map[string]any{"project": p}, "invalid argument")
	}
	if f.count() != n {
		t.Fatal("out-of-group projects must be rejected before calling GitLab")
	}
	// A path inside the group that GitLab redirects elsewhere is rejected.
	f.projects["/api/v4/projects/workbench%2Fapps%2Fmoved"] = map[string]any{"id": 7, "path_with_namespace": "elsewhere/moved"}
	mcpxtest.MustErr(t, cs, "gitlab_get_project", map[string]any{"project": "moved"}, "outside the Workbench group")
}

func TestListBranchesPagination(t *testing.T) {
	f, cs := setup(t)
	f.handlers["GET /api/v4/projects/42/repository/branches"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "main", "default": true, "protected": true, "commit": map[string]any{"id": "abc", "title": "init"}}})
	}
	var out ListBranchesOut
	mcpxtest.MustOK(t, cs, "gitlab_list_branches", map[string]any{"project": "my-app", "per_page": 1000}, &out)
	if out.NextPage != 2 || len(out.Branches) != 1 || out.Branches[0].CommitSHA != "abc" || !out.Branches[0].Default {
		t.Fatalf("unexpected %+v", out)
	}
	if q := f.last().Query; !strings.Contains(q, "per_page=100") || !strings.Contains(q, "page=1") {
		t.Fatalf("per_page not clamped: %s", q)
	}
	out = ListBranchesOut{}
	mcpxtest.MustOK(t, cs, "gitlab_list_branches", map[string]any{"project": "my-app", "page": 2}, &out)
	if out.NextPage != 0 {
		t.Fatalf("expected last page, got %+v", out)
	}
}

func TestCreateBranchDefaultsRef(t *testing.T) {
	f, cs := setup(t)
	f.handlers["POST /api/v4/projects/42/repository/branches"] = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"name": r.URL.Query().Get("branch"), "commit": map[string]any{"id": "def"}})
	}
	var out Branch
	mcpxtest.MustOK(t, cs, "gitlab_create_branch", map[string]any{"project": "my-app", "branch": "wb/feature-1"}, &out)
	if out.Name != "wb/feature-1" {
		t.Fatalf("unexpected %+v", out)
	}
	if q := f.last().Query; q != "branch=wb%2Ffeature-1&ref=main" {
		t.Fatalf("query %s", q)
	}
	mcpxtest.MustErr(t, cs, "gitlab_create_branch", map[string]any{"project": "my-app", "branch": "a..b"}, "invalid argument")
}

func TestCommitFiles(t *testing.T) {
	f, cs := setup(t)
	f.handlers["POST /api/v4/projects/42/repository/commits"] = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "0123456789abcdef", "short_id": "01234567", "title": "Add app"})
	}
	var out Commit
	mcpxtest.MustOK(t, cs, "gitlab_commit_files", map[string]any{
		"project": "my-app", "branch": "wb/feature-1", "start_branch": "main", "message": "Add app",
		"actions": []map[string]any{
			{"action": "create", "file_path": "src/main.go", "content": "package main"},
			{"action": "update", "file_path": "README.md", "content": "aGk=", "encoding": "base64"},
			{"action": "delete", "file_path": "old.txt"},
			{"action": "move", "file_path": "b.txt", "previous_path": "a.txt"},
		},
	}, &out)
	if out.SHA != "0123456789abcdef" || out.Files != 4 {
		t.Fatalf("unexpected %+v", out)
	}
	body := f.last().Body
	if msg := body["commit_message"].(string); msg != "Add app\n\nWorkbench-Requester: alice@example.com" {
		t.Fatalf("message %q", msg)
	}
	acts := body["actions"].([]any)
	if a := acts[0].(map[string]any); a["encoding"] != "text" || a["content"] != "package main" {
		t.Fatalf("action 0 %+v", a)
	}
	if a := acts[2].(map[string]any); a["content"] != nil {
		t.Fatalf("delete must not carry content: %+v", a)
	}
	if body["start_branch"] != "main" {
		t.Fatalf("start_branch missing")
	}
}

func TestCommitFilesValidation(t *testing.T) {
	f, cs := setup(t)
	base := func(actions any) map[string]any {
		return map[string]any{"project": "my-app", "branch": "b", "message": "m", "actions": actions}
	}
	cases := map[string]any{
		"empty":      []map[string]any{},
		"traversal":  []map[string]any{{"action": "create", "file_path": "../../etc/passwd", "content": "x"}},
		"absolute":   []map[string]any{{"action": "create", "file_path": "/etc/passwd", "content": "x"}},
		"dotgit":     []map[string]any{{"action": "update", "file_path": ".git/hooks/pre-commit", "content": "x"}},
		"badaction":  []map[string]any{{"action": "chmod", "file_path": "a"}},
		"delcontent": []map[string]any{{"action": "delete", "file_path": "a", "content": "x"}},
		"movenoprev": []map[string]any{{"action": "move", "file_path": "a"}},
		"badenc":     []map[string]any{{"action": "create", "file_path": "a", "encoding": "gzip"}},
		"dup":        []map[string]any{{"action": "create", "file_path": "a"}, {"action": "update", "file_path": "a"}},
	}
	for name, acts := range cases {
		t.Run(name, func(t *testing.T) {
			mcpxtest.MustErr(t, cs, "gitlab_commit_files", base(acts), "invalid argument")
		})
	}
	many := make([]map[string]any, MaxCommitActions+1)
	for i := range many {
		many[i] = map[string]any{"action": "create", "file_path": fmt.Sprintf("f%d", i)}
	}
	mcpxtest.MustErr(t, cs, "gitlab_commit_files", base(many), "at most 100 actions")
	big := []map[string]any{{"action": "create", "file_path": "big", "content": strings.Repeat("x", MaxCommitBytes+1)}}
	mcpxtest.MustErr(t, cs, "gitlab_commit_files", base(big), "limit is")
	if f.count() != 0 {
		t.Fatalf("validation errors must not reach GitLab (%d requests)", f.count())
	}
}

func TestCreateMergeRequest(t *testing.T) {
	f, cs := setup(t)
	f.handlers["POST /api/v4/projects/42/merge_requests"] = func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		_ = json.NewEncoder(w).Encode(map[string]any{"iid": 3, "title": "x", "state": "opened", "web_url": "u", "source_branch": "wb/f", "target_branch": "main", "detailed_merge_status": "checking"})
	}
	var out MergeRequest
	mcpxtest.MustOK(t, cs, "gitlab_create_merge_request", map[string]any{"project": "my-app", "source_branch": "wb/f", "title": "Add app", "description": "Does things", "draft": true}, &out)
	if out.IID != 3 || out.MergeStatus != "checking" {
		t.Fatalf("unexpected %+v", out)
	}
	b := f.last().Body
	if b["target_project_id"] != float64(42) || b["target_branch"] != "main" || b["title"] != "Draft: Add app" {
		t.Fatalf("body %+v", b)
	}
	if !strings.Contains(b["description"].(string), "alice@example.com") {
		t.Fatalf("description lacks requester: %v", b["description"])
	}
	mcpxtest.MustErr(t, cs, "gitlab_create_merge_request", map[string]any{"project": "my-app", "source_branch": "main", "title": "x"}, "must differ")
}

func TestPipelineStatus(t *testing.T) {
	f, cs := setup(t)
	f.handlers["GET /api/v4/projects/42/pipelines"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") == "none" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 500, "status": "failed", "ref": "main", "sha": "abc"}})
	}
	f.handlers["GET /api/v4/projects/42/pipelines/500/jobs"] = func(w http.ResponseWriter, r *http.Request) {
		jobs := make([]map[string]any, MaxJobs+1)
		for i := range jobs {
			jobs[i] = map[string]any{"id": i, "name": fmt.Sprintf("job%d", i), "stage": "test", "status": "failed", "failure_reason": "script_failure"}
		}
		_ = json.NewEncoder(w).Encode(jobs)
	}
	var out PipelineOut
	mcpxtest.MustOK(t, cs, "gitlab_get_pipeline_status", map[string]any{"project": "my-app"}, &out)
	if !out.Found || out.ID != 500 || out.Status != "failed" || len(out.Jobs) != MaxJobs || !out.JobsTruncated || out.Jobs[0].FailureReason != "script_failure" {
		t.Fatalf("unexpected %+v", out)
	}
	mcpxtest.MustOK(t, cs, "gitlab_get_pipeline_status", map[string]any{"project": "my-app", "ref": "none"}, &out)
	if out.Found {
		t.Fatalf("expected not found: %+v", out)
	}
}

func TestTokenNeverEchoed(t *testing.T) {
	f, cs := setup(t)
	// A hostile or buggy upstream echoes request headers back in its error.
	f.handlers["POST /api/v4/projects/42/repository/branches"] = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = fmt.Fprintf(w, `{"message":"bad request, your token was %s"}`, r.Header.Get("Private-Token"))
	}
	txt := mcpxtest.MustErr(t, cs, "gitlab_create_branch", map[string]any{"project": "my-app", "branch": "x"}, "[REDACTED]")
	if strings.Contains(txt, testToken) {
		t.Fatalf("token leaked: %s", txt)
	}
	if f.last().Token != testToken {
		t.Fatal("token not sent in PRIVATE-TOKEN header")
	}
	// No tool accepts a token argument.
	for _, tl := range mcpxtest.ToolMap(t, cs) {
		b, _ := json.Marshal(tl.InputSchema)
		if strings.Contains(strings.ToLower(string(b)), "token") {
			t.Errorf("%s input schema mentions a token", tl.Name)
		}
	}
}

func TestMissingTokenFile(t *testing.T) {
	_, srv := newFake(t)
	cfg := Config{BaseURL: srv.URL, AllowedGroup: "workbench/apps", TemplateGroup: "workbench/templates", TokenFile: "/nonexistent/token"}
	s := mcpx.NewServer(mcpx.Options{Name: "gitlab-mcp", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	Register(s, NewClient(cfg, srv.Client(), FileToken(cfg.TokenFile)))
	cs := mcpxtest.Connect(t, s, "bob")
	mcpxtest.MustErr(t, cs, "gitlab_get_project", map[string]any{"project": "my-app"}, "token file")
}

func TestConfigValidate(t *testing.T) {
	bad := []Config{
		{},
		{BaseURL: "ftp://x", AllowedGroup: "a", TemplateGroup: "b", TokenFile: "f"},
		{BaseURL: "https://x", AllowedGroup: "a/../b", TemplateGroup: "b", TokenFile: "f"},
	}
	for _, c := range bad {
		if c.Validate() == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}
