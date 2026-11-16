// Package mcptools adds Workbench's tools to kagent's /mcp endpoint, so a
// builder agent binds them like any kagent tool and calls them with its own
// runtime identity. kagent's runtime binding tells us which conversation and
// which requester every call is for; tool arguments never do.
package mcptools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/server/internal/approval"
	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/components"
	"github.com/thehaymaker/kagent-porcelain/server/internal/identity"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/registry"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/usage"
)

// Tool names. The approval policy matches production deploys on
// ToolRequestPromotion, so renaming it is a policy change.
const (
	ToolShow             = "workbench_show"
	ToolRegisterApp      = "workbench_register_app"
	ToolRecordDeployment = "workbench_record_deployment"
	ToolRequestPromotion = "workbench_request_promotion"
	ToolGetApp           = "workbench_get_app"
	ToolGetRequester     = "workbench_get_requester"
)

// Tools holds what the tools need.
type Tools struct {
	Store      *store.Store
	Registry   *registry.Registry
	Approvals  *approval.Service
	Components *components.Validator
	Audit      audit.Recorder
	// callerFrom is replaced in tests.
	callerFrom func(context.Context) (identity.Caller, bool)
}

// Register adds the tools to kagent's MCP server. It is app.Options.MCPExtensions.
func (t *Tools) Register(s *mcp.Server) error {
	if t.callerFrom == nil {
		t.callerFrom = identity.FromContext
	}
	no, yes := new(false), new(true)
	mcp.AddTool(s, &mcp.Tool{Name: ToolShow, Title: "Show components to the requester",
		Description: "Show the requester rich components (progress, deploy previews, app cards, cost summaries, approval cards) as A2UI " + components.Version +
			" messages against the Workbench catalogue " + t.Components.CatalogID + ". Components: " + strings.Join(t.Components.Components(), ", ") +
			". Send createSurface first, then updateComponents (one component must have id \"root\"), then updateDataModel to change bound values. Reuse a surfaceId to update a surface in place.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: no, IdempotentHint: true, OpenWorldHint: no}}, t.show)
	mcp.AddTool(s, &mcp.Tool{Name: ToolRegisterApp, Title: "Register an app",
		Description: "Add the app you are building to the Workbench catalogue, owned by the requester. Call once, after agreeing the app's name with the requester. Names are 3-40 lowercase letters, digits and dashes.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: no, OpenWorldHint: no}}, t.registerApp)
	mcp.AddTool(s, &mcp.Tool{Name: ToolRecordDeployment, Title: "Record a preview deployment",
		Description: "Record that a revision of the app is running in preview, with its URL and the image digest it runs. Only preview: production is reached through workbench_request_promotion.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: no, IdempotentHint: true, OpenWorldHint: no}}, t.recordDeployment)
	mcp.AddTool(s, &mcp.Tool{Name: ToolRequestPromotion, Title: "Request production",
		Description: "Ask to put a preview revision of the app into production. An approver who is not the requester must allow it; Workbench then deploys it. Returns status promoted (with the production URL), pending (with the approval ID to show in an ApprovalCard), or refused.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: yes, OpenWorldHint: yes}}, t.requestPromotion)
	mcp.AddTool(s, &mcp.Tool{Name: ToolGetApp, Title: "Read an app",
		Description: "Read an app's status, environments and URLs from the Workbench catalogue.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: no}}, t.getApp)
	mcp.AddTool(s, &mcp.Tool{Name: ToolGetRequester, Title: "Who asked",
		Description: "The name and email address of the person this conversation is for, e.g. to give them access to their app's test version.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: no}}, t.getRequester)
	return nil
}

// runtime returns the conversation and requester a call is for. Only agent
// runtimes may call these tools.
func (t *Tools) runtime(ctx context.Context) (identity.Caller, error) {
	c, ok := t.callerFrom(ctx)
	if !ok || !c.IsRuntime() || c.CreatorID == "" {
		return c, errors.New("these tools are for Workbench builder agents running in a conversation")
	}
	return c, nil
}

func (t *Tools) ownedApp(ctx context.Context, c identity.Caller, appID string) (store.App, error) {
	app, err := t.Store.GetApp(ctx, appID)
	if err != nil {
		return app, fmt.Errorf("no app %s", appID)
	}
	if app.OwnerID != c.CreatorID {
		return app, fmt.Errorf("app %s belongs to someone else", appID)
	}
	return app, nil
}

// ShowInput is workbench_show's input.
type ShowInput struct {
	Messages []map[string]any `json:"messages" jsonschema:"A2UI messages for one surface, in order"`
}

// SurfaceOut carries the surface back in the tool result, which is how
// clients that read the A2A stream receive it.
type SurfaceOut struct {
	SurfaceID string           `json:"surfaceId"`
	Messages  []map[string]any `json:"messages"`
}

// ShowOutput is workbench_show's result.
type ShowOutput struct {
	A2UI SurfaceOut `json:"a2ui"`
}

func (t *Tools) show(ctx context.Context, _ *mcp.CallToolRequest, in ShowInput) (*mcp.CallToolResult, ShowOutput, error) {
	c, err := t.runtime(ctx)
	if err != nil {
		return nil, ShowOutput{}, err
	}
	b, err := t.Components.Validate(in.Messages)
	if err != nil {
		return nil, ShowOutput{}, fmt.Errorf("components not shown: %w", err)
	}
	for _, m := range b.Messages {
		if body, ok := m["updateComponents"].(map[string]any); ok {
			if err := t.checkApprovalCards(ctx, c, body["components"]); err != nil {
				return nil, ShowOutput{}, err
			}
		}
	}
	if b.Deleted {
		err = t.Store.DeleteSurface(ctx, c.RuntimeSessionID, b.SurfaceID)
	} else {
		err = t.Store.AppendSurface(ctx, store.Surface{SurfaceID: b.SurfaceID, SessionID: c.RuntimeSessionID, CatalogID: t.Components.CatalogID, Messages: b.Messages})
	}
	if err != nil {
		return nil, ShowOutput{}, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Shown to the requester as surface " + b.SurfaceID + "."}}},
		ShowOutput{A2UI: SurfaceOut{SurfaceID: b.SurfaceID, Messages: b.Messages}}, nil
}

// checkApprovalCards refuses an ApprovalCard for an approval outside this
// conversation, so an agent cannot point a requester at someone else's.
func (t *Tools) checkApprovalCards(ctx context.Context, c identity.Caller, raw any) error {
	list, _ := raw.([]any)
	for _, item := range list {
		comp, _ := item.(map[string]any)
		if comp["component"] != "ApprovalCard" {
			continue
		}
		id, _ := comp["approvalId"].(string)
		a, err := t.Store.GetApproval(ctx, id)
		if err != nil || a.RequesterID != c.CreatorID {
			return fmt.Errorf("ApprovalCard names approval %q, which is not one of this conversation's", id)
		}
	}
	return nil
}

// RegisterInput is workbench_register_app's input.
type RegisterInput struct {
	Name          string `json:"name" jsonschema:"short app name: lowercase letters, digits and dashes"`
	Description   string `json:"description" jsonschema:"one sentence the requester would recognise"`
	Scaffold      string `json:"scaffold" jsonschema:"the golden-path scaffold the app started from"`
	RepositoryURL string `json:"repositoryUrl,omitempty" jsonschema:"https URL of the GitLab project"`
}

// AppOutput describes an app.
type AppOutput struct {
	AppID         string `json:"appId"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	PreviewURL    string `json:"previewUrl,omitempty"`
	ProductionURL string `json:"productionUrl,omitempty"`
	// CostCentsMTD is what building and running the app cost this month,
	// for a CostSummary component.
	CostCentsMTD int64 `json:"costCentsMtd"`
}

func appOut(a store.App) AppOutput {
	out := AppOutput{AppID: a.ID, Name: a.Name, Status: a.Status}
	if e, ok := a.Env(store.EnvPreview); ok {
		out.PreviewURL = e.URL
	}
	if e, ok := a.Env(store.EnvProduction); ok {
		out.ProductionURL = e.URL
	}
	return out
}

func (t *Tools) registerApp(ctx context.Context, _ *mcp.CallToolRequest, in RegisterInput) (*mcp.CallToolResult, AppOutput, error) {
	c, err := t.runtime(ctx)
	if err != nil {
		return nil, AppOutput{}, err
	}
	ws, _ := t.Store.GetWatchedSession(ctx, c.RuntimeSessionID)
	app, err := t.Registry.Register(ctx, registry.Registration{Name: in.Name, Description: in.Description, Scaffold: in.Scaffold,
		RepositoryURL: in.RepositoryURL, OwnerID: c.CreatorID, SessionID: c.RuntimeSessionID, Agent: ws.Agent, ActorID: c.ID})
	if errors.Is(err, store.ErrConflict) {
		return nil, AppOutput{}, fmt.Errorf("an app called %q already exists; agree another name with the requester", in.Name)
	}
	if err != nil {
		return nil, AppOutput{}, err
	}
	return nil, appOut(app), nil
}

// DeploymentInput is workbench_record_deployment's input.
type DeploymentInput struct {
	AppID       string `json:"appId"`
	Environment string `json:"environment" jsonschema:"must be preview"`
	URL         string `json:"url" jsonschema:"https URL where the requester can try it"`
	Revision    string `json:"revision" jsonschema:"the Cloud Run revision name"`
	Image       string `json:"image" jsonschema:"the image by digest (…@sha256:…)"`
}

// DeploymentOutput reports a recorded deployment.
type DeploymentOutput struct {
	DeploymentID string `json:"deploymentId"`
	AppOutput
}

func (t *Tools) recordDeployment(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentInput) (*mcp.CallToolResult, DeploymentOutput, error) {
	c, err := t.runtime(ctx)
	if err != nil {
		return nil, DeploymentOutput{}, err
	}
	if in.Environment != store.EnvPreview {
		return nil, DeploymentOutput{}, errors.New("agents record preview deployments only; use workbench_request_promotion for production")
	}
	if _, err := t.ownedApp(ctx, c, in.AppID); err != nil {
		return nil, DeploymentOutput{}, err
	}
	d, err := t.Registry.RecordPreview(ctx, in.AppID, in.URL, in.Revision, in.Image, c.ID, c.RuntimeSessionID)
	if err != nil {
		return nil, DeploymentOutput{}, err
	}
	app, _ := t.Store.GetApp(ctx, in.AppID)
	return nil, DeploymentOutput{DeploymentID: d.ID, AppOutput: appOut(app)}, nil
}

// PromotionInput is workbench_request_promotion's input.
type PromotionInput struct {
	AppID    string `json:"appId"`
	Revision string `json:"revision,omitempty" jsonschema:"preview revision to promote; empty means what preview serves now"`
	Note     string `json:"note,omitempty" jsonschema:"why, for the approver"`
}

// PromotionOutput reports where a promotion stands.
type PromotionOutput struct {
	Status        string `json:"status" jsonschema:"promoted, pending or refused"`
	ApprovalID    string `json:"approvalId"`
	ProductionURL string `json:"productionUrl,omitempty"`
	Message       string `json:"message"`
}

func (t *Tools) requestPromotion(ctx context.Context, _ *mcp.CallToolRequest, in PromotionInput) (*mcp.CallToolResult, PromotionOutput, error) {
	c, err := t.runtime(ctx)
	if err != nil {
		return nil, PromotionOutput{}, err
	}
	app, err := t.ownedApp(ctx, c, in.AppID)
	if err != nil {
		return nil, PromotionOutput{}, err
	}
	// If kagent paused this call and an approver allowed it, the promotion
	// already ran when they decided. Find that approval and report it.
	approvals, err := t.Store.ListSessionApprovals(ctx, c.RuntimeSessionID)
	if err != nil {
		return nil, PromotionOutput{}, err
	}
	for i := len(approvals) - 1; i >= 0; i-- {
		a := approvals[i]
		appID, rev := registry.PromotionTarget(a)
		if a.Kind != policy.ProductionDeploy || appID != app.ID || (in.Revision != "" && rev != "" && rev != in.Revision) {
			continue
		}
		switch a.State {
		case store.ApprovalApproved:
			d, err := t.Registry.Promote(ctx, a)
			if err != nil {
				return nil, PromotionOutput{Status: "pending", ApprovalID: a.ID, Message: "Approved, but the deployment failed: " + err.Error()}, nil
			}
			return nil, PromotionOutput{Status: "promoted", ApprovalID: a.ID, ProductionURL: d.URL,
				Message: fmt.Sprintf("%s revision %s is live in production.", app.Name, d.Revision)}, nil
		case store.ApprovalPending:
			return nil, PromotionOutput{Status: "pending", ApprovalID: a.ID, Message: "Waiting for " + strings.Join(a.ApproverGroups, ", ") + " to decide."}, nil
		case store.ApprovalRejected:
			if a.Delivered && a.TaskID == "" {
				return nil, PromotionOutput{Status: "refused", ApprovalID: a.ID, Message: "Refused: " + a.Reason}, nil
			}
		}
	}
	// Nothing gated this call: open an approval so production is still
	// reached only through an eligible approver.
	a, err := t.Approvals.RequestPromotion(ctx, identity.Caller{ID: c.ID, Surface: "agent"}, app, in.Revision, in.Note, c.RuntimeSessionID)
	if err != nil {
		return nil, PromotionOutput{}, err
	}
	return nil, PromotionOutput{Status: "pending", ApprovalID: a.ID,
		Message: "Asked " + strings.Join(a.ApproverGroups, ", ") + " to allow it. Show the requester an ApprovalCard with this approvalId."}, nil
}

// GetAppInput is workbench_get_app's input.
type GetAppInput struct {
	AppID string `json:"appId"`
}

func (t *Tools) getApp(ctx context.Context, _ *mcp.CallToolRequest, in GetAppInput) (*mcp.CallToolResult, AppOutput, error) {
	c, err := t.runtime(ctx)
	if err != nil {
		return nil, AppOutput{}, err
	}
	app, err := t.ownedApp(ctx, c, in.AppID)
	if err != nil {
		return nil, AppOutput{}, err
	}
	out := appOut(app)
	if spent, err := t.Store.AppSpentMicros(ctx, []string{app.ID}, usage.MonthStart(time.Now())); err == nil {
		out.CostCentsMTD = spent[app.ID] / 10_000
	}
	return nil, out, nil
}

// RequesterOutput is workbench_get_requester's result.
type RequesterOutput struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

func (t *Tools) getRequester(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, RequesterOutput, error) {
	c, err := t.runtime(ctx)
	if err != nil {
		return nil, RequesterOutput{}, err
	}
	p, err := t.Store.GetPerson(ctx, c.CreatorID)
	if err != nil || p.Email == "" {
		return nil, RequesterOutput{}, errors.New("the requester's email address is not known yet; they need to have signed in to Workbench")
	}
	return nil, RequesterOutput{Name: p.DisplayName, Email: p.Email}, nil
}
