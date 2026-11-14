package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

// CloudRunConfig says where production apps run.
type CloudRunConfig struct {
	Project string `json:"project" yaml:"project"`
	Region  string `json:"region" yaml:"region"`
	// ServiceAccountTemplate names the runtime identity, e.g.
	// "wb-{app}@prod-project.iam.gserviceaccount.com". {app} is replaced.
	ServiceAccountTemplate string `json:"serviceAccountTemplate" yaml:"serviceAccountTemplate"`
	// Ingress: INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER keeps apps behind IAP.
	Ingress string `json:"ingress" yaml:"ingress"`
}

// CloudRun deploys to Cloud Run with the Admin API v2. Client must carry
// the Workbench server's production credentials (Workload Identity).
type CloudRun struct {
	Config   CloudRunConfig
	Client   *http.Client
	Endpoint string
	// PollInterval and PollTimeout bound waiting for the revision to serve.
	PollInterval time.Duration
	PollTimeout  time.Duration
}

// ServiceName is the production Cloud Run service for an app.
func ServiceName(app store.App) string { return "wb-" + app.Name }

type crService struct {
	Name        string            `json:"name,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Ingress     string            `json:"ingress,omitempty"`
	Template    crTemplate        `json:"template"`
	URI         string            `json:"uri,omitempty"`
	Reconciling bool              `json:"reconciling,omitempty"`
	Conditions  []struct {
		Type    string `json:"type"`
		State   string `json:"state"`
		Message string `json:"message"`
	} `json:"terminalCondition,omitempty"`
}

type crTemplate struct {
	Labels         map[string]string `json:"labels,omitempty"`
	ServiceAccount string            `json:"serviceAccount,omitempty"`
	Containers     []struct {
		Image string `json:"image"`
	} `json:"containers"`
}

// DeployProduction implements Deployer.
func (c *CloudRun) DeployProduction(ctx context.Context, app store.App, image, revision, approvalID string) (string, error) {
	if image == "" {
		return "", fmt.Errorf("revision %s has no image recorded", revision)
	}
	if !strings.Contains(image, "@sha256:") {
		return "", fmt.Errorf("production deploys pin an image digest; %q is a tag", image)
	}
	base := c.Endpoint
	if base == "" {
		base = "https://run.googleapis.com"
	}
	parent := fmt.Sprintf("%s/v2/projects/%s/locations/%s/services", base, c.Config.Project, c.Config.Region)
	svcName := ServiceName(app)
	labels := map[string]string{"wb-app": app.ID, "wb-approval": strings.ToLower(approvalID), "wb-managed": "true"}
	svc := crService{Labels: labels, Ingress: c.Config.Ingress, Template: crTemplate{
		Labels:         map[string]string{"wb-revision": labelSafe(revision)},
		ServiceAccount: strings.ReplaceAll(c.Config.ServiceAccountTemplate, "{app}", app.Name),
	}}
	svc.Template.Containers = []struct {
		Image string `json:"image"`
	}{{Image: image}}

	exists, err := c.do(ctx, http.MethodGet, parent+"/"+svcName, nil, nil)
	if err != nil && !isNotFound(err) {
		return "", err
	}
	if exists && err == nil {
		_, err = c.do(ctx, http.MethodPatch, parent+"/"+svcName, svc, nil)
	} else {
		_, err = c.do(ctx, http.MethodPost, parent+"?serviceId="+svcName, svc, nil)
	}
	if err != nil {
		return "", err
	}
	return c.wait(ctx, parent+"/"+svcName)
}

func (c *CloudRun) wait(ctx context.Context, url string) (string, error) {
	interval, timeout := c.PollInterval, c.PollTimeout
	if interval == 0 {
		interval = 3 * time.Second
	}
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		var got crService
		if _, err := c.do(ctx, http.MethodGet, url, nil, &got); err != nil {
			return "", err
		}
		if !got.Reconciling && got.URI != "" {
			return got.URI, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("cloud run service did not become ready within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
	}
}

type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string { return fmt.Sprintf("cloud run: %d %s", e.status, e.body) }

func isNotFound(err error) bool {
	he, ok := err.(*httpError)
	return ok && he.status == http.StatusNotFound
}

func (c *CloudRun) do(ctx context.Context, method, url string, in, out any) (bool, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return false, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return false, &httpError{status: resp.StatusCode, body: string(b)}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return true, err
		}
	}
	return true, nil
}

func labelSafe(s string) string {
	s = strings.ToLower(s)
	b := []byte(s)
	for i, ch := range b {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			b[i] = '-'
		}
	}
	if len(b) > 63 {
		b = b[:63]
	}
	return string(b)
}

// LogDeployer records what it would deploy and returns a fake URL. It exists
// for local development and the phase 1 walking skeleton; production config
// must select CloudRun.
type LogDeployer struct{ Domain string }

// DeployProduction implements Deployer.
func (l LogDeployer) DeployProduction(_ context.Context, app store.App, _, _, _ string) (string, error) {
	d := l.Domain
	if d == "" {
		d = "apps.example.internal"
	}
	return "https://" + app.Name + "." + d, nil
}
