package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/validate"
)

// Limits.
const (
	MaxCommitActions = 100
	MaxCommitBytes   = 5 << 20 // total content per commit
	MaxJobs          = 50
	MaxPerPage       = 100
	// MaxRequestBytes is the MCP request body limit the gitlab server needs to
	// accept a maximal commit (JSON escaping can roughly double content size).
	MaxRequestBytes = 2*MaxCommitBytes + 1<<20
)

// Instructions is advertised to MCP clients.
const Instructions = `GitLab tools for Workbench apps. Projects live in one fixed group; pass a project as its bare name or full path.
Typical flow: gitlab_create_project (from a scaffold template) -> gitlab_create_branch -> gitlab_commit_files -> gitlab_create_merge_request -> gitlab_get_pipeline_status.
Never commit secrets; credentials are configured outside the repository.`

// Register adds all GitLab tools to s.
func Register(s *mcpx.Server, c *Client) {
	mcpx.AddTool(s, &mcp.Tool{
		Name: "gitlab_create_project",
		Description: "Create a new private Workbench project by forking a scaffold template into the Workbench group. " +
			"The fork is asynchronous: poll gitlab_get_project until import_status is 'finished' before committing.",
		Annotations: mcpx.Mutating("Create GitLab project from template", false, false, false),
	}, c.createProject)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "gitlab_get_project",
		Description: "Get a Workbench project's id, URL, default branch and import status.",
		Annotations: mcpx.ReadOnly("Get GitLab project", false),
	}, c.getProjectTool)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "gitlab_list_branches",
		Description: "List branches of a Workbench project (paginated; pass next_page back as page).",
		Annotations: mcpx.ReadOnly("List GitLab branches", false),
	}, c.listBranches)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "gitlab_create_branch",
		Description: "Create a branch in a Workbench project from a ref (branch, tag or commit; defaults to the default branch).",
		Annotations: mcpx.Mutating("Create GitLab branch", false, false, false),
	}, c.createBranch)
	mcpx.AddTool(s, &mcp.Tool{
		Name: "gitlab_commit_files",
		Description: fmt.Sprintf("Create one commit on a branch with up to %d file actions (create, update, delete, move). "+
			"Content is text unless encoding is base64. Total content is limited to %d bytes. "+
			"A Workbench-Requester trailer is appended to the message.", MaxCommitActions, MaxCommitBytes),
		Annotations: mcpx.Mutating("Commit files to GitLab", true, false, false),
	}, c.commitFiles)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "gitlab_create_merge_request",
		Description: "Open a merge request within the same Workbench project (source branch -> target branch, default: the project's default branch).",
		Annotations: mcpx.Mutating("Create GitLab merge request", false, false, false),
	}, c.createMergeRequest)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "gitlab_get_pipeline_status",
		Description: fmt.Sprintf("Get the latest pipeline for a ref (or a pipeline by id) with up to %d of its jobs.", MaxJobs),
		Annotations: mcpx.ReadOnly("Get GitLab pipeline status", false),
	}, c.getPipelineStatus)
}

// ---- create project ----

// CreateProjectIn are the arguments of gitlab_create_project.
type CreateProjectIn struct {
	Name        string `json:"name" jsonschema:"new project name and path: lowercase letters, digits, hyphens; starts with a letter"`
	Template    string `json:"template" jsonschema:"scaffold template project name in the template group"`
	Description string `json:"description,omitempty" jsonschema:"short project description"`
}

