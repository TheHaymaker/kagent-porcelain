// Package approval is Workbench's approval service: the one place the rules
// about who may allow what are applied. It turns kagent's HITL pauses into
// approvals, routes them, takes decisions from whichever surface the decider
// uses, and sends each decision back into the paused task.
package approval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
	"github.com/thehaymaker/kagent-porcelain/server/internal/identity"
	"github.com/thehaymaker/kagent-porcelain/server/internal/kagent"
	"github.com/thehaymaker/kagent-porcelain/server/internal/notify"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

// Errors the API layer maps to status codes.
var (
	ErrPermission = errors.New("permission denied")
	ErrInvalid    = errors.New("invalid")
	ErrNotFound   = store.ErrNotFound
	ErrConflict   = store.ErrConflict
)

// Promoter performs an approved promotion. registry.Registry implements it.
type Promoter interface {
	Promote(ctx context.Context, a store.Approval) (store.Deployment, error)
}

// Notifier is notify.Notifier's sending half.
type Notifier interface {
	Notify(ctx context.Context, personIDs []string, n notify.Notice)
}

// Service is the approval service.
type Service struct {
	Store    *store.Store
	Policy   policy.Policy
	Kagent   kagent.Client
	Audit    audit.Recorder
	Notify   Notifier
	Promoter Promoter
	// CallbackURL is where kagent posts push notifications for watched tasks.
	CallbackURL string
	Log         *slog.Logger
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Observation is one look at a task's status, from a push notification or
// from reading the task.
type Observation struct {
	SessionID string
	TaskID    string
	State     string
	Message   *hitl.Message
}

// Observe brings approvals in line with a task's state. It is idempotent: the
// same pause observed twice yields one approval.
func (s *Service) Observe(ctx context.Context, o Observation) error {
	if o.SessionID == "" || o.TaskID == "" {
		return fmt.Errorf("%w: observation needs a session and a task", ErrInvalid)
	}
	ws, err := s.session(ctx, o.SessionID)
	if err != nil {
		return err
	}
	if o.State != hitl.StateInputRequired {
		s.supersede(ctx, o.TaskID, "")
		if hitl.Terminal(o.State) {
			s.taskEnded(ctx, ws, o)
		}
		return nil
	}
	req, ok := hitl.ReadRequest(o.TaskID, o.SessionID, o.Message)
	if !ok || req.Kind == hitl.KindAskUser {
		s.supersede(ctx, o.TaskID, "")
		key := "input:" + o.TaskID
		if ok {
			key += ":" + req.AskUserID
		}
		s.Notify.Notify(ctx, []string{ws.RequesterID}, notify.Notice{Kind: notify.InputNeeded,
			Title: "Your agent has a question", Body: hintOr(req.Hint, "The agent needs your answer to continue."),
			Link: "/c/" + o.SessionID, SessionID: o.SessionID, DedupeKey: key})
		return nil
	}
	return s.observePause(ctx, ws, req)
}

func (s *Service) observePause(ctx context.Context, ws store.WatchedSession, req hitl.Request) error {
	calls := make([]store.Call, len(req.Tools))
	kinds := make([]policy.Kind, len(req.Tools))
	appID := ""
	for i, t := range req.Tools {
		kinds[i] = s.Policy.Classify(t.Name, t.Args)
		calls[i] = store.Call{Tool: t, Kind: kinds[i]}
		if v, ok := t.Args["appId"].(string); ok && kinds[i] == policy.ProductionDeploy && appID == "" {
			appID = v
		}
	}
	kind := policy.Most(kinds...)
	route := s.Policy.Route(kind, ws.RequesterID)
	fp := req.Fingerprint()
	a, created, err := s.Store.CreateApproval(ctx, store.Approval{
		Fingerprint: fp, SessionID: ws.SessionID, TaskID: req.TaskID, Agent: ws.Agent, RequesterID: ws.RequesterID,
		Kind: kind, Hint: req.Hint, Calls: calls, ApproverGroups: route.ApproverGroups, RequesterMayDecide: route.RequesterMayDecide,
		AppID: appID,
	})
	if err != nil {
		return err
	}
	if !created {
		return nil
	}
	s.supersede(ctx, req.TaskID, fp)
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.ApprovalRequested, ActorID: "agent:" + ws.Agent, OnBehalfOfID: ws.RequesterID,
		SessionID: ws.SessionID, TaskID: req.TaskID, ApprovalID: a.ID, AppID: appID,
		Summary: fmt.Sprintf("Agent asked to %s (%s)", toolNames(calls), kind), Detail: map[string]any{"calls": calls, "kind": kind}})

	if kind == policy.Ordinary && s.applyRemembered(ctx, a) {
		return nil
	}
	s.announce(ctx, a)
	return nil
}

