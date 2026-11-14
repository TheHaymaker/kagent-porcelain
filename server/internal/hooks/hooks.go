// Package hooks is Workbench's internal HTTP listener: where kagent posts
// push notifications and the model gateway reports usage. It is never
// exposed outside the cluster; clients use the gRPC API on kagent's server.
package hooks

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/jwk"
	"github.com/lestrrat-go/jwx/jwt"

	"github.com/thehaymaker/kagent-porcelain/server/internal/approval"
	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
	"github.com/thehaymaker/kagent-porcelain/server/internal/identity"
	"github.com/thehaymaker/kagent-porcelain/server/internal/notify"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/usage"
)

// Paths.
const (
	PushPath  = "/hooks/a2a"
	UsagePath = "/hooks/usage"
)

// PushKeys returns kagent's push signing keys.
type PushKeys func(ctx context.Context) (jwk.Set, error)

// Handler serves the hooks.
type Handler struct {
	Approvals *approval.Service
	Meter     *usage.Meter
	// PushKeys and PushIssuer verify kagent's push JWTs. PushURL is the exact
	// callback URL registered with kagent, which is the JWT audience.
	PushKeys   PushKeys
	PushIssuer string
	PushURL    string
	// UsageSecret verifies the gateway's HMAC signature.
	UsageSecret []byte
	Ready       func(context.Context) error
	Log         *slog.Logger
	now         func() time.Time
}

// ServeMux returns the routes.
func (h *Handler) ServeMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+PushPath, h.push)
	mux.HandleFunc("POST "+UsagePath, h.usage)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if h.Ready != nil {
			if err := h.Ready(r.Context()); err != nil {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func (h *Handler) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

const maxBody = 1 << 20

// push receives a kagent push notification. The JWT proves kagent sent it
// for this task; the body is then only a hint, and the task is read back
// through the API as the source of truth.
func (h *Handler) push(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var u hitl.StatusUpdate
	if err := json.Unmarshal(body, &u); err != nil || u.StatusUpdate == nil || u.StatusUpdate.TaskID == "" || u.StatusUpdate.ContextID == "" {
		http.Error(w, "not a status update", http.StatusBadRequest)
		return
	}
	if err := h.verifyPush(r, u.StatusUpdate.TaskID); err != nil {
		h.log().WarnContext(r.Context(), "push notification refused", "error", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	system := identity.Caller{ID: identity.SystemPrincipal, Delegated: true}
	if err := h.Approvals.Sync(r.Context(), system, u.StatusUpdate.ContextID, u.StatusUpdate.TaskID); err != nil {
		h.log().ErrorContext(r.Context(), "process push notification", "task", u.StatusUpdate.TaskID, "error", err)
		// 5xx makes kagent retry, which is what we want for a transient failure.
		http.Error(w, "retry", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) verifyPush(r *http.Request, taskID string) error {
	if h.PushKeys == nil {
		return errors.New("push verification is not configured")
	}
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return errors.New("no bearer token")
	}
	set, err := h.PushKeys(r.Context())
	if err != nil {
		return err
	}
	now := time.Now
	if h.now != nil {
		now = h.now
	}
	tok, err := jwt.ParseString(raw, jwt.WithKeySet(set), jwt.WithValidate(true), jwt.WithIssuer(h.PushIssuer),
		jwt.WithAudience(h.PushURL), jwt.WithClock(jwt.ClockFunc(now)), jwt.WithAcceptableSkew(30*time.Second))
	if err != nil {
		return err
	}
	if tok.Expiration().IsZero() || tok.IssuedAt().IsZero() {
		return errors.New("token lacks exp or iat")
	}
	claimed, _ := tok.PrivateClaims()["taskId"].(string)
	if claimed != taskID {
		return fmt.Errorf("token is for task %q, body names %q", claimed, taskID)
	}
	return nil
}

// UsageReport is what the model gateway posts per model call.
type UsageReport struct {
	PersonID     string    `json:"personId"`
	SessionID    string    `json:"sessionId"`
	AppID        string    `json:"appId"`
	Model        string    `json:"model"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	CostMicros   int64     `json:"costMicros"`
	RequestID    string    `json:"requestId"`
	Time         time.Time `json:"time"`
}

// UsageSignatureHeader carries notify.Sign(secret, body).
const UsageSignatureHeader = "X-Workbench-Signature"

func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if len(h.UsageSecret) == 0 || !hmac.Equal([]byte(r.Header.Get(UsageSignatureHeader)), []byte(notify.Sign(h.UsageSecret, body))) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var reports []UsageReport
	if err := json.Unmarshal(body, &reports); err != nil {
		var one UsageReport
		if err := json.Unmarshal(body, &one); err != nil {
			http.Error(w, "bad usage report", http.StatusBadRequest)
			return
		}
		reports = []UsageReport{one}
	}
	for _, u := range reports {
		// A session tells us whose usage it is when the gateway only knows
		// the conversation (Claude Code in a sandbox, say).
		if u.PersonID == "" && u.SessionID != "" {
			if ws, err := h.Approvals.Store.GetWatchedSession(r.Context(), u.SessionID); err == nil {
				u.PersonID = ws.RequesterID
			}
		}
		if err := h.Meter.Record(r.Context(), store.UsageRecord{Time: u.Time, PersonID: u.PersonID, SessionID: u.SessionID, AppID: u.AppID,
			Model: u.Model, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CostMicros: u.CostMicros, RequestID: u.RequestID}); err != nil {
			h.log().WarnContext(r.Context(), "usage report refused", "request", u.RequestID, "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoteKeys fetches kagent's push JWKS from its own server.
func RemoteKeys(ctx context.Context, url string) PushKeys {
	ar := jwk.NewAutoRefresh(ctx)
	ar.Configure(url, jwk.WithMinRefreshInterval(time.Minute))
	return func(ctx context.Context) (jwk.Set, error) { return ar.Fetch(ctx, url) }
}