func (c *Client) createProject(ctx context.Context, call mcpx.Call, in CreateProjectIn) (Project, error) {
	if err := validate.Match("name", in.Name, validate.Slug, "lowercase letters, digits and hyphens, starting with a letter, at most 63 characters"); err != nil {
		return Project{}, err
	}
	if err := validate.Match("template", in.Template, validate.Slug, "a template name (lowercase letters, digits, hyphens)"); err != nil {
		return Project{}, err
	}
	if err := validate.Text("description", in.Description, false, 1000); err != nil {
		return Project{}, err
	}
	tmplPath := c.cfg.TemplateGroup + "/" + in.Template
	var tmpl apiProject
	if _, err := c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(tmplPath), nil, nil, &tmpl); err != nil {
		return Project{}, fmt.Errorf("template %q: %w", in.Template, err)
	}
	if !strings.EqualFold(tmpl.PathWithNamespace, tmplPath) {
		return Project{}, fmt.Errorf("template %q resolves outside the template group", in.Template)
	}
	desc := strings.TrimSpace(in.Description)
	if desc != "" {
		desc += " "
	}
	desc += "(Workbench app, requested by " + call.Requester + ")"
	body := map[string]any{
		"namespace_path":         c.cfg.AllowedGroup,
		"name":                   in.Name,
		"path":                   in.Name,
		"visibility":             "private",
		"description":            desc,
		"mr_default_target_self": true,
	}
	var p apiProject
	if _, err := c.do(ctx, http.MethodPost, "/projects/"+strconv.Itoa(tmpl.ID)+"/fork", nil, body, &p); err != nil {
		return Project{}, err
	}
	if !c.inGroup(p.PathWithNamespace) {
		return Project{}, fmt.Errorf("fork landed at %q, outside the Workbench group", p.PathWithNamespace)
	}
	return p.summary(), nil
}

// ---- get project ----

// ProjectIn identifies a project.
type ProjectIn struct {
	Project string `json:"project" jsonschema:"project name in the Workbench group, or its full path"`
}

func (c *Client) getProjectTool(ctx context.Context, _ mcpx.Call, in ProjectIn) (Project, error) {
	p, err := c.resolve(ctx, in.Project)
	if err != nil {
		return Project{}, err
	}
	return p.summary(), nil
}

// ---- branches ----

// ListBranchesIn are the arguments of gitlab_list_branches.
type ListBranchesIn struct {
	Project string `json:"project" jsonschema:"project name or full path"`
	Search  string `json:"search,omitempty" jsonschema:"only branches containing this string"`
	Page    int    `json:"page,omitempty" jsonschema:"page number, starting at 1"`
	PerPage int    `json:"per_page,omitempty" jsonschema:"results per page (default 20, max 100)"`
}

// Branch is a branch summary.
type Branch struct {
	Name        string `json:"name"`
	CommitSHA   string `json:"commit_sha"`
	CommitTitle string `json:"commit_title,omitempty"`
	Protected   bool   `json:"protected"`
	Default     bool   `json:"default"`
	WebURL      string `json:"web_url,omitempty"`
}

// ListBranchesOut is the result of gitlab_list_branches.
type ListBranchesOut struct {
	Branches []Branch `json:"branches"`
	NextPage int      `json:"next_page,omitempty" jsonschema:"pass as page to get more; absent when this is the last page"`
}

