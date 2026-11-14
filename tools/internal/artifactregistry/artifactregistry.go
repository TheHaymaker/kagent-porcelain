// Package artifactregistry implements the artifactregistry-mcp tools: a
// read-only view of the single Workbench builds repository (Artifact Registry
// API v1). There is deliberately no delete tool.
package artifactregistry

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/httpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/validate"
)

// Config is server-side configuration.
type Config struct {
	Project    string // project holding the builds repository
	Location   string // e.g. europe-west1
	Repository string // e.g. builds
	BaseURL    string // default https://artifactregistry.googleapis.com
}

var (
	projectRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	locationRe = regexp.MustCompile(`^[a-z]+(-[a-z]+[0-9]+)?$`)
	repoRe     = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	imageRe    = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*$`)
	tagRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

// Validate checks configuration and fills defaults.
func (c *Config) Validate() error {
	if !projectRe.MatchString(c.Project) || !locationRe.MatchString(c.Location) || !repoRe.MatchString(c.Repository) {
		return fmt.Errorf("artifactregistry: invalid project/location/repository %q/%q/%q", c.Project, c.Location, c.Repository)
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://artifactregistry.googleapis.com"
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return nil
}

// ImagePrefix is the Docker reference prefix of the repository, e.g.
// "europe-west1-docker.pkg.dev/wb-builds/builds/".
func (c Config) ImagePrefix() string {
	return c.Location + "-docker.pkg.dev/" + c.Project + "/" + c.Repository + "/"
}

// Client calls Artifact Registry.
type Client struct {
	cfg  Config
	http httpx.Doer
}

// NewClient builds a client. cfg must have been validated.
func NewClient(cfg Config, d httpx.Doer) *Client { return &Client{cfg: cfg, http: d} }

func (c *Client) repoPath() string {
	return "projects/" + c.cfg.Project + "/locations/" + c.cfg.Location + "/repositories/" + c.cfg.Repository
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.cfg.BaseURL + "/v1/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	_, err := httpx.Do(ctx, c.http, httpx.Request{Method: http.MethodGet, URL: u}, out)
	return err
}

// imageName normalizes "my-app" or "<prefix>my-app" into the package name.
func (c *Client) imageName(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if strings.Contains(arg, ".") && strings.Contains(arg, "/") && strings.Contains(arg, "docker.pkg.dev") {
		if !strings.HasPrefix(arg, c.cfg.ImagePrefix()) {
			return "", validate.Errorf("image", "only images in %s can be inspected", c.cfg.ImagePrefix())
		}
		arg = strings.TrimPrefix(arg, c.cfg.ImagePrefix())
	}
	if i := strings.IndexAny(arg, ":@"); i >= 0 {
		return "", validate.Errorf("image", "pass the image name without a tag or digest")
	}
	if len(arg) > 200 {
		return "", validate.Errorf("image", "is too long")
	}
	if err := validate.Match("image", arg, imageRe, "a Docker image name such as my-app or team/my-app"); err != nil {
		return "", err
	}
	return arg, nil
}

func (c *Client) packagePath(image string) string {
	return c.repoPath() + "/packages/" + url.PathEscape(image)
}

// Instructions is advertised to MCP clients.
const Instructions = `Read-only Artifact Registry tools for the Workbench builds repository. Use artifactregistry_get_digest to turn a tag into a digest-pinned image reference for cloudrun_deploy_preview.`

// Register adds the tools.
func Register(s *mcpx.Server, c *Client) {
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "artifactregistry_list_images",
		Description: "List images in the Workbench builds repository (" + c.cfg.ImagePrefix() + "), most recently updated first, paginated.",
		Annotations: mcpx.ReadOnly("List build images", false),
	}, c.listImages)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "artifactregistry_list_tags",
		Description: "List tags of an image in the builds repository with the digest each points to, paginated.",
		Annotations: mcpx.ReadOnly("List image tags", false),
	}, c.listTags)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "artifactregistry_get_digest",
		Description: "Resolve an image tag to its digest and return the digest-pinned reference to deploy.",
		Annotations: mcpx.ReadOnly("Get image digest", false),
	}, c.getDigest)
}

// PageIn is shared pagination input.
type PageIn struct {
	PageSize  int    `json:"page_size,omitempty" jsonschema:"default 50, max 200"`
	PageToken string `json:"page_token,omitempty" jsonschema:"next_page_token from a previous call"`
}

func pageQuery(in PageIn) (url.Values, error) {
	if err := validate.PageToken("page_token", in.PageToken); err != nil {
		return nil, err
	}
	q := url.Values{"pageSize": {strconv.Itoa(validate.Bounded(in.PageSize, 50, 200))}}
	if in.PageToken != "" {
		q.Set("pageToken", in.PageToken)
	}
	return q, nil
}

// Image is one image (package) in the repository.
type Image struct {
	Name       string `json:"name"`
	URI        string `json:"uri" jsonschema:"image reference without tag"`
	CreateTime string `json:"create_time,omitempty"`
	UpdateTime string `json:"update_time,omitempty"`
}

// ListImagesOut is the result of artifactregistry_list_images.
type ListImagesOut struct {
	Images        []Image `json:"images"`
	NextPageToken string  `json:"next_page_token,omitempty"`
}

func (c *Client) listImages(ctx context.Context, _ mcpx.Call, in PageIn) (ListImagesOut, error) {
	q, err := pageQuery(in)
	if err != nil {
		return ListImagesOut{}, err
	}
	q.Set("orderBy", "update_time desc")
	var res struct {
		Packages []struct {
			Name       string `json:"name"`
			CreateTime string `json:"createTime"`
			UpdateTime string `json:"updateTime"`
		} `json:"packages"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := c.get(ctx, c.repoPath()+"/packages", q, &res); err != nil {
		return ListImagesOut{}, err
	}
	out := ListImagesOut{Images: make([]Image, 0, len(res.Packages)), NextPageToken: res.NextPageToken}
	for _, p := range res.Packages {
		name := p.Name[strings.LastIndex(p.Name, "/packages/")+len("/packages/"):]
		if dec, err := url.PathUnescape(name); err == nil {
			name = dec
		}
		out.Images = append(out.Images, Image{Name: name, URI: c.cfg.ImagePrefix() + name, CreateTime: p.CreateTime, UpdateTime: p.UpdateTime})
	}
	return out, nil
}

