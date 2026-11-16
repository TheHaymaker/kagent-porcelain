package cloudrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/httpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/validate"
)

// Limits.
const (
	MaxEnvVars        = 50
	MaxEnvBytes       = 32 << 10
	MaxLogEntries     = 200
	MaxLogEntryBytes  = 2000
	MaxLogTotalBytes  = 64 << 10
	MaxLogSinceMinute = 7 * 24 * 60
	MaxRevisionsPage  = 50
	InvokerRole       = "roles/run.invoker"
)

// Label keys written on preview services.
const (
	LabelManaged   = "wb-managed"
	LabelRequester = "wb-requester"
)

// Instructions is advertised to MCP clients.
const Instructions = `Cloud Run tools for Workbench PREVIEW environments only. Services are always named "wb-preview-<name>" in the preview project; you cannot target another project.
There is no production deploy: production is reached only through Workbench's approval-gated promotion.
Deploy with an image digest from artifactregistry_get_digest. Creating a new preview service requires allow_create=true (it adds spend and needs approval).
Preview services require authentication. With IAP on, people open the URL in a browser after a Google sign-in: pass the requester as a viewer when deploying (viewers: ["user:<email>"]) and use cloudrun_share_preview to add others.`

// Register adds all Cloud Run tools to s.
func Register(s *mcpx.Server, c *Client) {
	mcpx.AddTool(s, &mcp.Tool{
		Name: "cloudrun_deploy_preview",
		Description: "Deploy an image (digest-pinned, from the Workbench builds repository) as a new revision of a preview service in the preview project. " +
			"Updating an existing preview service needs no flag; creating a new one requires allow_create=true (new spend, approval-gated). " +
			"Waits for the rollout unless wait=false.",
		Annotations: mcpx.Mutating("Deploy Cloud Run preview", true, false, false),
	}, c.deployPreview)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "cloudrun_get_service",
		Description: "Get a preview service's URL, readiness, current image, latest revisions and traffic.",
		Annotations: mcpx.ReadOnly("Get Cloud Run preview service", false),
	}, c.getService)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "cloudrun_list_revisions",
		Description: "List revisions of a preview service, newest first (paginated with page_token).",
		Annotations: mcpx.ReadOnly("List Cloud Run revisions", false),
	}, c.listRevisions)
	mcpx.AddTool(s, &mcp.Tool{
		Name: "cloudrun_read_logs",
		Description: fmt.Sprintf("Read recent Cloud Logging entries of a preview service, newest first (at most %d entries, each truncated to %d bytes).",
			MaxLogEntries, MaxLogEntryBytes),
		Annotations: mcpx.ReadOnly("Read Cloud Run preview logs", false),
	}, c.readLogs)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "cloudrun_delete_preview_service",
		Description: "Delete a preview service and all its revisions. Irreversible; approval-gated.",
		Annotations: mcpx.Mutating("Delete Cloud Run preview service", true, true, false),
	}, c.deleteService)
	mcpx.AddTool(s, &mcp.Tool{
		Name: "cloudrun_set_iam_invoker",
		Description: "Grant or revoke roles/run.invoker on a preview service for one member " +
			"(user:, group:, serviceAccount: or domain:). IAM change; approval-gated.",
		Annotations: mcpx.Mutating("Change Cloud Run preview invoker", true, true, false),
	}, c.setIAMInvoker)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "cloudrun_share_preview",
		Description: "Give a person, group or domain access to open a preview in a browser (they sign in with Google first; IAP), or remove it. Never public.",
		Annotations: mcpx.Mutating("Share test version", true, true, false),
	}, c.sharePreview)
}

// ---- deploy ----

// DeployIn are the arguments of cloudrun_deploy_preview.
type DeployIn struct {
	Service      string            `json:"service" jsonschema:"preview service name; the wb-preview- prefix is added if missing"`
	Image        string            `json:"image" jsonschema:"image reference pinned by digest (…@sha256:…) in the Workbench builds repository"`
	Env          map[string]string `json:"env,omitempty" jsonschema:"plain environment variables (no secrets)"`
	Port         int               `json:"port,omitempty" jsonschema:"container port (default 8080)"`
	Memory       string            `json:"memory,omitempty" jsonschema:"256Mi, 512Mi (default), 1Gi or 2Gi"`
	CPU          string            `json:"cpu,omitempty" jsonschema:"1 (default) or 2"`
	MaxInstances int               `json:"max_instances,omitempty" jsonschema:"upper bound on instances; capped by server policy"`
	AllowCreate  bool              `json:"allow_create,omitempty" jsonschema:"must be true to create a service that does not exist yet (new spend; approval-gated)"`
	Viewers      []string          `json:"viewers,omitempty" jsonschema:"people or groups who may open the test version in a browser, e.g. user:alice@example.com; give at least the requester (workbench_get_requester)"`
	Wait         *bool             `json:"wait,omitempty" jsonschema:"wait for the rollout to finish (default true)"`
}

