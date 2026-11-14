// Package gitlab implements the gitlab-mcp tools against the GitLab REST API
// v4. Every project a tool touches is resolved through the API and must live
// under the configured group; the GitLab token is read from a mounted file
// and only ever placed in the PRIVATE-TOKEN request header.
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/httpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/validate"
)

// Config is the server-side configuration. None of it comes from tool args.
type Config struct {
	// BaseURL of the GitLab instance, e.g. https://gitlab.example.com.
	BaseURL string
	// AllowedGroup is the full path of the group all Workbench projects live
	// in, e.g. "workbench/apps". Tools refuse projects outside it.
	AllowedGroup string
	// TemplateGroup is the full path of the group holding scaffold templates.
	TemplateGroup string
	// TokenFile is the path of the mounted Secret file holding the token.
	TokenFile string
}

// Validate checks the configuration.
func (c Config) Validate() error {
	if c.BaseURL == "" || c.AllowedGroup == "" || c.TemplateGroup == "" || c.TokenFile == "" {
		return errors.New("gitlab: base URL, allowed group, template group and token file are required")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("gitlab: invalid base URL %q", c.BaseURL)
	}
	for _, g := range []string{c.AllowedGroup, c.TemplateGroup} {
		if err := validNamespace(g); err != nil {
			return fmt.Errorf("gitlab: invalid group %q", g)
		}
	}
	return nil
}

// TokenFunc returns the current GitLab token.
type TokenFunc func() (string, error)

// FileToken reads the token from path on every call so Secret rotation is
// picked up without a restart.
func FileToken(path string) TokenFunc {
	return func() (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading GitLab token file: %w", err)
		}
		t := strings.TrimSpace(string(b))
		if t == "" {
			return "", errors.New("GitLab token file is empty")
		}
		return t, nil
	}
}

// Client is a thin GitLab v4 client.
type Client struct {
	cfg   Config
	http  httpx.Doer
	token TokenFunc
}

// NewClient builds a client.
func NewClient(cfg Config, d httpx.Doer, token TokenFunc) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Client{cfg: cfg, http: d, token: token}
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) (http.Header, error) {
	tok, err := c.token()
	if err != nil {
		return nil, err
	}
	u := c.cfg.BaseURL + "/api/v4" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return httpx.Do(ctx, c.http, httpx.Request{
		Method: method,
		URL:    u,
		Header: http.Header{"Private-Token": []string{tok}},
		Body:   body,
	}, out)
}

var segment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,254}$`)

func validNamespace(p string) error {
	parts := strings.Split(p, "/")
	if len(parts) > 20 {
		return errors.New("too deep")
	}
	for _, s := range parts {
		if !segment.MatchString(s) || strings.HasSuffix(s, ".git") || strings.HasSuffix(s, ".atom") {
			return fmt.Errorf("invalid segment %q", s)
		}
	}
	return nil
}

// projectPath turns a tool's "project" argument (a bare name or a full path)
// into a full path inside the allowed group.
func (c *Client) projectPath(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", validate.Errorf("project", "is required")
	}
	full := arg
	if !strings.Contains(arg, "/") {
		full = c.cfg.AllowedGroup + "/" + arg
	}
	if err := validNamespace(full); err != nil {
		return "", validate.Errorf("project", "%q is not a valid project path", validate.Clip(arg, 80))
	}
	if !c.inGroup(full) {
		return "", validate.Errorf("project", "%q is outside the Workbench group %q", validate.Clip(arg, 80), c.cfg.AllowedGroup)
	}
	return full, nil
}

func (c *Client) inGroup(path string) bool {
	return strings.HasPrefix(strings.ToLower(path), strings.ToLower(c.cfg.AllowedGroup)+"/")
}

type apiProject struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
	Visibility        string `json:"visibility"`
	ImportStatus      string `json:"import_status"`
	LastActivityAt    string `json:"last_activity_at"`
	ForkedFromProject *struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"forked_from_project"`
}

// Project is the tool-facing project summary.
type Project struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Path           string `json:"path" jsonschema:"full path, e.g. workbench/apps/my-app"`
	WebURL         string `json:"web_url"`
	DefaultBranch  string `json:"default_branch,omitempty"`
	Visibility     string `json:"visibility,omitempty"`
	ImportStatus   string `json:"import_status,omitempty" jsonschema:"for newly created projects: scheduled, started, finished or failed"`
	ForkedFrom     string `json:"forked_from,omitempty"`
	LastActivityAt string `json:"last_activity_at,omitempty"`
}

func (p apiProject) summary() Project {
	out := Project{ID: p.ID, Name: p.Name, Path: p.PathWithNamespace, WebURL: p.WebURL,
		DefaultBranch: p.DefaultBranch, Visibility: p.Visibility, ImportStatus: p.ImportStatus,
		LastActivityAt: p.LastActivityAt}
	if p.ForkedFromProject != nil {
		out.ForkedFrom = p.ForkedFromProject.PathWithNamespace
	}
	return out
}

// getProject fetches a project by full path and re-checks that its canonical
// path (GitLab follows renames/redirects) is still inside the allowed group.
func (c *Client) getProject(ctx context.Context, full string) (apiProject, error) {
	var p apiProject
	if _, err := c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(full), nil, nil, &p); err != nil {
		if httpx.IsNotFound(err) {
			return p, fmt.Errorf("project %q not found (or the Workbench bot has no access)", full)
		}
		return p, err
	}
	if !c.inGroup(p.PathWithNamespace) {
		return p, fmt.Errorf("project %q resolves to %q, outside the Workbench group", full, p.PathWithNamespace)
	}
	return p, nil
}

// resolve validates a project argument and returns the project.
func (c *Client) resolve(ctx context.Context, arg string) (apiProject, error) {
	full, err := c.projectPath(arg)
	if err != nil {
		return apiProject{}, err
	}
	return c.getProject(ctx, full)
}

func pid(p apiProject) string { return strconv.Itoa(p.ID) }

func nextPage(h http.Header) int {
	n, _ := strconv.Atoi(h.Get("X-Next-Page"))
	return n
}
