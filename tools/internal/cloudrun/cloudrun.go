// Package cloudrun implements the cloudrun-mcp tools against the Cloud Run
// Admin API v2 and Cloud Logging v2.
//
// Preview-only by construction: the project and region come exclusively from
// server configuration, every service name is forced into the "wb-preview-"
// namespace, and there is no production deploy tool. Production is reached
// only through Workbench's approval-gated promotion, which runs elsewhere
// with credentials this server never has.
package cloudrun

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/httpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/validate"
)

// ServicePrefix is prepended to every service this server manages.
const ServicePrefix = "wb-preview-"

// Config is server-side configuration; none of it comes from tool args.
type Config struct {
	// PreviewProject is the only project this server touches.
	PreviewProject string
	// Region of the preview services, e.g. europe-west1.
	Region string
	// ProductionProjects are refused as PreviewProject (misconfiguration guard).
	ProductionProjects []string
	// ImagePrefix: deployable images must start with it, e.g.
	// "europe-west1-docker.pkg.dev/wb-builds/builds/".
	ImagePrefix string
	// RuntimeServiceAccount for preview revisions (optional; Cloud Run default otherwise).
	RuntimeServiceAccount string
	// MaxInstances caps max_instances per preview service.
	MaxInstances int
	// AllowPublicInvoker permits allUsers/allAuthenticatedUsers invoker grants.
	AllowPublicInvoker bool
	// InvokerDomains, if set, restricts invoker members to these email domains
	// (the preview project's service-account domain is always allowed).
	InvokerDomains []string
	// RunBaseURL and LoggingBaseURL default to the Google endpoints.
	RunBaseURL     string
	LoggingBaseURL string
	// DeployWait bounds how long deploy waits for the rollout.
	DeployWait time.Duration
	// PollInterval between operation polls.
	PollInterval time.Duration
}

var (
	projectRe = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	regionRe  = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+$`)
)

// Validate checks the configuration and fills defaults.
func (c *Config) Validate() error {
	if !projectRe.MatchString(c.PreviewProject) {
		return fmt.Errorf("cloudrun: invalid preview project %q", c.PreviewProject)
	}
	if !regionRe.MatchString(c.Region) {
		return fmt.Errorf("cloudrun: invalid region %q", c.Region)
	}
	if slices.Contains(c.ProductionProjects, c.PreviewProject) {
		return fmt.Errorf("cloudrun: preview project %q is listed as a production project; refusing to start", c.PreviewProject)
	}
	if c.ImagePrefix == "" || !strings.HasSuffix(c.ImagePrefix, "/") || !strings.Contains(c.ImagePrefix, ".") {
		return fmt.Errorf("cloudrun: image prefix must be a registry repository path ending in '/', got %q", c.ImagePrefix)
	}
	if c.MaxInstances <= 0 {
		c.MaxInstances = 2
	}
	if c.RunBaseURL == "" {
		c.RunBaseURL = "https://run.googleapis.com"
	}
	if c.LoggingBaseURL == "" {
		c.LoggingBaseURL = "https://logging.googleapis.com"
	}
	if c.DeployWait <= 0 {
		c.DeployWait = 3 * time.Minute
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	c.RunBaseURL = strings.TrimRight(c.RunBaseURL, "/")
	c.LoggingBaseURL = strings.TrimRight(c.LoggingBaseURL, "/")
	return nil
}

// Client calls Cloud Run and Cloud Logging.
type Client struct {
	cfg  Config
	http httpx.Doer
}

// NewClient builds a client. cfg must have been validated.
func NewClient(cfg Config, d httpx.Doer) *Client { return &Client{cfg: cfg, http: d} }

var serviceSuffixRe = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

// ServiceName normalizes a tool's "service" argument into a full preview
// service name. "my-app" and "wb-preview-my-app" are equivalent. Anything
// that is not a plain Cloud Run service name is rejected.
func ServiceName(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", validate.Errorf("service", "is required")
	}
	suffix := strings.TrimPrefix(arg, ServicePrefix)
	maxSuffix := 49 - len(ServicePrefix)
	if len(suffix) > maxSuffix || !serviceSuffixRe.MatchString(suffix) {
		return "", validate.Errorf("service", "%q is invalid: use lowercase letters, digits and hyphens, starting with a letter, at most %d characters (the %q prefix is added automatically)",
			validate.Clip(arg, 80), maxSuffix, ServicePrefix)
	}
	return ServicePrefix + suffix, nil
}

func (c *Client) parent() string {
	return "projects/" + c.cfg.PreviewProject + "/locations/" + c.cfg.Region
}

func (c *Client) servicePath(name string) string {
	return c.parent() + "/services/" + name
}

func (c *Client) run(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := c.cfg.RunBaseURL + "/v2/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	_, err := httpx.Do(ctx, c.http, httpx.Request{Method: method, URL: u, Body: body}, out)
	return err
}

// operation is google.longrunning.Operation.
type operation struct {
	Name  string `json:"name"`
	Done  bool   `json:"done"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// operationPath returns an operation name if it is within the preview project.
func (c *Client) operationPath(name string) (string, error) {
	if !strings.HasPrefix(name, c.parent()+"/operations/") {
		return "", fmt.Errorf("unexpected operation name %q", name)
	}
	return name, nil
}

// wait polls op until done, the deadline, or ctx expiry. It returns the last
// operation state; done=false means it is still running.
func (c *Client) wait(ctx context.Context, op operation) (operation, error) {
	if op.Done || op.Name == "" {
		return op, nil
	}
	path, err := c.operationPath(op.Name)
	if err != nil {
		return op, err
	}
	deadline := time.Now().Add(c.cfg.DeployWait)
	for !op.Done && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return op, nil
		case <-time.After(c.cfg.PollInterval):
		}
		var next operation
		if err := c.run(ctx, http.MethodGet, path, nil, nil, &next); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return op, nil
			}
			return op, err
		}
		op = next
	}
	return op, nil
}

// ---- API shapes (subset of run.googleapis.com/v2 Service and Revision) ----

type condition struct {
	Type    string `json:"type"`
	State   string `json:"state"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
}

type container struct {
	Image string `json:"image"`
}

type apiService struct {
	Name                  string            `json:"name"`
	URI                   string            `json:"uri"`
	Labels                map[string]string `json:"labels"`
	UpdateTime            string            `json:"updateTime"`
	Reconciling           bool              `json:"reconciling"`
	LatestReadyRevision   string            `json:"latestReadyRevision"`
	LatestCreatedRevision string            `json:"latestCreatedRevision"`
	TerminalCondition     *condition        `json:"terminalCondition"`
	Template              struct {
		Containers []container `json:"containers"`
	} `json:"template"`
	TrafficStatuses []struct {
		Type     string `json:"type"`
		Revision string `json:"revision"`
		Percent  int    `json:"percent"`
	} `json:"trafficStatuses"`
}

type apiRevision struct {
	Name        string            `json:"name"`
	CreateTime  string            `json:"createTime"`
	Labels      map[string]string `json:"labels"`
	Reconciling bool              `json:"reconciling"`
	Containers  []container       `json:"containers"`
	Conditions  []condition       `json:"conditions"`
}

func lastSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}
