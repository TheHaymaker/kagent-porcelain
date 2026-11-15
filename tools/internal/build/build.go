// Package build implements the build-mcp tools: build a Workbench app's
// container image from its GitLab project with Cloud Build, and report the
// digest-pinned reference to deploy. It stands in for a GitLab CI pipeline
// with Google credentials.
//
// The source is the project's tree at one commit, fetched through the GitLab
// client (so only projects in the allowed group can be built), uploaded to a
// staging bucket, and built by Cloud Build as a plain `docker build` of the
// repository root. The image goes to the builds repository only, tagged with
// the commit.
package build

import (
	"bytes"
	"context"
	"fmt"
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
const Instructions = `Build a Workbench app's container image from its GitLab project with Cloud Build. Call build_start_image with the project and ref, then poll build_get_status until it reports SUCCESS and returns image_ref, the digest-pinned reference cloudrun_deploy_preview needs. Builds take one to five minutes.`

// Register adds the tools.
func Register(s *mcpx.Server, c *Client) {
	mcpx.AddTool(s, &mcp.Tool{
		Name: "build_start_image",
		Description: "Start a Cloud Build of a Workbench project's Dockerfile at a ref (default: the default branch). " +
			"Returns a build_id at once; poll build_get_status. The image goes to " + c.cfg.ImagePrefix() + "<project>:<commit>.",
		Annotations: mcpx.Mutating("Build app image", false, false, false),
	}, c.start)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "build_get_status",
		Description: "Status of a build started with build_start_image; on SUCCESS returns image_ref (digest-pinned) to deploy.",
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
		"tags":           []string{"workbench", "wb-" + name},
	}
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
		return StartOut{}, fmt.Errorf("starting the build: %w", err)
	}
	if op.Metadata.Build.ID == "" {
		return StartOut{}, fmt.Errorf("Cloud Build returned no build id")
	}
	return StartOut{BuildID: op.Metadata.Build.ID, Project: snap.Project, Commit: snap.SHA, Image: image, LogURL: op.Metadata.Build.LogURL}, nil
}

func (c *Client) upload(ctx context.Context, object string, data []byte) error {
	u := c.cfg.StorageURL + "/upload/storage/v1/b/" + url.PathEscape(c.cfg.Bucket) + "/o?uploadType=media&name=" + url.QueryEscape(object)
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
	LogURL   string `json:"log_url,omitempty"`
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
		Results        struct {
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
	ours := false
	for _, t := range b.Tags {
		if t == "workbench" {
			ours = true
		}
	}
	if !ours {
		return StatusOut{}, fmt.Errorf("build %s was not started by Workbench", in.BuildID)
	}
	out := StatusOut{BuildID: b.ID, Status: b.Status, LogURL: b.LogURL}
	switch b.Status {
	case "SUCCESS":
		out.Done = true
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
		out.Detail = validate.Clip(b.StatusDetail, 500)
	}
	return out, nil
}