// DeployOut is the result of cloudrun_deploy_preview.
type DeployOut struct {
	Service   string   `json:"service"`
	Project   string   `json:"project"`
	Region    string   `json:"region"`
	Created   bool     `json:"created" jsonschema:"true if this call created the service"`
	Done      bool     `json:"done" jsonschema:"false if the rollout is still in progress; poll cloudrun_get_service"`
	Operation string   `json:"operation,omitempty"`
	Status    *Service `json:"status,omitempty"`
	Viewers   []string `json:"viewers,omitempty" jsonschema:"members given browser access by this call"`
}

var (
	digestRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*@sha256:[0-9a-f]{64}$`)
	memories    = []string{"256Mi", "512Mi", "1Gi", "2Gi"}
	cpus        = []string{"1", "2"}
	reservedEnv = []string{"PORT", "K_SERVICE", "K_REVISION", "K_CONFIGURATION"}
)

func (c *Client) validateDeploy(in *DeployIn) (string, error) {
	if len(in.Viewers) > 0 && !c.cfg.IAP {
		return "", validate.Errorf("viewers", "browser access needs IAP, which is not enabled on this server")
	}
	if len(in.Viewers) > 10 {
		return "", validate.Errorf("viewers", "at most 10; share with a group instead")
	}
	for _, m := range in.Viewers {
		if err := c.checkViewer(m); err != nil {
			return "", err
		}
	}
	name, err := ServiceName(in.Service)
	if err != nil {
		return "", err
	}
	if in.Image == "" {
		return "", validate.Errorf("image", "is required")
	}
	if !strings.HasPrefix(in.Image, c.cfg.ImagePrefix) {
		return "", validate.Errorf("image", "must be in the Workbench builds repository %q", c.cfg.ImagePrefix)
	}
	if !digestRe.MatchString(in.Image) {
		return "", validate.Errorf("image", "must be pinned by digest (…@sha256:<64 hex>); use artifactregistry_get_digest")
	}
	if len(in.Env) > MaxEnvVars {
		return "", validate.Errorf("env", "at most %d variables", MaxEnvVars)
	}
	total := 0
	for k, v := range in.Env {
		if !validate.EnvName.MatchString(k) {
			return "", validate.Errorf("env", "invalid variable name %q", validate.Clip(k, 60))
		}
		up := strings.ToUpper(k)
		if slices.Contains(reservedEnv, up) || strings.HasPrefix(up, "X_GOOGLE_") || strings.HasPrefix(up, "GOOGLE_") {
			return "", validate.Errorf("env", "%q is reserved", k)
		}
		total += len(k) + len(v)
	}
	if total > MaxEnvBytes {
		return "", validate.Errorf("env", "total size %d exceeds %d bytes", total, MaxEnvBytes)
	}
	if in.Port == 0 {
		in.Port = 8080
	}
	if in.Port < 1 || in.Port > 65535 {
		return "", validate.Errorf("port", "must be 1-65535")
	}
	if in.Memory == "" {
		in.Memory = "512Mi"
	}
	if !slices.Contains(memories, in.Memory) {
		return "", validate.Errorf("memory", "must be one of %s", strings.Join(memories, ", "))
	}
	if in.CPU == "" {
		in.CPU = "1"
	}
	if !slices.Contains(cpus, in.CPU) {
		return "", validate.Errorf("cpu", "must be one of %s", strings.Join(cpus, ", "))
	}
	if in.MaxInstances < 0 {
		return "", validate.Errorf("max_instances", "must be positive")
	}
	if in.MaxInstances == 0 || in.MaxInstances > c.cfg.MaxInstances {
		in.MaxInstances = c.cfg.MaxInstances
	}
	return name, nil
}

func (c *Client) serviceBody(in DeployIn, requester string) map[string]any {
	labels := map[string]string{LabelManaged: "true", LabelRequester: validate.LabelValue(requester)}
	keys := slices.Sorted(maps.Keys(in.Env))
	env := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, map[string]string{"name": k, "value": in.Env[k]})
	}
	tmpl := map[string]any{
		"labels":  labels,
		"scaling": map[string]any{"minInstanceCount": 0, "maxInstanceCount": in.MaxInstances},
		"containers": []map[string]any{{
			"image": in.Image,
			"ports": []map[string]any{{"containerPort": in.Port}},
			"env":   env,
			"resources": map[string]any{
				"limits": map[string]string{"cpu": in.CPU, "memory": in.Memory},
			},
		}},
	}
	if c.cfg.RuntimeServiceAccount != "" {
		tmpl["serviceAccount"] = c.cfg.RuntimeServiceAccount
	}
	body := map[string]any{
		"labels":   labels,
		"ingress":  "INGRESS_TRAFFIC_ALL", // reachable, but authenticated (IAM, or IAP sign-in)
		"template": tmpl,
	}
	if c.cfg.IAP {
		body["iapEnabled"] = true
	}
	return body
}

func (c *Client) deployPreview(ctx context.Context, call mcpx.Call, in DeployIn) (DeployOut, error) {
	name, err := c.validateDeploy(&in)
	if err != nil {
		return DeployOut{}, err
	}
	out := DeployOut{Service: name, Project: c.cfg.PreviewProject, Region: c.cfg.Region}
	var existing apiService
	err = c.run(ctx, http.MethodGet, c.servicePath(name), nil, nil, &existing)
	exists := err == nil
	if err != nil && !httpx.IsNotFound(err) {
		return out, err
	}
	if exists && existing.Labels[LabelManaged] != "true" {
		return out, fmt.Errorf("service %s exists but is not managed by Workbench (missing %s=true label); refusing to modify it", name, LabelManaged)
	}
	body := c.serviceBody(in, call.Requester)
	var op operation
	if exists {
		err = c.run(ctx, http.MethodPatch, c.servicePath(name), nil, body, &op)
	} else {
		if !in.AllowCreate {
			return out, fmt.Errorf("preview service %s does not exist; creating it adds spend and requires allow_create=true (approval-gated)", name)
		}
		err = c.run(ctx, http.MethodPost, c.parent()+"/services", url.Values{"serviceId": {name}}, body, &op)
		out.Created = err == nil
	}
	if err != nil {
		return out, err
	}
	out.Operation = op.Name
	if in.Wait == nil || *in.Wait {
		op, err = c.wait(ctx, op)
		if err != nil {
			return out, err
		}
	}
	out.Done = op.Done
	if op.Error != nil {
		return out, fmt.Errorf("deployment of %s failed: %s", name, httpx.Truncate(op.Error.Message, 600))
	}
	if st, err := c.fetchService(ctx, name); err == nil {
		out.Status = &st
	}
	for _, m := range in.Viewers {
		if _, err := c.editIAPAccess(ctx, name, m, "add"); err != nil {
			return out, fmt.Errorf("deployed, but could not give %s access: %w", m, err)
		}
		out.Viewers = append(out.Viewers, m)
	}
	return out, nil
}

// ---- get service ----

// ServiceIn identifies a preview service.
type ServiceIn struct {
	Service string `json:"service" jsonschema:"preview service name (wb-preview- prefix optional)"`
}

// Traffic is one traffic target.
type Traffic struct {
	Revision string `json:"revision,omitempty"`
	Type     string `json:"type,omitempty"`
	Percent  int    `json:"percent"`
}

// Service is a preview service summary.
type Service struct {
	Name                  string    `json:"name"`
	URL                   string    `json:"url,omitempty"`
	Ready                 bool      `json:"ready"`
	Reconciling           bool      `json:"reconciling"`
	Message               string    `json:"message,omitempty" jsonschema:"terminal condition message when not ready"`
	Image                 string    `json:"image,omitempty"`
	LatestReadyRevision   string    `json:"latest_ready_revision,omitempty"`
	LatestCreatedRevision string    `json:"latest_created_revision,omitempty"`
	Requester             string    `json:"requester,omitempty" jsonschema:"wb-requester label of the last deploy"`
	UpdateTime            string    `json:"update_time,omitempty"`
	Traffic               []Traffic `json:"traffic,omitempty"`
}

func (c *Client) fetchService(ctx context.Context, name string) (Service, error) {
	var s apiService
	if err := c.run(ctx, http.MethodGet, c.servicePath(name), nil, nil, &s); err != nil {
		if httpx.IsNotFound(err) {
			return Service{}, fmt.Errorf("preview service %s not found", name)
		}
		return Service{}, err
	}
	out := Service{
		Name: lastSegment(s.Name), URL: s.URI, Reconciling: s.Reconciling,
		LatestReadyRevision: lastSegment(s.LatestReadyRevision), LatestCreatedRevision: lastSegment(s.LatestCreatedRevision),
		Requester: s.Labels[LabelRequester], UpdateTime: s.UpdateTime,
	}
	if s.TerminalCondition != nil {
		out.Ready = s.TerminalCondition.State == "CONDITION_SUCCEEDED"
		if !out.Ready {
			out.Message = httpx.Truncate(s.TerminalCondition.Message, 600)
		}
	}
	if len(s.Template.Containers) > 0 {
		out.Image = s.Template.Containers[0].Image
	}
	for _, t := range s.TrafficStatuses {
		out.Traffic = append(out.Traffic, Traffic{Revision: lastSegment(t.Revision), Type: t.Type, Percent: t.Percent})
	}
	return out, nil
}

func (c *Client) getService(ctx context.Context, _ mcpx.Call, in ServiceIn) (Service, error) {
	name, err := ServiceName(in.Service)
	if err != nil {
		return Service{}, err
	}
	return c.fetchService(ctx, name)
}

// ---- revisions ----

// ListRevisionsIn are the arguments of cloudrun_list_revisions.
type ListRevisionsIn struct {
	Service   string `json:"service" jsonschema:"preview service name"`
	PageSize  int    `json:"page_size,omitempty" jsonschema:"default 20, max 50"`
	PageToken string `json:"page_token,omitempty" jsonschema:"next_page_token from a previous call"`
}

// Revision is a revision summary.
type Revision struct {
	Name       string `json:"name"`
	CreateTime string `json:"create_time,omitempty"`
	Image      string `json:"image,omitempty"`
	Ready      bool   `json:"ready"`
	Message    string `json:"message,omitempty"`
	Requester  string `json:"requester,omitempty"`
}

// ListRevisionsOut is the result of cloudrun_list_revisions.
type ListRevisionsOut struct {
	Revisions     []Revision `json:"revisions"`
	NextPageToken string     `json:"next_page_token,omitempty"`
}

func (c *Client) listRevisions(ctx context.Context, _ mcpx.Call, in ListRevisionsIn) (ListRevisionsOut, error) {
	name, err := ServiceName(in.Service)
	if err != nil {
		return ListRevisionsOut{}, err
	}
	q := url.Values{"pageSize": {strconv.Itoa(validate.Bounded(in.PageSize, 20, MaxRevisionsPage))}}
	if err := validate.PageToken("page_token", in.PageToken); err != nil {
		return ListRevisionsOut{}, err
	}
	if in.PageToken != "" {
		q.Set("pageToken", in.PageToken)
	}
	var res struct {
		Revisions     []apiRevision `json:"revisions"`
		NextPageToken string        `json:"nextPageToken"`
	}
	if err := c.run(ctx, http.MethodGet, c.servicePath(name)+"/revisions", q, nil, &res); err != nil {
		return ListRevisionsOut{}, err
	}
	out := ListRevisionsOut{Revisions: make([]Revision, 0, len(res.Revisions)), NextPageToken: res.NextPageToken}
	for _, r := range res.Revisions {
		rv := Revision{Name: lastSegment(r.Name), CreateTime: r.CreateTime, Requester: r.Labels[LabelRequester]}
		if len(r.Containers) > 0 {
			rv.Image = r.Containers[0].Image
		}
		for _, cnd := range r.Conditions {
			if cnd.Type == "Ready" {
				rv.Ready = cnd.State == "CONDITION_SUCCEEDED"
				if !rv.Ready {
					rv.Message = httpx.Truncate(cnd.Message, 300)
				}
			}
		}
		out.Revisions = append(out.Revisions, rv)
	}
	return out, nil
}

// ---- logs ----

// ReadLogsIn are the arguments of cloudrun_read_logs.
type ReadLogsIn struct {
	Service      string `json:"service" jsonschema:"preview service name"`
	SinceMinutes int    `json:"since_minutes,omitempty" jsonschema:"look back this many minutes (default 60, max 10080)"`
	MinSeverity  string `json:"min_severity,omitempty" jsonschema:"DEBUG, INFO, NOTICE, WARNING, ERROR, CRITICAL, ALERT or EMERGENCY"`
	Revision     string `json:"revision,omitempty" jsonschema:"only this revision"`
	Contains     string `json:"contains,omitempty" jsonschema:"only entries whose payload contains this text"`
	Limit        int    `json:"limit,omitempty" jsonschema:"max entries (default 50, max 200)"`
}

// LogEntry is one log line.
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Severity  string `json:"severity,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Message   string `json:"message"`
}