// applyRemembered decides an ordinary approval from the requester's
// remembered answers, if every call has one.
func (s *Service) applyRemembered(ctx context.Context, a store.Approval) bool {
	decisions := make([]hitl.Decision, 0, len(a.Calls))
	approved := true
	for _, c := range a.Calls {
		r, err := s.Store.FindRemembered(ctx, a.RequesterID, a.Agent, a.SessionID, c.Name)
		if err != nil {
			return false
		}
		d := hitl.Decision{ID: c.ID, Approved: r.Approved}
		if !r.Approved {
			d.Reason = "Refused by a remembered answer."
			approved = false
		}
		decisions = append(decisions, d)
	}
	decided, err := s.Store.Decide(ctx, a.ID, a.RequesterID, approved, decisions, "remembered answer")
	if err != nil {
		s.log().ErrorContext(ctx, "apply remembered answer", "approval", a.ID, "error", err)
		return false
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.ApprovalAutoDecided, ActorID: a.RequesterID, SessionID: a.SessionID,
		TaskID: a.TaskID, ApprovalID: a.ID, Summary: fmt.Sprintf("%s by a remembered answer: %s", verdict(approved), toolNames(a.Calls))})
	s.deliver(ctx, decided)
	return true
}

// announce tells the people who may decide, and tells the requester when
// someone else must.
func (s *Service) announce(ctx context.Context, a store.Approval) {
	link := "/approvals/" + a.ID
	title := "Approval needed: " + describe(a)
	if a.RequesterMayDecide {
		s.Notify.Notify(ctx, []string{a.RequesterID}, notify.Notice{Kind: notify.ApprovalNeeded, Title: title,
			Body: hintOr(a.Hint, "Your agent is waiting for you."), Link: "/c/" + a.SessionID, SessionID: a.SessionID,
			ApprovalID: a.ID, DedupeKey: "need:" + a.ID})
	}
	if a.Kind == policy.Ordinary {
		return
	}
	var approvers []string
	for _, g := range a.ApproverGroups {
		people, err := s.Store.PeopleInGroup(ctx, g)
		if err != nil {
			s.log().ErrorContext(ctx, "list approvers", "group", g, "error", err)
			continue
		}
		for _, p := range people {
			if p.ID != a.RequesterID {
				approvers = append(approvers, p.ID)
			}
		}
	}
	approvers = append(approvers, a.ApproverIDs...)
	s.Notify.Notify(ctx, approvers, notify.Notice{Kind: notify.ApprovalNeeded, Title: title,
		Body: hintOr(a.Hint, "An agent is waiting for a decision."), Link: link, SessionID: a.SessionID,
		ApprovalID: a.ID, AppID: a.AppID, DedupeKey: "need:" + a.ID})
	if !a.RequesterMayDecide {
		s.Notify.Notify(ctx, []string{a.RequesterID}, notify.Notice{Kind: notify.ApprovalNeeded,
			Title: "Waiting for approval: " + describe(a), Body: "Someone in " + strings.Join(a.ApproverGroups, ", ") + " needs to allow this.",
			Link: "/c/" + a.SessionID, SessionID: a.SessionID, ApprovalID: a.ID, DedupeKey: "wait:" + a.ID})
	}
}

func (s *Service) supersede(ctx context.Context, taskID, keep string) {
	changed, err := s.Store.SupersedePending(ctx, taskID, keep)
	if err != nil {
		s.log().ErrorContext(ctx, "supersede approvals", "task", taskID, "error", err)
		return
	}
	for _, a := range changed {
		s.Audit.Record(ctx, store.AuditEvent{Type: audit.ApprovalSuperseded, SessionID: a.SessionID, TaskID: a.TaskID,
			ApprovalID: a.ID, OnBehalfOfID: a.RequesterID, Summary: "The task moved on before a decision: " + toolNames(a.Calls)})
	}
}

