// Package audit records what was asked, allowed, refused and done, and ships
// the record to BigQuery.
package audit

import (
	"context"
	"log/slog"

	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

// Event types. Clients filter on prefixes, so keep the dotted shape.
const (
	ApprovalRequested     = "approval.requested"
	ApprovalDecided       = "approval.decided"
	ApprovalAutoDecided   = "approval.auto_decided"
	ApprovalReassigned    = "approval.reassigned"
	ApprovalDelivered     = "approval.delivered"
	ApprovalSuperseded    = "approval.superseded"
	ApprovalDeliveryStale = "approval.delivery_stale"
	AppRegistered         = "app.registered"
	AppUpdated            = "app.updated"
	AppArchived           = "app.archived"
	AppDeployed           = "app.deployed"
	AppPromotionRequested = "app.promotion_requested"
	AppPromoted           = "app.promoted"
	AppPromotionFailed    = "app.promotion_failed"
	TaskFinished          = "task.finished"
	TaskInputNeeded       = "task.input_needed"
	IdentityLinked        = "identity.linked"
	IdentityUnlinked      = "identity.unlinked"
	LimitSet              = "usage.limit_set"
	ComponentShown        = "component.shown"
	ComponentAction       = "component.action"
)

// Recorder appends audit events. A failure to record is logged loudly but does
// not undo the action: the action already happened, and the record of it is
// retried by the caller's own retry where one exists.
type Recorder interface {
	Record(ctx context.Context, e store.AuditEvent)
}

// StoreRecorder writes events to the audit table.
type StoreRecorder struct {
	Store *store.Store
	Log   *slog.Logger
}

// Record implements Recorder.
func (r StoreRecorder) Record(ctx context.Context, e store.AuditEvent) {
	if _, err := r.Store.AppendAudit(ctx, e); err != nil {
		log := r.Log
		if log == nil {
			log = slog.Default()
		}
		log.ErrorContext(ctx, "AUDIT WRITE FAILED", "type", e.Type, "summary", e.Summary, "actor", e.ActorID, "error", err)
	}
}

// Memory collects events, for tests.
type Memory struct{ Events []store.AuditEvent }

// Record implements Recorder.
func (m *Memory) Record(_ context.Context, e store.AuditEvent) { m.Events = append(m.Events, e) }

// Types returns the recorded event types in order.
func (m *Memory) Types() []string {
	out := make([]string, len(m.Events))
	for i, e := range m.Events {
		out[i] = e.Type
	}
	return out
}