// ReadLogsOut is the result of cloudrun_read_logs.
type ReadLogsOut struct {
	Service   string     `json:"service"`
	Entries   []LogEntry `json:"entries"`
	Truncated bool       `json:"truncated" jsonschema:"true if entries or messages were cut to stay within output limits"`
}

var severities = []string{"DEFAULT", "DEBUG", "INFO", "NOTICE", "WARNING", "ERROR", "CRITICAL", "ALERT", "EMERGENCY"}

// now is replaceable in tests.
var now = time.Now

// logFilter builds the Logging query from validated values only; callers
// cannot widen it beyond the preview project's service.
func logFilter(region, service, revision, severity, contains string, since time.Time) string {
	parts := []string{
		`resource.type="cloud_run_revision"`,
		fmt.Sprintf(`resource.labels.service_name="%s"`, service),
		fmt.Sprintf(`resource.labels.location="%s"`, region),
		fmt.Sprintf(`timestamp>="%s"`, since.UTC().Format(time.RFC3339)),
	}
	if revision != "" {
		parts = append(parts, fmt.Sprintf(`resource.labels.revision_name="%s"`, revision))
	}
	if severity != "" {
		parts = append(parts, "severity>="+severity)
	}
	if contains != "" {
		parts = append(parts, fmt.Sprintf(`SEARCH(%s)`, quoteLogString(contains)))
	}
	return strings.Join(parts, " AND ")
}

