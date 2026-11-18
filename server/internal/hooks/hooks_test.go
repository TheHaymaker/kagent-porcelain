package hooks

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/lestrrat-go/jwx/jwt"

	"github.com/thehaymaker/kagent-porcelain/server/internal/approval"
	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
	"github.com/thehaymaker/kagent-porcelain/server/internal/kagent"
	"github.com/thehaymaker/kagent-porcelain/server/internal/notify"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/testdb"
	"github.com/thehaymaker/kagent-porcelain/server/internal/usage"
)

type k struct{}

func (k) Session(_ context.Context, _, id string) (kagent.SessionInfo, error) {
	return kagent.SessionInfo{ID: id, Agent: "workbench/builder", Creator: "alice"}, nil
}
func (k) Task(_ context.Context, _, _, id string) (kagent.Task, error) {
	return kagent.Task{ID: id, ContextID: "ctx", State: hitl.StateCompleted}, nil
}
func (k) PausedTasks(context.Context, string, string, string) ([]kagent.Task, error) { return nil, nil }
func (k) Send(context.Context, string, string, string, hitl.Message) error           { return nil }
func (k) Watch(context.Context, string, string, string, string) error                { return nil }

type n struct{ got []notify.Notice }

func (x *n) Notify(_ context.Context, _ []string, no notify.Notice) { x.got = append(x.got, no) }

const callback = "http://workbench-hooks.workbench.svc:8090/hooks/a2a"

func TestPush(t *testing.T) {
	s := testdb.New(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := jwk.New(priv)
	_ = key.Set(jwk.KeyIDKey, "k")
	pk, _ := jwk.New(pub)
	_ = pk.Set(jwk.KeyIDKey, "k")
	_ = pk.Set(jwk.AlgorithmKey, jwa.EdDSA)
	set := jwk.NewSet()
	set.Add(pk)
	notes := &n{}
	h := &Handler{
		Approvals: &approval.Service{Store: s, Policy: policy.Default("a"), Kagent: k{}, Audit: &audit.Memory{}, Notify: notes},
		PushKeys:  func(context.Context) (jwk.Set, error) { return set, nil }, PushIssuer: "https://kagent.example", PushURL: callback,
	}
	sign := func(aud, task string) string {
		tok := jwt.New()
		_ = tok.Set(jwt.IssuerKey, "https://kagent.example")
		_ = tok.Set(jwt.AudienceKey, aud)
		_ = tok.Set(jwt.IssuedAtKey, time.Now())
		_ = tok.Set(jwt.ExpirationKey, time.Now().Add(5*time.Minute))
		_ = tok.Set("taskId", task)
		b, _ := jwt.Sign(tok, jwa.EdDSA, key)
		return string(b)
	}
	body := []byte(`{"statusUpdate":{"taskId":"t1","contextId":"ctx","status":{"state":"TASK_STATE_COMPLETED"}}}`)
	post := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, PushPath, bytes.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeMux().ServeHTTP(rec, req)
		return rec.Code
	}
	if c := post(""); c != http.StatusUnauthorized {
		t.Fatalf("unsigned: %d", c)
	}
	if c := post(sign("http://elsewhere/hooks/a2a", "t1")); c != http.StatusUnauthorized {
		t.Fatalf("wrong audience: %d", c)
	}
	if c := post(sign(callback, "t2")); c != http.StatusUnauthorized {
		t.Fatalf("token for another task: %d", c)
	}
	if c := post(sign(callback, "t1")); c != http.StatusNoContent {
		t.Fatalf("valid: %d", c)
	}
	if len(notes.got) != 1 || notes.got[0].Kind != notify.TaskFinished {
		t.Fatalf("finished task must notify: %+v", notes.got)
	}
}

func TestUsage(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	_ = s.WatchSession(ctx, store.WatchedSession{SessionID: "ctx", Agent: "a", RequesterID: "alice"})
	h := &Handler{Approvals: &approval.Service{Store: s}, Meter: &usage.Meter{Store: s}, UsageSecret: []byte("secret")}
	body := []byte(`[{"sessionId":"ctx","model":"claude","inputTokens":10,"outputTokens":5,"costMicros":2500,"requestId":"r1"}]`)
	req := httptest.NewRequest(http.MethodPost, UsagePath, bytes.NewReader(body))
	req.Header.Set(UsageSignatureHeader, notify.Sign([]byte("wrong"), body))
	rec := httptest.NewRecorder()
	h.ServeMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatal("bad signature")
	}
	req = httptest.NewRequest(http.MethodPost, UsagePath, bytes.NewReader(body))
	req.Header.Set(UsageSignatureHeader, notify.Sign([]byte("secret"), body))
	rec = httptest.NewRecorder()
	h.ServeMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	spent, _ := s.SpentMicros(ctx, []string{"alice"}, time.Now().Add(-time.Hour))
	if spent != 2500 {
		t.Fatalf("usage must be attributed to the session's requester: %d", spent)
	}
}