func (s *Service) taskEnded(ctx context.Context, ws store.WatchedSession, o Observation) {
	kind, title, body := notify.TaskFinished, "Your agent finished", "Open the conversation to see the result."
	if o.State != hitl.StateCompleted {
		kind, title, body = notify.TaskFailed, "Your agent stopped", "The task ended as "+strings.ToLower(strings.TrimPrefix(o.State, "TASK_STATE_"))+"."
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.TaskFinished, OnBehalfOfID: ws.RequesterID, SessionID: ws.SessionID,
		TaskID: o.TaskID, Summary: title, Detail: map[string]any{"state": o.State}})
	s.Notify.Notify(ctx, []string{ws.RequesterID}, notify.Notice{Kind: kind, Title: title, Body: body,
		Link: "/c/" + ws.SessionID, SessionID: ws.SessionID, DedupeKey: "end:" + o.TaskID})
}

// session returns the recorded session, learning it from kagent the first
// time. kagent is asked as the system principal, which may read every session.
func (s *Service) session(ctx context.Context, sessionID string) (store.WatchedSession, error) {
	ws, err := s.Store.GetWatchedSession(ctx, sessionID)
	if err == nil {
		return ws, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return ws, err
	}
	info, err := s.Kagent.Session(ctx, identity.SystemPrincipal, sessionID)
	if err != nil {
		return ws, fmt.Errorf("look up session %s: %w", sessionID, err)
	}
	ws = store.WatchedSession{SessionID: info.ID, Agent: info.Agent, RequesterID: info.Creator}
	return ws, s.Store.WatchSession(ctx, ws)
}

// Sync reads a task from kagent and observes it. Clients call this
// indirectly (through ListSessionApprovals), so an approval exists the moment
// anyone looks, even if a push notification is late or was never registered.
func (s *Service) Sync(ctx context.Context, caller identity.Caller, sessionID, taskID string) error {
	ws, err := s.session(ctx, sessionID)
	if err != nil {
		return err
	}
	if !s.mayView(caller, ws.RequesterID) {
		return ErrPermission
	}
	t, err := s.Kagent.Task(ctx, ws.RequesterID, ws.Agent, taskID)
	if err != nil {
		return err
	}
	if t.ContextID != "" && t.ContextID != sessionID {
		return fmt.Errorf("%w: task %s is not in session %s", ErrInvalid, taskID, sessionID)
	}
	if !hitl.Terminal(t.State) && s.CallbackURL != "" {
		if err := s.Kagent.Watch(ctx, ws.RequesterID, ws.Agent, taskID, s.CallbackURL); err != nil {
			s.log().WarnContext(ctx, "register push callback", "task", taskID, "error", err)
		}
	}
	return s.Observe(ctx, Observation{SessionID: sessionID, TaskID: taskID, State: t.State, Message: t.Message})
}