// quoteLogString renders s as a Logging-query string literal.
func quoteLogString(s string) string {
	b, _ := json.Marshal(s) // escapes quotes, backslashes and control chars
	return string(b)
}

var revisionRe = regexp.MustCompile(`^wb-preview-[a-z0-9-]{1,60}$`)

func (c *Client) readLogs(ctx context.Context, _ mcpx.Call, in ReadLogsIn) (ReadLogsOut, error) {
	name, err := ServiceName(in.Service)
	if err != nil {
		return ReadLogsOut{}, err
	}
	since := validate.Bounded(in.SinceMinutes, 60, MaxLogSinceMinute)
	sev := strings.ToUpper(in.MinSeverity)
	if sev != "" && !slices.Contains(severities, sev) {
		return ReadLogsOut{}, validate.Errorf("min_severity", "must be one of %s", strings.Join(severities, ", "))
	}
	if in.Revision != "" && (!revisionRe.MatchString(in.Revision) || !strings.HasPrefix(in.Revision, name+"-")) {
		return ReadLogsOut{}, validate.Errorf("revision", "must be a revision of %s", name)
	}
	if err := validate.Text("contains", in.Contains, false, 200); err != nil {
		return ReadLogsOut{}, err
	}
	limit := validate.Bounded(in.Limit, 50, MaxLogEntries)
	filter := logFilter(c.cfg.Region, name, in.Revision, sev, in.Contains, now().Add(-time.Duration(since)*time.Minute))
	body := map[string]any{
		"resourceNames": []string{"projects/" + c.cfg.PreviewProject},
		"filter":        filter,
		"orderBy":       "timestamp desc",
		"pageSize":      limit,
	}
	var res struct {
		Entries []struct {
			Timestamp   string          `json:"timestamp"`
			Severity    string          `json:"severity"`
			TextPayload string          `json:"textPayload"`
			JSONPayload json.RawMessage `json:"jsonPayload"`
			HTTPRequest *struct {
				RequestMethod string `json:"requestMethod"`
				RequestURL    string `json:"requestUrl"`
				Status        int    `json:"status"`
				Latency       string `json:"latency"`
			} `json:"httpRequest"`
			Resource struct {
				Labels map[string]string `json:"labels"`
			} `json:"resource"`
		} `json:"entries"`
		NextPageToken string `json:"nextPageToken"`
	}
	if _, err := httpx.Do(ctx, c.http, httpx.Request{Method: http.MethodPost, URL: c.cfg.LoggingBaseURL + "/v2/entries:list", Body: body}, &res); err != nil {
		return ReadLogsOut{}, err
	}
	out := ReadLogsOut{Service: name, Entries: []LogEntry{}}
	total := 0
	for i, e := range res.Entries {
		if i >= limit {
			out.Truncated = true
			break
		}
		msg := e.TextPayload
		if msg == "" && len(e.JSONPayload) > 0 {
			var jp map[string]any
			if json.Unmarshal(e.JSONPayload, &jp) == nil {
				if m, ok := jp["message"].(string); ok {
					msg = m
				}
			}
			if msg == "" {
				msg = string(e.JSONPayload)
			}
		}
		if msg == "" && e.HTTPRequest != nil {
			msg = fmt.Sprintf("%s %s -> %d (%s)", e.HTTPRequest.RequestMethod, e.HTTPRequest.RequestURL, e.HTTPRequest.Status, e.HTTPRequest.Latency)
		}
		if len(msg) > MaxLogEntryBytes {
			msg = httpx.Truncate(msg, MaxLogEntryBytes)
			out.Truncated = true
		}
		if total+len(msg) > MaxLogTotalBytes {
			out.Truncated = true
			break
		}
		total += len(msg)
		out.Entries = append(out.Entries, LogEntry{Timestamp: e.Timestamp, Severity: e.Severity, Revision: e.Resource.Labels["revision_name"], Message: msg})
	}
	if res.NextPageToken != "" && len(out.Entries) >= limit {
		out.Truncated = true
	}
	return out, nil
}