type apiBranch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Default   bool   `json:"default"`
	WebURL    string `json:"web_url"`
	Commit    struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"commit"`
}

func (b apiBranch) summary() Branch {
	return Branch{Name: b.Name, CommitSHA: b.Commit.ID, CommitTitle: b.Commit.Title, Protected: b.Protected, Default: b.Default, WebURL: b.WebURL}
}

func (c *Client) listBranches(ctx context.Context, _ mcpx.Call, in ListBranchesIn) (ListBranchesOut, error) {
	if err := validate.Text("search", in.Search, false, 200); err != nil {
		return ListBranchesOut{}, err
	}
	p, err := c.resolve(ctx, in.Project)
	if err != nil {
		return ListBranchesOut{}, err
	}
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(validate.Bounded(in.PerPage, 20, MaxPerPage)))
	q.Set("page", strconv.Itoa(max(in.Page, 1)))
	if in.Search != "" {
		q.Set("search", in.Search)
	}
	var bs []apiBranch
	h, err := c.do(ctx, http.MethodGet, "/projects/"+pid(p)+"/repository/branches", q, nil, &bs)
	if err != nil {
		return ListBranchesOut{}, err
	}
	out := ListBranchesOut{Branches: make([]Branch, 0, len(bs)), NextPage: nextPage(h)}
	for _, b := range bs {
		out.Branches = append(out.Branches, b.summary())
	}
	return out, nil
}

// CreateBranchIn are the arguments of gitlab_create_branch.
type CreateBranchIn struct {
	Project string `json:"project" jsonschema:"project name or full path"`
	Branch  string `json:"branch" jsonschema:"new branch name"`
	Ref     string `json:"ref,omitempty" jsonschema:"branch, tag or commit SHA to branch from (default: the default branch)"`
}

func (c *Client) createBranch(ctx context.Context, _ mcpx.Call, in CreateBranchIn) (Branch, error) {
	if err := validate.GitRefName("branch", in.Branch); err != nil {
		return Branch{}, err
	}
	if in.Ref != "" {
		if err := validate.GitRefName("ref", in.Ref); err != nil {
			return Branch{}, err
		}
	}
	p, err := c.resolve(ctx, in.Project)
	if err != nil {
		return Branch{}, err
	}
	ref := in.Ref
	if ref == "" {
		ref = p.DefaultBranch
	}
	if ref == "" {
		return Branch{}, fmt.Errorf("project %q has no default branch yet (is the import finished?)", p.PathWithNamespace)
	}
	q := url.Values{"branch": {in.Branch}, "ref": {ref}}
	var b apiBranch
	if _, err := c.do(ctx, http.MethodPost, "/projects/"+pid(p)+"/repository/branches", q, nil, &b); err != nil {
		return Branch{}, err
	}
	return b.summary(), nil
}

// ---- commits ----

// CommitAction is one file operation.
type CommitAction struct {
	Action       string `json:"action" jsonschema:"one of create, update, delete, move"`
	FilePath     string `json:"file_path" jsonschema:"repository-relative path"`
	PreviousPath string `json:"previous_path,omitempty" jsonschema:"for move: the old path"`
	Content      string `json:"content,omitempty" jsonschema:"file content for create/update (and optionally move)"`
	Encoding     string `json:"encoding,omitempty" jsonschema:"text (default) or base64"`
}

// CommitFilesIn are the arguments of gitlab_commit_files.
type CommitFilesIn struct {
	Project     string         `json:"project" jsonschema:"project name or full path"`
	Branch      string         `json:"branch" jsonschema:"branch to commit to"`
	StartBranch string         `json:"start_branch,omitempty" jsonschema:"create branch from this branch if it does not exist"`
	Message     string         `json:"message" jsonschema:"commit message"`
	Actions     []CommitAction `json:"actions" jsonschema:"file actions, applied atomically in one commit"`
}

// Commit is a commit summary.
type Commit struct {
	SHA     string `json:"sha"`
	ShortID string `json:"short_id"`
	Title   string `json:"title"`
	WebURL  string `json:"web_url,omitempty"`
	Files   int    `json:"files"`
}

var validActions = map[string]bool{"create": true, "update": true, "delete": true, "move": true}

func validateActions(actions []CommitAction) error {
	if len(actions) == 0 {
		return validate.Errorf("actions", "at least one action is required")
	}
	if len(actions) > MaxCommitActions {
		return validate.Errorf("actions", "at most %d actions per commit (got %d)", MaxCommitActions, len(actions))
	}
	total := 0
	seen := map[string]bool{}
	for i, a := range actions {
		f := fmt.Sprintf("actions[%d]", i)
		if !validActions[a.Action] {
			return validate.Errorf(f+".action", "%q must be one of create, update, delete, move", validate.Clip(a.Action, 20))
		}
		if err := validate.RepoPath(f+".file_path", a.FilePath); err != nil {
			return err
		}
		if seen[a.FilePath] {
			return validate.Errorf(f+".file_path", "%q appears more than once", a.FilePath)
		}
		seen[a.FilePath] = true
		switch a.Action {
		case "move":
			if err := validate.RepoPath(f+".previous_path", a.PreviousPath); err != nil {
				return err
			}
		case "delete":
			if a.Content != "" {
				return validate.Errorf(f+".content", "must be empty for delete")
			}
		}
		if a.Encoding != "" && a.Encoding != "text" && a.Encoding != "base64" {
			return validate.Errorf(f+".encoding", "must be text or base64")
		}
		total += len(a.Content)
	}
	if total > MaxCommitBytes {
		return validate.Errorf("actions", "total content is %d bytes; the limit is %d", total, MaxCommitBytes)
	}
	return nil
}

// withTrailer appends the requester trailer to a commit message.
func withTrailer(msg, requester string) string {
	return strings.TrimRight(msg, "\n") + "\n\nWorkbench-Requester: " + requester
}

func (c *Client) commitFiles(ctx context.Context, call mcpx.Call, in CommitFilesIn) (Commit, error) {
	if err := validate.GitRefName("branch", in.Branch); err != nil {
		return Commit{}, err
	}
	if in.StartBranch != "" {
		if err := validate.GitRefName("start_branch", in.StartBranch); err != nil {
			return Commit{}, err
		}
	}
	if err := validate.Text("message", in.Message, true, 5000); err != nil {
		return Commit{}, err
	}
	if err := validateActions(in.Actions); err != nil {
		return Commit{}, err
	}
	p, err := c.resolve(ctx, in.Project)
	if err != nil {
		return Commit{}, err
	}
	acts := make([]map[string]any, 0, len(in.Actions))
	for _, a := range in.Actions {
		m := map[string]any{"action": a.Action, "file_path": a.FilePath}
		if a.PreviousPath != "" {
			m["previous_path"] = a.PreviousPath
		}
		if a.Action != "delete" {
			m["content"] = a.Content
			enc := a.Encoding
			if enc == "" {
				enc = "text"
			}
			m["encoding"] = enc
		}
		acts = append(acts, m)
	}
	body := map[string]any{
		"branch":         in.Branch,
		"commit_message": withTrailer(in.Message, call.Requester),
		"actions":        acts,
	}
	if in.StartBranch != "" {
		body["start_branch"] = in.StartBranch
	}
	var res struct {
		ID      string `json:"id"`
		ShortID string `json:"short_id"`
		Title   string `json:"title"`
		WebURL  string `json:"web_url"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/projects/"+pid(p)+"/repository/commits", nil, body, &res); err != nil {
		return Commit{}, err
	}
	return Commit{SHA: res.ID, ShortID: res.ShortID, Title: res.Title, WebURL: res.WebURL, Files: len(in.Actions)}, nil
}

