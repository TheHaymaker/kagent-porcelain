package mcpx_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx/mcpxtest"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/validate"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type echoIn struct {
	Msg string `json:"msg"`
}
type echoOut struct {
	Requester string `json:"requester"`
	Msg       string `json:"msg"`
}

func newTestServer(t *testing.T, require bool) (*mcpx.Server, *syncBuf) {
	buf := &syncBuf{}
	red := mcpx.NewRedactor()
	red.AddSource(func() []string { return []string{"s3cr3t-token-value"} })
	s := mcpx.NewServer(mcpx.Options{Name: "test", Logger: slog.New(slog.NewJSONHandler(buf, nil)), Redactor: red, RequireRequester: require})
	mcpx.AddTool(s, &mcp.Tool{Name: "test_get_echo", Description: "echo", Annotations: mcpx.ReadOnly("Echo", false)},
		func(ctx context.Context, c mcpx.Call, in echoIn) (echoOut, error) {
			return echoOut{Requester: c.Requester, Msg: in.Msg}, nil
		})
	mcpx.AddTool(s, &mcp.Tool{Name: "test_create_thing", Description: "mutate", Annotations: mcpx.Mutating("Create", false, false, false)},
		func(ctx context.Context, c mcpx.Call, in echoIn) (echoOut, error) {
			switch in.Msg {
			case "leak":
				return echoOut{}, errors.New("upstream said: token s3cr3t-token-value and " + fakeGitLabToken + " and Bearer ya29.abcdefghijklmnop")
			case "invalid":
				return echoOut{}, validate.Errorf("msg", "bad")
			case "panic":
				panic("boom s3cr3t-token-value")
			}
			return echoOut{Requester: c.Requester}, nil
		})
	return s, buf
}

func TestRequesterIsPropagatedAndAudited(t *testing.T) {
	s, logs := newTestServer(t, false)
	cs := mcpxtest.Connect(t, s, "alice@example.com")
	var out echoOut
	mcpxtest.MustOK(t, cs, "test_get_echo", map[string]any{"msg": "hi"}, &out)
	if out.Requester != "alice@example.com" || out.Msg != "hi" {
		t.Fatalf("got %+v", out)
	}
	if !strings.Contains(logs.String(), `"requester":"alice@example.com"`) || !strings.Contains(logs.String(), `"tool":"test_get_echo"`) {
		t.Fatalf("audit log missing: %s", logs.String())
	}
}

func TestMissingRequester(t *testing.T) {
	s, _ := newTestServer(t, true)
	cs := mcpxtest.Connect(t, s, "")
	var out echoOut
	mcpxtest.MustOK(t, cs, "test_get_echo", map[string]any{"msg": "hi"}, &out) // read-only allowed
	if out.Requester != mcpx.UnknownRequester {
		t.Fatalf("got %q", out.Requester)
	}
	mcpxtest.MustErr(t, cs, "test_create_thing", map[string]any{"msg": "x"}, "X-User-Id")
}

func TestErrorsAreRedacted(t *testing.T) {
	s, logs := newTestServer(t, false)
	cs := mcpxtest.Connect(t, s, "bob")
	txt := mcpxtest.MustErr(t, cs, "test_create_thing", map[string]any{"msg": "leak"}, "[REDACTED]")
	for _, secret := range []string{"s3cr3t-token-value", fakeGitLabToken, "ya29.abcdefghijklmnop"} {
		if strings.Contains(txt, secret) || strings.Contains(logs.String(), secret) {
			t.Fatalf("secret %q leaked: %s / %s", secret, txt, logs.String())
		}
	}
}

func TestValidationAndPanic(t *testing.T) {
	s, logs := newTestServer(t, false)
	cs := mcpxtest.Connect(t, s, "bob")
	mcpxtest.MustErr(t, cs, "test_create_thing", map[string]any{"msg": "invalid"}, "invalid argument: msg: bad")
	txt := mcpxtest.MustErr(t, cs, "test_create_thing", map[string]any{"msg": "panic"}, "internal error")
	if strings.Contains(txt, "s3cr3t") || strings.Contains(logs.String(), "s3cr3t") {
		t.Fatal("panic value leaked")
	}
}

func TestSanitizeRequester(t *testing.T) {
	if got := mcpx.SanitizeRequester(" a\nb\x00c "); got != "abc" {
		t.Fatalf("got %q", got)
	}
	if got := mcpx.SanitizeRequester(strings.Repeat("x", 500)); len(got) != 128 {
		t.Fatalf("len %d", len(got))
	}
}

func TestToolNameConventionEnforced(t *testing.T) {
	s, _ := newTestServer(t, false)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	mcpx.AddTool(s, &mcp.Tool{Name: "BadName", Annotations: mcpx.ReadOnly("x", false)},
		func(ctx context.Context, c mcpx.Call, in echoIn) (echoOut, error) { return echoOut{}, nil })
}

// fakeGitLabToken is built from pieces so secret scanners do not mistake it for a real token.
const fakeGitLabToken = "glpat" + "-" + "abcdefghijklmnopqrstu"