// ---- delete ----

// DeleteOut is the result of cloudrun_delete_preview_service.
type DeleteOut struct {
	Service   string `json:"service"`
	Deleted   bool   `json:"deleted" jsonschema:"true once the deletion finished"`
	Operation string `json:"operation,omitempty"`
}

func (c *Client) deleteService(ctx context.Context, _ mcpx.Call, in ServiceIn) (DeleteOut, error) {
	name, err := ServiceName(in.Service)
	if err != nil {
		return DeleteOut{}, err
	}
	var existing apiService
	if err := c.run(ctx, http.MethodGet, c.servicePath(name), nil, nil, &existing); err != nil {
		if httpx.IsNotFound(err) {
			return DeleteOut{}, fmt.Errorf("preview service %s not found", name)
		}
		return DeleteOut{}, err
	}
	if existing.Labels[LabelManaged] != "true" {
		return DeleteOut{}, fmt.Errorf("service %s is not managed by Workbench (missing %s=true label); refusing to delete it", name, LabelManaged)
	}
	var op operation
	if err := c.run(ctx, http.MethodDelete, c.servicePath(name), nil, nil, &op); err != nil {
		return DeleteOut{}, err
	}
	op, err = c.wait(ctx, op)
	if err != nil {
		return DeleteOut{}, err
	}
	if op.Error != nil {
		return DeleteOut{}, fmt.Errorf("deleting %s failed: %s", name, httpx.Truncate(op.Error.Message, 600))
	}
	return DeleteOut{Service: name, Deleted: op.Done, Operation: op.Name}, nil
}