// SyncSession observes every paused task in a session. The approval list a
// client reads is therefore current even when no push notification arrived.
func (s *Service) SyncSession(ctx context.Context, caller identity.Caller, sessionID string) error {
	ws, err := s.session(ctx, sessionID)
	if err != nil {
		return err
	}
	if !s.mayView(caller, ws.RequesterID) {
		return ErrPermission
	}
	tasks, err := s.Kagent.PausedTasks(ctx, ws.RequesterID, ws.Agent, sessionID)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if s.CallbackURL != "" {
			if err := s.Kagent.Watch(ctx, ws.RequesterID, ws.Agent, t.ID, s.CallbackURL); err != nil {
				s.log().WarnContext(ctx, "register push callback", "task", t.ID, "error", err)
			}
		}
		if err := s.Observe(ctx, Observation{SessionID: sessionID, TaskID: t.ID, State: t.State, Message: t.Message}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) mayView(c identity.Caller, requesterID string) bool {
	return c.ID == requesterID || c.Delegated
}

// View is an approval as one caller sees it.
type View struct {
	store.Approval
	CallerMayDecide bool
}

// Viewable reports whether the caller may see an approval: the requester,
// anyone who may decide it, and anyone who did.
func (s *Service) Viewable(c identity.Caller, a store.Approval, auditor bool) bool {
	return auditor || c.ID == a.RequesterID || c.ID == a.DecidedByID || a.Routing().MayDecide(policy.Decider{ID: c.ID, Groups: c.Groups})
}

// ViewFor wraps an approval for a caller.
func ViewFor(c identity.Caller, a store.Approval) View {
	may := a.State == store.ApprovalPending && a.Routing().MayDecide(policy.Decider{ID: c.ID, Groups: c.Groups})
	return View{Approval: a, CallerMayDecide: may}
}

// Decision is what a decider submits.
type Decision struct {
	ApprovalID string
	Approved   bool
	Reason     string
	// Calls overrides the overall decision per call.
	Calls    []hitl.Decision
	Remember string // "", "once", "conversation", "always"
}

// Decide records a decision and sends it to the paused task.
func (s *Service) Decide(ctx context.Context, c identity.Caller, d Decision) (store.Approval, error) {
	a, err := s.Store.GetApproval(ctx, d.ApprovalID)
	if err != nil {
		return a, err
	}
	if a.State != store.ApprovalPending {
		return a, fmt.Errorf("%w: this approval was already %s", ErrConflict, a.State)
	}
	if !a.Routing().MayDecide(policy.Decider{ID: c.ID, Groups: c.Groups}) {
		if c.ID == a.RequesterID {
			return a, fmt.Errorf("%w: you cannot approve your own %s; someone in %s must", ErrPermission,
				strings.ReplaceAll(string(a.Kind), "_", " "), strings.Join(a.ApproverGroups, ", "))
		}
		return a, fmt.Errorf("%w: you may not decide this approval", ErrPermission)
	}
	switch d.Remember {
	case "", "once":
	case "conversation", "always":
		if a.Kind.AlwaysAsk() {
			return a, fmt.Errorf("%w: %s always asks; its answers cannot be remembered", ErrInvalid, strings.ReplaceAll(string(a.Kind), "_", " "))
		}
		if c.ID != a.RequesterID {
			return a, fmt.Errorf("%w: only the requester can remember an answer", ErrInvalid)
		}
	default:
		return a, fmt.Errorf("%w: unknown remember scope %q", ErrInvalid, d.Remember)
	}
	reason := strings.TrimSpace(d.Reason)
	if len(reason) > 2000 {
		return a, fmt.Errorf("%w: reason is too long", ErrInvalid)
	}
	if !d.Approved && reason == "" && a.Kind.AlwaysAsk() {
		return a, fmt.Errorf("%w: give a reason when refusing a %s", ErrInvalid, strings.ReplaceAll(string(a.Kind), "_", " "))
	}
	decisions, approved, err := buildDecisions(a, d, reason)
	if err != nil {
		return a, err
	}
	decided, err := s.Store.Decide(ctx, a.ID, c.ID, approved, decisions, reason)
	if err != nil {
		return a, err
	}
	onBehalf := ""
	if c.ID != a.RequesterID {
		onBehalf = a.RequesterID
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.ApprovalDecided, ActorID: c.ID, OnBehalfOfID: onBehalf, SessionID: a.SessionID,
		TaskID: a.TaskID, ApprovalID: a.ID, AppID: a.AppID, Surface: c.Surface,
		Summary: fmt.Sprintf("%s %s", verdict(approved), describe(a)),
		Detail:  map[string]any{"reason": reason, "decisions": decisions, "kind": a.Kind, "remember": d.Remember}})
	if d.Remember == "conversation" || d.Remember == "always" {
		for _, call := range a.Calls {
			if _, err := s.Store.Remember(ctx, store.RememberedAnswer{PersonID: c.ID, Scope: d.Remember, ToolName: call.Name,
				Agent: a.Agent, SessionID: a.SessionID, Approved: decisionFor(decisions, call.ID)}); err != nil {
				s.log().ErrorContext(ctx, "remember answer", "error", err)
			}
		}
	}
	// A promotion runs before the task resumes, so the agent's tool call
	// reports a finished deployment rather than racing it.
	if approved && a.Kind == policy.ProductionDeploy && s.Promoter != nil {
		if _, err := s.Promoter.Promote(ctx, decided); err != nil {
			s.log().ErrorContext(ctx, "promotion after approval failed", "approval", a.ID, "error", err)
		}
	}
	if onBehalf != "" {
		s.Notify.Notify(ctx, []string{a.RequesterID}, notify.Notice{Kind: notify.ApprovalDecided,
			Title: fmt.Sprintf("%s: %s", verdict(approved), describe(a)), Body: reasonOr(reason, approved),
			Link: "/c/" + a.SessionID, SessionID: a.SessionID, ApprovalID: a.ID, DedupeKey: "decided:" + a.ID})
	}
	s.deliver(ctx, decided)
	return s.Store.GetApproval(ctx, a.ID)
}

