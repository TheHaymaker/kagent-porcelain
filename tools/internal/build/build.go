// Package build implements the build-mcp tools: build a Workbench app's
// container image from its GitLab project with Cloud Build, and report the
// digest-pinned reference to deploy; and run the app's lint and tests the
// same way. It stands in for a GitLab CI pipeline with Google credentials.
//
// The source is the project's tree at one commit, fetched through the GitLab
// client (so only projects in the allowed group can be built), uploaded to a
// staging bucket, and built by Cloud Build as a plain `docker build` of the
// repository root. The image goes to the builds repository only, tagged with
// the commit.
package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/gitlab"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/httpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/validate"
)

// Config is server-side configuration; none of it comes from tool arguments.
type Config struct {
	Project        string // project that runs Cloud Build and holds the builds repository
	Location       string // Artifact Registry location, e.g. us-central1
	Repository     string // builds repository id
	Bucket         string // staging bucket for build sources
	ServiceAccount string // email of the service account builds run as
	CloudBuildURL  string // default https://cloudbuild.googleapis.com
	StorageURL     string // default https://storage.googleapis.com
	LoggingURL     string // default https://logging.googleapis.com
}

var (
	projectRe = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	bucketRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$`)
	buildIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	imageRe   = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)
)

// Validate checks configuration and fills defaults.
func (c *Config) Validate() error {
	if !projectRe.MatchString(c.Project) || c.Location == "" || c.Repository == "" || !bucketRe.MatchString(c.Bucket) ||
		!strings.Contains(c.ServiceAccount, "@") {
		return fmt.Errorf("build: project, location, repository, bucket and service account are required")
	}
	if c.CloudBuildURL == "" {
		c.CloudBuildURL = "https://cloudbuild.googleapis.com"
	}
	if c.StorageURL == "" {
		c.StorageURL = "https://storage.googleapis.com"
	}
	if c.LoggingURL == "" {
		c.LoggingURL = "https://logging.googleapis.com"
	}
	c.LoggingURL = strings.TrimRight(c.LoggingURL, "/")
	c.CloudBuildURL = strings.TrimRight(c.CloudBuildURL, "/")
	c.StorageURL = strings.TrimRight(c.StorageURL, "/")
	return nil
}

// ImagePrefix is the Docker reference prefix of the builds repository.
func (c Config) ImagePrefix() string {
	return c.Location + "-docker.pkg.dev/" + c.Project + "/" + c.Repository + "/"
}

// Source fetches a project's tree; gitlab.Client implements it.
type Source interface {
	Source(ctx context.Context, project, ref string) (gitlab.Snapshot, error)
}

// Client builds images.
type Client struct {
	cfg  Config
	src  Source
	http httpx.Doer
}

// NewClient builds a client. cfg must have been validated.
func NewClient(cfg Config, src Source, d httpx.Doer) *Client {
	return &Client{cfg: cfg, src: src, http: d}
}

// Instructions is advertised to MCP clients.
const Instructions = `Build a Workbench app's container image from its GitLab project with Cloud Build, or run its checks. Call build_start_image with the project and ref, then poll build_get_status until it reports SUCCESS and returns image_ref, the digest-pinned reference cloudrun_deploy_preview needs. Builds take one to five minutes. build_run_checks runs the app's lint and tests at a commit; poll build_get_status the same way and read output for the results.`

// Register adds the tools.
func Register(s *mcpx.Server, c *Client) {
	mcpx.AddTool(s, &mcp.Tool{
		Name: "build_start_image",
		Description: "Start a Cloud Build of a Workbench project's Dockerfile at a ref (default: the default branch). " +
			"Returns a build_id at once; poll build_get_status. The image goes to " + c.cfg.ImagePrefix() + "<project>:<commit>.",
		Annotations: mcpx.Mutating("Build app image", false, false, false),
	}, c.start)
	mcpx.AddTool(s, &mcp.Tool{
		Name: "build_run_checks",
		Description: "Run a Workbench project's lint, format check and tests at a ref (default: the default branch) in Cloud Build, " +
			"the same checks as its CI. Returns a build_id at once; poll build_get_status, whose output holds the end of the log.",
		Annotations: mcpx.Mutating("Run app checks", false, false, false),
	}, c.checks)
	mcpx.AddTool(s, &mcp.Tool{
		Name: "build_get_status",
		Description: "Status of a build started with build_start_image or build_run_checks. An image build's SUCCESS returns image_ref " +
			"(digest-pinned) to deploy; a finished check run, or any failed build, returns output: the end of its log.",
		Annotations: mcpx.ReadOnly("Get build status", false),
	}, c.status)
}

// StartIn is build_start_image's input.
type StartIn struct {
	Project string `json:"project" jsonschema:"the app's GitLab project: a bare name or its full path in the Workbench apps group"`
	Ref     string `json:"ref,omitempty" jsonschema:"branch, tag or commit SHA; default the project's default branch"`
}

// StartOut is build_start_image's output.
type StartOut struct {
	BuildID string `json:"build_id"`
	Project string `json:"project"`
	Commit  string `json:"commit"`
	Image   string `json:"image" jsonschema:"tagged reference the build will push"`
	LogURL  string `json:"log_url,omitempty"`
}

func (c *Client) start(ctx context.Context, call mcpx.Call, in StartIn) (StartOut, error) {
	if err := validate.Text("project", in.Project, true, 255); err != nil {
		return StartOut{}, err
	}
	snap, err := c.src.Source(ctx, in.Project, strings.TrimSpace(in.Ref))
	if err != nil {
		return StartOut{}, err
	}
	name := strings.ToLower(snap.Name)
	if !imageRe.MatchString(name) {
		return StartOut{}, validate.Errorf("project", "%q cannot be used as an image name", validate.Clip(snap.Name, 80))
	}
	object := "sources/" + name + "/" + snap.SHA + ".tgz"
	if err := c.upload(ctx, object, snap.Tarball); err != nil {
		return StartOut{}, fmt.Errorf("uploading the source: %w", err)
	}
	image := c.cfg.ImagePrefix() + name + ":" + snap.SHA[:12]
	body := map[string]any{
		"source": map[string]any{"storageSource": map[string]any{"bucket": c.cfg.Bucket, "object": object}},
		"steps": []map[string]any{{
			"name": "gcr.io/cloud-builders/docker",
			"args": []string{"build", "--label", "dev.workbench.commit=" + snap.SHA, "-t", image, "."},
		}},
		"images":         []string{image},
		"serviceAccount": "projects/" + c.cfg.Project + "/serviceAccounts/" + c.cfg.ServiceAccount,
		"options":        map[string]any{"logging": "CLOUD_LOGGING_ONLY"},
		"timeout":        "1200s",
		"tags":           []string{"workbench", tagImage, "wb-" + name},
	}
	id, logURL, err := c.create(ctx, body)
	if err != nil {
		return StartOut{}, err
	}
	return StartOut{BuildID: id, Project: snap.Project, Commit: snap.SHA, Image: image, LogURL: logURL}, nil
}

// Build kinds, recorded as tags so build_get_status knows what to report.
const (
	tagImage  = "wb-kind-image"
	tagChecks = "wb-kind-checks"
)

func (c *Client) create(ctx context.Context, body map[string]any) (id, logURL string, err error) {
	var op struct {
		Metadata struct {
			Build struct {
				ID     string `json:"id"`
				LogURL string `json:"logUrl"`
			} `json:"build"`
		} `json:"metadata"`
	}
	u := c.cfg.CloudBuildURL + "/v1/projects/" + c.cfg.Project + "/builds"
	if _, err := httpx.Do(ctx, c.http, httpx.Request{Method: http.MethodPost, URL: u, Body: body}, &op); err != nil {
		return "", "", fmt.Errorf("starting the build: %w", err)
	}
	if op.Metadata.Build.ID == "" {
		return "", "", fmt.Errorf("Cloud Build returned no build id")
	}
	return op.Metadata.Build.ID, op.Metadata.Build.LogURL, nil
}

// Checks images: the same toolchains the scaffolds' CI uses.
const (
	uvImage   = "ghcr.io/astral-sh/uv:0.11.32-python3.12-trixie-slim"
	nodeImage = "node:22-bookworm-slim"
)

type step struct{ id, image, script string }

// checkSteps chooses the checks from the files at the repository root: a uv
// project (uv.lock) or an npm project (package-lock.json), as the scaffolds
// are. Each check is its own step so a failure names what failed.
func checkSteps(root map[string]bool) ([]step, error) {
	switch {
	case root["uv.lock"]:
		return []step{
			{"install", uvImage, "uv sync --frozen"},
			{"lint", uvImage, "uv run ruff check ."},
			{"format", uvImage, "uv run ruff format --check ."},
			{"tests", uvImage, "uv run pytest -q"},
		}, nil
	case root["package-lock.json"]:
		return []step{
			{"install", nodeImage, "npm ci --no-audit --no-fund"},
			{"lint", nodeImage, "npm run check"},
			{"tests", nodeImage, "npm test"},
		}, nil
	}
	return nil, validate.Errorf("project", "has neither uv.lock nor package-lock.json at its root, so its checks are unknown")
}

// rootFiles lists the regular files at the top of a snapshot's tarball.
func rootFiles(tgz []byte) (map[string]bool, error) {
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, fmt.Errorf("reading the source: %w", err)
	}
	tr := tar.NewReader(zr)
	out := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading the source: %w", err)
		}
		if h.Typeflag == tar.TypeReg && !strings.Contains(strings.TrimPrefix(h.Name, "./"), "/") {
			out[strings.TrimPrefix(h.Name, "./")] = true
		}
	}
}

// ChecksOut is build_run_checks's output.
type ChecksOut struct {
	BuildID string   `json:"build_id"`
	Project string   `json:"project"`
	Commit  string   `json:"commit"`
	Checks  []string `json:"checks" jsonschema:"the checks that run, in order"`
	LogURL  string   `json:"log_url,omitempty"`
}

func (c *Client) checks(ctx context.Context, call mcpx.Call, in StartIn) (ChecksOut, error) {
	if err := validate.Text("project", in.Project, true, 255); err != nil {
		return ChecksOut{}, err
	}
	snap, err := c.src.Source(ctx, in.Project, strings.TrimSpace(in.Ref))
	if err != nil {
		return ChecksOut{}, err
	}
	name := strings.ToLower(snap.Name)
	if !imageRe.MatchString(name) {
		return ChecksOut{}, validate.Errorf("project", "%q is not a usable name", validate.Clip(snap.Name, 80))
	}
	root, err := rootFiles(snap.Tarball)
	if err != nil {
		return ChecksOut{}, err
	}
	plan, err := checkSteps(root)
	if err != nil {
		return ChecksOut{}, err
	}
	object := "sources/" + name + "/" + snap.SHA + ".tgz"
	if err := c.upload(ctx, object, snap.Tarball); err != nil {
		return ChecksOut{}, fmt.Errorf("uploading the source: %w", err)
	}
	steps := make([]map[string]any, 0, len(plan))
	names := make([]string, 0, len(plan))
	for _, st := range plan {
		steps = append(steps, map[string]any{
			"id": st.id, "name": st.image, "entrypoint": "sh", "args": []string{"-c", st.script},
			// No Python downloads: the image's interpreter is the one CI uses.
			"env": []string{"UV_PYTHON_DOWNLOADS=never", "CI=true"},
		})
		names = append(names, st.id)
	}
	body := map[string]any{
		"source":         map[string]any{"storageSource": map[string]any{"bucket": c.cfg.Bucket, "object": object}},
		"steps":          steps,
		"serviceAccount": "projects/" + c.cfg.Project + "/serviceAccounts/" + c.cfg.ServiceAccount,
		"options":        map[string]any{"logging": "CLOUD_LOGGING_ONLY"},
		"timeout":        "900s",
		"tags":           []string{"workbench", tagChecks, "wb-" + name},
	}
	id, logURL, err := c.create(ctx, body)
	if err != nil {
		return ChecksOut{}, err
	}
	return ChecksOut{BuildID: id, Project: snap.Project, Commit: snap.SHA, Checks: names, LogURL: logURL}, nil
}

// upload stores a commit's source once. Objects are named by commit, so an
// object that already exists holds the same tree: ifGenerationMatch=0 makes
// the write create-only (the server may create objects, not overwrite them)
// and a 412 means it is already there.
func (c *Client) upload(ctx context.Context, object string, data []byte) error {
	u := c.cfg.StorageURL + "/upload/storage/v1/b/" + url.PathEscape(c.cfg.Bucket) + "/o?uploadType=media&ifGenerationMatch=0&name=" + url.QueryEscape(object)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// StatusIn is build_get_status's input.
type StatusIn struct {
	BuildID string `json:"build_id"`
}

// StatusOut is build_get_status's output.
type StatusOut struct {
	BuildID  string `json:"build_id"`
	Status   string `json:"status" jsonschema:"QUEUED, WORKING, SUCCESS, FAILURE, TIMEOUT, CANCELLED or INTERNAL_ERROR"`
	Done     bool   `json:"done"`
	ImageRef string `json:"image_ref,omitempty" jsonschema:"digest-pinned reference, on SUCCESS"`
	Detail   string `json:"detail,omitempty" jsonschema:"why a build failed"`
	// FailedStep names the step that failed: for checks, install, lint,
	// format or tests.
	FailedStep string `json:"failed_step,omitempty"`
	Output     string `json:"output,omitempty" jsonschema:"the end of the build's log: for a finished check run or any failed build"`
	LogURL     string `json:"log_url,omitempty"`
}

func (c *Client) status(ctx context.Context, call mcpx.Call, in StatusIn) (StatusOut, error) {
	if err := validate.Match("build_id", strings.TrimSpace(in.BuildID), buildIDRe, "a build id from build_start_image"); err != nil {
		return StatusOut{}, err
	}
	var b struct {
		ID             string   `json:"id"`
		Status         string   `json:"status"`
		StatusDetail   string   `json:"statusDetail"`
		LogURL         string   `json:"logUrl"`
		Tags           []string `json:"tags"`
		ServiceAccount string   `json:"serviceAccount"`
		FailureInfo    struct {
			Detail string `json:"detail"`
		} `json:"failureInfo"`
		Steps []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"steps"`
		Results struct {
			Images []struct {
				Name   string `json:"name"`
				Digest string `json:"digest"`
			} `json:"images"`
		} `json:"results"`
	}
	u := c.cfg.CloudBuildURL + "/v1/projects/" + c.cfg.Project + "/builds/" + strings.TrimSpace(in.BuildID)
	if _, err := httpx.Do(ctx, c.http, httpx.Request{Method: http.MethodGet, URL: u}, &b); err != nil {
		return StatusOut{}, err
	}
	// Only builds this server started are reported.
	ours, checks := false, false
	for _, t := range b.Tags {
		switch t {
		case "workbench":
			ours = true
		case tagChecks:
			checks = true
		}
	}
	if !ours {
		return StatusOut{}, fmt.Errorf("build %s was not started by Workbench", in.BuildID)
	}
	out := StatusOut{BuildID: b.ID, Status: b.Status, LogURL: b.LogURL}
	switch b.Status {
	case "SUCCESS":
		out.Done = true
		if checks {
			out.Output = c.logTail(ctx, b.ID, checksTail)
			break
		}
		for _, img := range b.Results.Images {
			if strings.HasPrefix(img.Name, c.cfg.ImagePrefix()) && strings.HasPrefix(img.Digest, "sha256:") {
				name := img.Name
				if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
					name = name[:i]
				}
				out.ImageRef = name + "@" + img.Digest
			}
		}
		if out.ImageRef == "" {
			return out, fmt.Errorf("build %s succeeded but pushed no image to the builds repository", b.ID)
		}
	case "FAILURE", "TIMEOUT", "CANCELLED", "INTERNAL_ERROR", "EXPIRED":
		out.Done = true
		detail := b.StatusDetail
		if b.FailureInfo.Detail != "" && !strings.Contains(detail, b.FailureInfo.Detail) {
			detail = strings.TrimSpace(detail + " " + b.FailureInfo.Detail)
		}
		out.Detail = validate.Clip(detail, 500)
		for _, st := range b.Steps {
			if st.Status == "FAILURE" || st.Status == "TIMEOUT" {
				out.FailedStep = st.ID
				break
			}
		}
		out.Output = c.logTail(ctx, b.ID, failureTail)
	}
	return out, nil
}