// ---- IAM ----

// SetInvokerIn are the arguments of cloudrun_set_iam_invoker.
type SetInvokerIn struct {
	Service string `json:"service" jsonschema:"preview service name"`
	Member  string `json:"member" jsonschema:"IAM member, e.g. user:alice@example.com, group:team@example.com, serviceAccount:x@p.iam.gserviceaccount.com, domain:example.com"`
	Action  string `json:"action,omitempty" jsonschema:"add (default) or remove"`
}

// SetInvokerOut is the result of cloudrun_set_iam_invoker.
type SetInvokerOut struct {
	Service string   `json:"service"`
	Role    string   `json:"role"`
	Changed bool     `json:"changed" jsonschema:"false if the member already had (or lacked) the role"`
	Members []string `json:"members" jsonschema:"members holding roles/run.invoker after the change"`
}

var memberRe = regexp.MustCompile(`^(user|group|serviceAccount):[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+\.)+[A-Za-z]{2,}$|^domain:([a-z0-9-]+\.)+[a-z]{2,}$`)

func (c *Client) checkMember(m string) error {
	switch m {
	case "allUsers", "allAuthenticatedUsers":
		if !c.cfg.AllowPublicInvoker {
			return validate.Errorf("member", "%s would make the preview public; this is disabled by server policy", m)
		}
		return nil
	}
	if !memberRe.MatchString(m) {
		return validate.Errorf("member", "%q is not a valid IAM member (user:, group:, serviceAccount: or domain:)", validate.Clip(m, 80))
	}
	domain := strings.ToLower(m[strings.LastIndexAny(m, "@:")+1:])
	if strings.HasPrefix(m, "serviceAccount:") && domain == c.cfg.PreviewProject+".iam.gserviceaccount.com" {
		return nil
	}
	if len(c.cfg.InvokerDomains) > 0 && !slices.Contains(c.cfg.InvokerDomains, domain) {
		return validate.Errorf("member", "domain %q is not allowed (allowed: %s)", domain, strings.Join(c.cfg.InvokerDomains, ", "))
	}
	return nil
}