// ListTagsIn are the arguments of artifactregistry_list_tags.
type ListTagsIn struct {
	Image string `json:"image" jsonschema:"image name (e.g. my-app) or full reference without tag"`
	PageIn
}

// Tag is a tag and the digest it points to.
type Tag struct {
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
	Ref    string `json:"ref" jsonschema:"digest-pinned image reference"`
}

// ListTagsOut is the result of artifactregistry_list_tags.
type ListTagsOut struct {
	Image         string `json:"image"`
	Tags          []Tag  `json:"tags"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

type apiTag struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (c *Client) tag(image string, t apiTag) Tag {
	digest := t.Version[strings.LastIndex(t.Version, "/")+1:]
	if dec, err := url.PathUnescape(digest); err == nil {
		digest = dec
	}
	return Tag{Tag: t.Name[strings.LastIndex(t.Name, "/")+1:], Digest: digest, Ref: c.cfg.ImagePrefix() + image + "@" + digest}
}

func (c *Client) listTags(ctx context.Context, _ mcpx.Call, in ListTagsIn) (ListTagsOut, error) {
	image, err := c.imageName(in.Image)
	if err != nil {
		return ListTagsOut{}, err
	}
	q, err := pageQuery(in.PageIn)
	if err != nil {
		return ListTagsOut{}, err
	}
	var res struct {
		Tags          []apiTag `json:"tags"`
		NextPageToken string   `json:"nextPageToken"`
	}
	if err := c.get(ctx, c.packagePath(image)+"/tags", q, &res); err != nil {
		if httpx.IsNotFound(err) {
			return ListTagsOut{}, fmt.Errorf("image %q not found in the builds repository", image)
		}
		return ListTagsOut{}, err
	}
	out := ListTagsOut{Image: c.cfg.ImagePrefix() + image, Tags: make([]Tag, 0, len(res.Tags)), NextPageToken: res.NextPageToken}
	for _, t := range res.Tags {
		out.Tags = append(out.Tags, c.tag(image, t))
	}
	return out, nil
}

// GetDigestIn are the arguments of artifactregistry_get_digest.
type GetDigestIn struct {
	Image string `json:"image" jsonschema:"image name (e.g. my-app) or full reference without tag"`
	Tag   string `json:"tag" jsonschema:"tag, e.g. a commit SHA or 'latest'"`
}

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (c *Client) getDigest(ctx context.Context, _ mcpx.Call, in GetDigestIn) (Tag, error) {
	image, err := c.imageName(in.Image)
	if err != nil {
		return Tag{}, err
	}
	if err := validate.Match("tag", in.Tag, tagRe, "a Docker tag (letters, digits, '_', '.', '-'; at most 128)"); err != nil {
		return Tag{}, err
	}
	var t apiTag
	if err := c.get(ctx, c.packagePath(image)+"/tags/"+url.PathEscape(in.Tag), nil, &t); err != nil {
		if httpx.IsNotFound(err) {
			return Tag{}, fmt.Errorf("tag %q of image %q not found", in.Tag, image)
		}
		return Tag{}, err
	}
	out := c.tag(image, t)
	if !digestRe.MatchString(out.Digest) {
		return Tag{}, fmt.Errorf("tag %q points to an unexpected version %q", in.Tag, validate.Clip(out.Digest, 100))
	}
	return out, nil
}