// Lines of log returned: enough for a test summary or a compiler error.
const (
	checksTail  = 60
	failureTail = 80
	maxOutput   = 12 << 10
)

// logTail returns the last n lines a build wrote to Cloud Logging, oldest
// first, or a note saying why it could not. Logs can lag the build's status
// by a few seconds; an empty tail says so rather than failing the call.
func (c *Client) logTail(ctx context.Context, buildID string, n int) string {
	body := map[string]any{
		"resourceNames": []string{"projects/" + c.cfg.Project},
		"filter":        `resource.type="build" AND resource.labels.build_id="` + buildID + `"`,
		"orderBy":       "timestamp desc",
		"pageSize":      n,
	}
	var res struct {
		Entries []struct {
			TextPayload string `json:"textPayload"`
		} `json:"entries"`
	}
	if _, err := httpx.Do(ctx, c.http, httpx.Request{Method: http.MethodPost, URL: c.cfg.LoggingURL + "/v2/entries:list", Body: body}, &res); err != nil {
		return "(the build log could not be read: " + validate.Clip(err.Error(), 200) + ")"
	}
	if len(res.Entries) == 0 {
		return "(no log lines yet; ask again in a few seconds)"
	}
	lines := make([]string, 0, len(res.Entries))
	for i := len(res.Entries) - 1; i >= 0; i-- {
		lines = append(lines, strings.TrimRight(res.Entries[i].TextPayload, "\n"))
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxOutput {
		out = "…" + out[len(out)-maxOutput:]
	}
	return out
}