type iamBinding struct {
	Role      string          `json:"role"`
	Members   []string        `json:"members"`
	Condition json.RawMessage `json:"condition,omitempty"`
}

// iamPolicy round-trips the policy; etag gives optimistic concurrency.
type iamPolicy struct {
	Version  int          `json:"version,omitempty"`
	Bindings []iamBinding `json:"bindings,omitempty"`
	Etag     string       `json:"etag,omitempty"`
}

func (c *Client) setIAMInvoker(ctx context.Context, _ mcpx.Call, in SetInvokerIn) (SetInvokerOut, error) {
	name, err := ServiceName(in.Service)
	if err != nil {
		return SetInvokerOut{}, err
	}
	action := in.Action
	if action == "" {
		action = "add"
	}
	if action != "add" && action != "remove" {
		return SetInvokerOut{}, validate.Errorf("action", "must be add or remove")
	}
	if err := c.checkMember(in.Member); err != nil {
		return SetInvokerOut{}, err
	}
	var pol iamPolicy
	// Request policy version 3 so conditional bindings round-trip intact.
	q := url.Values{"options.requestedPolicyVersion": {"3"}}
	if err := c.run(ctx, http.MethodGet, c.servicePath(name)+":getIamPolicy", q, nil, &pol); err != nil {
		if httpx.IsNotFound(err) {
			return SetInvokerOut{}, fmt.Errorf("preview service %s not found", name)
		}
		return SetInvokerOut{}, err
	}
	idx := -1
	for i, b := range pol.Bindings {
		if b.Role == InvokerRole && len(b.Condition) == 0 {
			idx = i
			break
		}
	}
	has := idx >= 0 && slices.Contains(pol.Bindings[idx].Members, in.Member)
	out := SetInvokerOut{Service: name, Role: InvokerRole}
	switch {
	case action == "add" && !has:
		if idx < 0 {
			pol.Bindings = append(pol.Bindings, iamBinding{Role: InvokerRole})
			idx = len(pol.Bindings) - 1
		}
		pol.Bindings[idx].Members = append(pol.Bindings[idx].Members, in.Member)
		out.Changed = true
	case action == "remove" && has:
		pol.Bindings[idx].Members = slices.DeleteFunc(pol.Bindings[idx].Members, func(m string) bool { return m == in.Member })
		if len(pol.Bindings[idx].Members) == 0 {
			pol.Bindings = slices.Delete(pol.Bindings, idx, idx+1)
			idx = -1
		}
		out.Changed = true
	}
	if out.Changed {
		var updated iamPolicy
		if err := c.run(ctx, http.MethodPost, c.servicePath(name)+":setIamPolicy", nil, map[string]any{"policy": pol}, &updated); err != nil {
			return SetInvokerOut{}, err
		}
		pol = updated
	}
	out.Members = []string{}
	for _, b := range pol.Bindings {
		if b.Role == InvokerRole && len(b.Condition) == 0 {
			out.Members = append(out.Members, b.Members...)
		}
	}
	return out, nil
}

// ---- IAP access ----