// ---- merge requests ----

// CreateMergeRequestIn are the arguments of gitlab_create_merge_request.
type CreateMergeRequestIn struct {
	Project      string `json:"project" jsonschema:"project name or full path"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch,omitempty" jsonschema:"default: the project's default branch"`
	Title        string `json:"title"`
	Description  string `json:"description,omitempty" jsonschema:"markdown description"`
	Draft        bool   `json:"draft,omitempty" jsonschema:"open as a draft"`
}

// MergeRequest is a merge request summary.
type MergeRequest struct {
	IID          int    `json:"iid"`
	Title        string `json:"title"`
	State        string `json:"state"`
	WebURL       string `json:"web_url"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	MergeStatus  string `json:"merge_status,omitempty"`
}

func (c *Client) createMergeRequest(ctx context.Context, call mcpx.Call, in CreateMergeRequestIn) (MergeRequest, error) {
	if err := validate.GitRefName("source_branch", in.SourceBranch); err != nil {
		return MergeRequest{}, err
	}
	if in.TargetBranch != "" {
		if err := validate.GitRefName("target_branch", in.TargetBranch); err != nil {
			return MergeRequest{}, err
		}
	}
	if err := validate.Text("title", in.Title, true, 255); err != nil {
		return MergeRequest{}, err
	}
	if err := validate.Text("description", in.Description, false, 50000); err != nil {
		return MergeRequest{}, err
	}
	p, err := c.resolve(ctx, in.Project)
	if err != nil {
		return MergeRequest{}, err
	}
	target := in.TargetBranch
	if target == "" {
		target = p.DefaultBranch
	}
	if target == in.SourceBranch {
		return MergeRequest{}, validate.Errorf("target_branch", "must differ from source_branch")
	}
	title := in.Title
	if in.Draft && !strings.HasPrefix(strings.ToLower(title), "draft:") {
		title = "Draft: " + title
	}
	desc := strings.TrimRight(in.Description, "\n") + "\n\n---\nOpened via Workbench for " + call.Requester + "."
	body := map[string]any{
		"source_branch":        in.SourceBranch,
		"target_branch":        target,
		"target_project_id":    p.ID, // never the template upstream
		"title":                title,
		"description":          strings.TrimLeft(desc, "\n"),
		"remove_source_branch": true,
	}
	var mr struct {
		IID                 int    `json:"iid"`
		Title               string `json:"title"`
		State               string `json:"state"`
		WebURL              string `json:"web_url"`
		SourceBranch        string `json:"source_branch"`
		TargetBranch        string `json:"target_branch"`
		DetailedMergeStatus string `json:"detailed_merge_status"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/projects/"+pid(p)+"/merge_requests", nil, body, &mr); err != nil {
		return MergeRequest{}, err
	}
	return MergeRequest{IID: mr.IID, Title: mr.Title, State: mr.State, WebURL: mr.WebURL,
		SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch, MergeStatus: mr.DetailedMergeStatus}, nil
}