func buildDecisions(a store.Approval, d Decision, reason string) ([]hitl.Decision, bool, error) {
	byID := map[string]hitl.Decision{}
	for _, cd := range d.Calls {
		byID[cd.ID] = cd
	}
	out := make([]hitl.Decision, 0, len(a.Calls))
	all := true
	for _, call := range a.Calls {
		dec, ok := byID[call.ID]
		if ok {
			delete(byID, call.ID)
		} else {
			dec = hitl.Decision{ID: call.ID, Approved: d.Approved}
		}
		if !dec.Approved {
			all = false
			if dec.Reason == "" {
				dec.Reason = reason
			}
		} else {
			dec.Reason = ""
		}
		out = append(out, dec)
	}
	if len(byID) > 0 {
		return nil, false, fmt.Errorf("%w: a decision names a call this approval does not cover", ErrInvalid)
	}
	return out, all, nil
}

// deliver sends a decision into its paused task, if it has one and the task
// is still paused there. Failures are retried by Redeliver.
func (s *Service) deliver(ctx context.Context, a store.Approval) {
	if err := s.tryDeliver(ctx, a); err != nil {
		s.log().WarnContext(ctx, "decision not yet delivered; will retry", "approval", a.ID, "error", err)
	}
}

func (s *Service) tryDeliver(ctx context.Context, a store.Approval) error {
	if a.TaskID == "" {
		return s.Store.MarkDelivered(ctx, a.ID)
	}
	t, err := s.Kagent.Task(ctx, a.RequesterID, a.Agent, a.TaskID)
	if err != nil {
		return err
	}
	current, ok := hitl.ReadRequest(t.ID, a.SessionID, t.Message)
	if t.State != hitl.StateInputRequired || !ok || current.Fingerprint() != a.Fingerprint {
		s.Audit.Record(ctx, store.AuditEvent{Type: audit.ApprovalDeliveryStale, SessionID: a.SessionID, TaskID: a.TaskID,
			ApprovalID: a.ID, Summary: "The task was no longer waiting for this decision", Detail: map[string]any{"state": t.State}})
		return s.Store.MarkDelivered(ctx, a.ID)
	}
	msg, err := hitl.ToolApprovalResponse(current, a.Decisions, "wb-"+a.ID)
	if err != nil {
		return err
	}
	if err := s.Kagent.Send(ctx, a.RequesterID, a.DecidedByID, a.Agent, msg); err != nil {
		return err
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.ApprovalDelivered, ActorID: a.DecidedByID, OnBehalfOfID: a.RequesterID,
		SessionID: a.SessionID, TaskID: a.TaskID, ApprovalID: a.ID, Summary: "Decision delivered to the agent"})
	return s.Store.MarkDelivered(ctx, a.ID)
}

// Redeliver retries decisions that have not reached their task. It runs as a
// background worker on the leader.
type Redeliver struct {
	Service  *Service
	Interval time.Duration
}

// Start implements controller-runtime's Runnable.
func (r Redeliver) Start(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		pending, err := r.Service.Store.ListUndelivered(ctx, 50)
		if err != nil {
			r.Service.log().ErrorContext(ctx, "list undelivered decisions", "error", err)
			continue
		}
		for _, a := range pending {
			r.Service.deliver(ctx, a)
		}
	}
}

// NeedLeaderElection keeps one redeliverer across replicas.
func (Redeliver) NeedLeaderElection() bool { return true }

// Reassign names a colleague as an approver. The requester (for their own
// approvals) and anyone who may already decide can reassign.
func (s *Service) Reassign(ctx context.Context, c identity.Caller, approvalID, assignee, note string) (store.Approval, error) {
	a, err := s.Store.GetApproval(ctx, approvalID)
	if err != nil {
		return a, err
	}
	if c.ID != a.RequesterID && !a.Routing().MayDecide(policy.Decider{ID: c.ID, Groups: c.Groups}) {
		return a, ErrPermission
	}
	p, err := s.Store.FindPerson(ctx, assignee)
	if err != nil {
		return a, fmt.Errorf("%w: nobody called %q has signed in to Workbench", ErrInvalid, assignee)
	}
	if p.ID == a.RequesterID {
		return a, fmt.Errorf("%w: assign it to someone other than the requester", ErrInvalid)
	}
	a, err = s.Store.Reassign(ctx, approvalID, p.ID)
	if err != nil {
		return a, err
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.ApprovalReassigned, ActorID: c.ID, SessionID: a.SessionID, ApprovalID: a.ID,
		Summary: fmt.Sprintf("Asked %s to decide %s", firstNonEmpty(p.DisplayName, p.Email, p.ID), describe(a)), Detail: map[string]any{"note": note}})
	s.Notify.Notify(ctx, []string{p.ID}, notify.Notice{Kind: notify.ApprovalNeeded, Title: "You were asked to decide: " + describe(a),
		Body: firstNonEmpty(note, a.Hint), Link: "/approvals/" + a.ID, ApprovalID: a.ID, DedupeKey: "assigned:" + a.ID + ":" + p.ID})
	return a, nil
}