// IAPAccessorRole lets a member through IAP to a preview service.
const IAPAccessorRole = "roles/iap.httpsResourceAccessor"

// checkViewer allows people, groups and domains; never public access.
func (c *Client) checkViewer(m string) error {
	if m == "allUsers" || m == "allAuthenticatedUsers" || strings.HasPrefix(m, "serviceAccount:") {
		return validate.Errorf("member", "%q cannot be given browser access; name people (user:), groups (group:) or a domain (domain:)", validate.Clip(m, 80))
	}
	return c.checkMember(m)
}

// ShareIn are the arguments of cloudrun_share_preview.
type ShareIn struct {
	Service string `json:"service" jsonschema:"preview service name"`
	Member  string `json:"member" jsonschema:"who may open it in a browser: user:alice@example.com, group:team@example.com or domain:example.com"`
	Action  string `json:"action,omitempty" jsonschema:"add (default) or remove"`
}

// ShareOut is the result of cloudrun_share_preview.
type ShareOut struct {
	Service string   `json:"service"`
	Changed bool     `json:"changed" jsonschema:"false if the member already had (or lacked) access"`
	Members []string `json:"members" jsonschema:"everyone who may open the test version after the change"`
}

func (c *Client) sharePreview(ctx context.Context, _ mcpx.Call, in ShareIn) (ShareOut, error) {
	if !c.cfg.IAP {
		return ShareOut{}, errors.New("browser access needs IAP, which is not enabled on this server")
	}
	name, err := ServiceName(in.Service)
	if err != nil {
		return ShareOut{}, err
	}
	action := in.Action
	if action == "" {
		action = "add"
	}
	if action != "add" && action != "remove" {
		return ShareOut{}, validate.Errorf("action", "must be add or remove")
	}
	if err := c.checkViewer(in.Member); err != nil {
		return ShareOut{}, err
	}
	var svc apiService
	if err := c.run(ctx, http.MethodGet, c.servicePath(name), nil, nil, &svc); err != nil {
		if httpx.IsNotFound(err) {
			return ShareOut{}, fmt.Errorf("preview service %s not found", name)
		}
		return ShareOut{}, err
	}
	if svc.Labels[LabelManaged] != "true" {
		return ShareOut{}, fmt.Errorf("service %s is not managed by Workbench; refusing to change its access", name)
	}
	return c.editIAPAccess(ctx, name, in.Member, action)
}

// editIAPAccess adds or removes member's IAP accessor role on a service,
// round-tripping the policy with its etag.
func (c *Client) editIAPAccess(ctx context.Context, name, member, action string) (ShareOut, error) {
	var pol iamPolicy
	if err := c.iap(ctx, http.MethodPost, name, "getIamPolicy", map[string]any{"options": map[string]any{"requestedPolicyVersion": 3}}, &pol); err != nil {
		return ShareOut{}, err
	}
	idx := -1
	for i, b := range pol.Bindings {
		if b.Role == IAPAccessorRole && len(b.Condition) == 0 {
			idx = i
			break
		}
	}
	has := idx >= 0 && slices.Contains(pol.Bindings[idx].Members, member)
	out := ShareOut{Service: name}
	switch {
	case action == "add" && !has:
		if idx < 0 {
			pol.Bindings = append(pol.Bindings, iamBinding{Role: IAPAccessorRole})
			idx = len(pol.Bindings) - 1
		}
		pol.Bindings[idx].Members = append(pol.Bindings[idx].Members, member)
		out.Changed = true
	case action == "remove" && has:
		pol.Bindings[idx].Members = slices.DeleteFunc(pol.Bindings[idx].Members, func(m string) bool { return m == member })
		if len(pol.Bindings[idx].Members) == 0 {
			pol.Bindings = slices.Delete(pol.Bindings, idx, idx+1)
		}
		out.Changed = true
	}
	if out.Changed {
		pol.Version = 3
		var updated iamPolicy
		if err := c.iap(ctx, http.MethodPost, name, "setIamPolicy", map[string]any{"policy": pol}, &updated); err != nil {
			return ShareOut{}, err
		}
		pol = updated
	}
	out.Members = []string{}
	for _, b := range pol.Bindings {
		if b.Role == IAPAccessorRole && len(b.Condition) == 0 {
			out.Members = append(out.Members, b.Members...)
		}
	}
	return out, nil
}
