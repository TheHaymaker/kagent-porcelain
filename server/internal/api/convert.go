package api

import (
	"context"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	wb "github.com/thehaymaker/kagent-porcelain/server/gen/workbench/v1"
	"github.com/thehaymaker/kagent-porcelain/server/internal/approval"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

func tsp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// people resolves principal IDs to Person messages in one query.
type people map[string]store.Person

func (s *Server) people(ctx context.Context, ids ...string) people {
	m, err := s.Store.People(ctx, ids)
	if err != nil {
		return people{}
	}
	return m
}

func (p people) person(id string) *wb.Person {
	if id == "" {
		return nil
	}
	if x, ok := p[id]; ok {
		return &wb.Person{Id: x.ID, Email: x.Email, DisplayName: x.DisplayName}
	}
	return &wb.Person{Id: id}
}

var kindToProto = map[policy.Kind]wb.ActionKind{
	policy.Ordinary: wb.ActionKind_ACTION_KIND_ORDINARY, policy.ProductionDeploy: wb.ActionKind_ACTION_KIND_PRODUCTION_DEPLOY,
	policy.IAMChange: wb.ActionKind_ACTION_KIND_IAM_CHANGE, policy.DataDeletion: wb.ActionKind_ACTION_KIND_DATA_DELETION,
	policy.NewSpend: wb.ActionKind_ACTION_KIND_NEW_SPEND,
}

var stateToProto = map[string]wb.ApprovalState{
	store.ApprovalPending: wb.ApprovalState_APPROVAL_STATE_PENDING, store.ApprovalApproved: wb.ApprovalState_APPROVAL_STATE_APPROVED,
	store.ApprovalRejected: wb.ApprovalState_APPROVAL_STATE_REJECTED, store.ApprovalSuperseded: wb.ApprovalState_APPROVAL_STATE_SUPERSEDED,
}

func approvalIDs(list []store.Approval) []string {
	var ids []string
	for _, a := range list {
		ids = append(ids, a.RequesterID, a.DecidedByID)
		ids = append(ids, a.ApproverIDs...)
	}
	return ids
}

func (p people) approval(v approval.View) *wb.Approval {
	a := v.Approval
	out := &wb.Approval{
		Id: a.ID, State: stateToProto[a.State], SessionId: a.SessionID, TaskId: a.TaskID, Agent: a.Agent,
		Requester: p.person(a.RequesterID), Kind: kindToProto[a.Kind], Hint: a.Hint, ApproverGroups: a.ApproverGroups,
		RequesterMayDecide: a.RequesterMayDecide, DecidedBy: p.person(a.DecidedByID), Reason: a.Reason,
		CreatedAt: tsp(a.CreatedAt), CallerMayDecide: v.CallerMayDecide, AppId: a.AppID,
	}
	if a.DecidedAt != nil {
		out.DecidedAt = tsp(*a.DecidedAt)
	}
	for _, id := range a.ApproverIDs {
		out.Approvers = append(out.Approvers, p.person(id))
	}
	for _, c := range a.Calls {
		out.Calls = append(out.Calls, &wb.ToolCall{Id: c.ID, CallId: c.CallID, Name: c.Name, Args: toStruct(c.Args), Kind: kindToProto[c.Kind]})
	}
	return out
}

var appStatusToProto = map[string]wb.AppStatus{
	store.AppBuilding: wb.AppStatus_APP_STATUS_BUILDING, store.AppPreview: wb.AppStatus_APP_STATUS_PREVIEW,
	store.AppLive: wb.AppStatus_APP_STATUS_LIVE, store.AppFailed: wb.AppStatus_APP_STATUS_FAILED, store.AppArchived: wb.AppStatus_APP_STATUS_ARCHIVED,
}

var envToProto = map[string]wb.EnvironmentName{
	store.EnvPreview: wb.EnvironmentName_ENVIRONMENT_NAME_PREVIEW, store.EnvProduction: wb.EnvironmentName_ENVIRONMENT_NAME_PRODUCTION,
}

func (p people) app(a store.App, costMicros int64, pendingApproval string) *wb.App {
	out := &wb.App{Id: a.ID, Name: a.Name, Description: a.Description, Owner: p.person(a.OwnerID), Status: appStatusToProto[a.Status],
		Scaffold: a.Scaffold, RepositoryUrl: a.RepositoryURL, SessionId: a.SessionID, Agent: a.Agent, CostCentsMtd: costMicros / 10_000,
		CreatedAt: tsp(a.CreatedAt), UpdatedAt: tsp(a.UpdatedAt), PendingApprovalId: pendingApproval}
	for _, e := range a.Environments {
		out.Environments = append(out.Environments, &wb.Environment{Name: envToProto[e.Name], Url: e.URL, Revision: e.Revision, Image: e.Image, UpdatedAt: tsp(e.UpdatedAt)})
	}
	return out
}

func (p people) auditEvent(e store.AuditEvent) *wb.AuditEvent {
	actor := p.person(e.ActorID)
	if actor != nil && strings.HasPrefix(e.ActorID, "agent:") {
		actor = &wb.Person{Id: e.ActorID, DisplayName: "Agent " + strings.TrimPrefix(e.ActorID, "agent:")}
	}
	return &wb.AuditEvent{Id: e.ID, Time: tsp(e.Time), Type: e.Type, Actor: actor, OnBehalfOf: p.person(e.OnBehalfOfID),
		SessionId: e.SessionID, TaskId: e.TaskID, AppId: e.AppID, ApprovalId: e.ApprovalID, Summary: e.Summary,
		Detail: toStruct(e.Detail), Surface: e.Surface}
}

// Enum names on the wire are SCREAMING_CASE with a type prefix; the store
// uses the lower-case suffix.
func enumSuffix(name, prefix string) string { return strings.ToLower(strings.TrimPrefix(name, prefix)) }