// RequestPromotion creates a production-deploy approval for an app that no
// agent pause wraps: a request from the app catalogue, or from an agent whose
// template did not gate the promotion tool. Production is reached the same
// way in both cases.
func (s *Service) RequestPromotion(ctx context.Context, c identity.Caller, app store.App, revision, note, sessionID string) (store.Approval, error) {
	if app.Status == store.AppArchived {
		return store.Approval{}, fmt.Errorf("%w: app is archived", ErrInvalid)
	}
	if revision == "" {
		env, ok := app.Env(store.EnvPreview)
		if !ok || env.Revision == "" {
			return store.Approval{}, fmt.Errorf("%w: %s has no preview to promote yet", ErrInvalid, app.Name)
		}
		revision = env.Revision
	}
	if _, err := s.Store.FindPreviewDeployment(ctx, app.ID, revision); err != nil {
		return store.Approval{}, fmt.Errorf("%w: revision %s was never deployed to preview", ErrInvalid, revision)
	}
	// One open request per app and revision.
	if latest, err := s.Store.LatestApprovalForApp(ctx, app.ID); err == nil && latest.State == store.ApprovalPending {
		if _, rev := promotionArgs(latest); rev == revision {
			return latest, nil
		}
	}
	route := s.Policy.Route(policy.ProductionDeploy, app.OwnerID)
	call := store.Call{Tool: hitl.Tool{ID: "promote", Name: "workbench_request_promotion",
		Args: map[string]any{"appId": app.ID, "revision": revision, "note": note}}, Kind: policy.ProductionDeploy}
	a, _, err := s.Store.CreateApproval(ctx, store.Approval{
		Fingerprint: "promotion:" + store.NewID("p"), SessionID: sessionID, Agent: app.Agent, RequesterID: app.OwnerID,
		Kind: policy.ProductionDeploy, Hint: firstNonEmpty(note, "Put "+app.Name+" into production"), Calls: []store.Call{call},
		ApproverGroups: route.ApproverGroups, RequesterMayDecide: route.RequesterMayDecide, AppID: app.ID,
	})
	if err != nil {
		return a, err
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.AppPromotionRequested, ActorID: c.ID, OnBehalfOfID: app.OwnerID, AppID: app.ID,
		ApprovalID: a.ID, SessionID: sessionID, Surface: c.Surface, Summary: fmt.Sprintf("Asked to put %s revision %s into production", app.Name, revision)})
	s.announce(ctx, a)
	return a, nil
}

func promotionArgs(a store.Approval) (appID, revision string) {
	for _, c := range a.Calls {
		appID, _ = c.Args["appId"].(string)
		revision, _ = c.Args["revision"].(string)
	}
	return
}

func decisionFor(ds []hitl.Decision, id string) bool {
	for _, d := range ds {
		if d.ID == id {
			return d.Approved
		}
	}
	return false
}

func toolNames(calls []store.Call) string {
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

func describe(a store.Approval) string {
	switch a.Kind {
	case policy.ProductionDeploy:
		return "production deploy"
	case policy.IAMChange:
		return "access change (" + toolNames(a.Calls) + ")"
	case policy.DataDeletion:
		return "data deletion (" + toolNames(a.Calls) + ")"
	case policy.NewSpend:
		return "new spend (" + toolNames(a.Calls) + ")"
	}
	return toolNames(a.Calls)
}

func verdict(approved bool) string {
	if approved {
		return "Allowed"
	}
	return "Refused"
}

func reasonOr(reason string, approved bool) string {
	if reason != "" {
		return reason
	}
	if approved {
		return "Your agent will continue."
	}
	return "Your agent will be told it was refused."
}

func hintOr(h, fallback string) string { return firstNonEmpty(h, fallback) }

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