// ---- pipelines ----

// PipelineIn are the arguments of gitlab_get_pipeline_status.
type PipelineIn struct {
	Project    string `json:"project" jsonschema:"project name or full path"`
	Ref        string `json:"ref,omitempty" jsonschema:"branch or tag; the latest pipeline for it is returned (default: the default branch)"`
	PipelineID int    `json:"pipeline_id,omitempty" jsonschema:"a specific pipeline id (overrides ref)"`
}

// Job is a pipeline job summary.
type Job struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Stage         string `json:"stage"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason,omitempty"`
	WebURL        string `json:"web_url,omitempty"`
}

// PipelineOut is the result of gitlab_get_pipeline_status.
type PipelineOut struct {
	Found         bool   `json:"found" jsonschema:"false when no pipeline exists for the ref"`
	ID            int    `json:"id,omitempty"`
	Status        string `json:"status,omitempty" jsonschema:"created, pending, running, success, failed, canceled, skipped, manual, ..."`
	Ref           string `json:"ref,omitempty"`
	SHA           string `json:"sha,omitempty"`
	WebURL        string `json:"web_url,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	Jobs          []Job  `json:"jobs,omitempty"`
	JobsTruncated bool   `json:"jobs_truncated,omitempty"`
}

type apiPipeline struct {
	ID        int    `json:"id"`
	Status    string `json:"status"`
	Ref       string `json:"ref"`
	SHA       string `json:"sha"`
	WebURL    string `json:"web_url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (c *Client) getPipelineStatus(ctx context.Context, _ mcpx.Call, in PipelineIn) (PipelineOut, error) {
	if in.Ref != "" {
		if err := validate.GitRefName("ref", in.Ref); err != nil {
			return PipelineOut{}, err
		}
	}
	if in.PipelineID < 0 {
		return PipelineOut{}, validate.Errorf("pipeline_id", "must be positive")
	}
	p, err := c.resolve(ctx, in.Project)
	if err != nil {
		return PipelineOut{}, err
	}
	var pl apiPipeline
	if in.PipelineID > 0 {
		if _, err := c.do(ctx, http.MethodGet, "/projects/"+pid(p)+"/pipelines/"+strconv.Itoa(in.PipelineID), nil, nil, &pl); err != nil {
			return PipelineOut{}, err
		}
	} else {
		ref := in.Ref
		if ref == "" {
			ref = p.DefaultBranch
		}
		q := url.Values{"ref": {ref}, "per_page": {"1"}, "order_by": {"id"}, "sort": {"desc"}}
		var pls []apiPipeline
		if _, err := c.do(ctx, http.MethodGet, "/projects/"+pid(p)+"/pipelines", q, nil, &pls); err != nil {
			return PipelineOut{}, err
		}
		if len(pls) == 0 {
			return PipelineOut{Found: false, Ref: ref}, nil
		}
		pl = pls[0]
	}
	out := PipelineOut{Found: true, ID: pl.ID, Status: pl.Status, Ref: pl.Ref, SHA: pl.SHA, WebURL: pl.WebURL, CreatedAt: pl.CreatedAt, UpdatedAt: pl.UpdatedAt}
	var jobs []struct {
		Job
	}
	q := url.Values{"per_page": {strconv.Itoa(MaxJobs + 1)}}
	if _, err := c.do(ctx, http.MethodGet, "/projects/"+pid(p)+"/pipelines/"+strconv.Itoa(pl.ID)+"/jobs", q, nil, &jobs); err != nil {
		return PipelineOut{}, err
	}
	for i, j := range jobs {
		if i >= MaxJobs {
			out.JobsTruncated = true
			break
		}
		out.Jobs = append(out.Jobs, j.Job)
	}
	return out, nil
}
