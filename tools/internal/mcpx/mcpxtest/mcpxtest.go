// Package mcpxtest connects a real MCP client to an mcpx.Server over
// Streamable HTTP (httptest), so tests exercise schema validation,
// annotations, headers and structured output exactly as kagent would.
package mcpxtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
)

type headerRT struct {
	base   http.RoundTripper
	userID string
}

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if h.userID != "" {
		r.Header.Set(mcpx.UserIDHeader, h.userID)
	}
	return h.base.RoundTrip(r)
}

// Connect serves s on an httptest server and returns a connected client
// session that sends X-User-Id: userID (omitted when empty).
func Connect(t testing.TB, s *mcpx.Server, userID string) *mcp.ClientSession {
	t.Helper()
	ts := httptest.NewServer(s.HTTPHandler())
	t.Cleanup(ts.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	tr := &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: headerRT{base: http.DefaultTransport, userID: userID}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	cs, err := client.Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// Call invokes a tool and returns the raw result.
func Call(t testing.TB, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: protocol error: %v", name, err)
	}
	return res
}

// MustOK invokes a tool, fails on a tool error, and decodes structured
// content into out (if non-nil).
func MustOK(t testing.TB, cs *mcp.ClientSession, name string, args any, out any) {
	t.Helper()
	res := Call(t, cs, name, args)
	if res.IsError {
		t.Fatalf("%s returned error: %s", name, Text(res))
	}
	if out != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("decode %s output: %v (%s)", name, err, b)
		}
	}
}

// MustErr invokes a tool, requires a tool error, and returns its text.
func MustErr(t testing.TB, cs *mcp.ClientSession, name string, args any, contains ...string) string {
	t.Helper()
	res := Call(t, cs, name, args)
	if !res.IsError {
		t.Fatalf("%s: expected error, got %s", name, Text(res))
	}
	txt := Text(res)
	for _, c := range contains {
		if !strings.Contains(txt, c) {
			t.Fatalf("%s: error %q does not contain %q", name, txt, c)
		}
	}
	return txt
}

// Text concatenates the text content of a result.
func Text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// ToolMap lists tools from the server via the protocol.
func ToolMap(t testing.TB, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		m[tl.Name] = tl
	}
	return m
}

// Hints is a compact view of a tool's annotations for table-driven tests.
type Hints struct {
	ReadOnly, Destructive, Idempotent, OpenWorld bool
}

// HintsOf extracts hints, applying MCP defaults for nil pointers.
func HintsOf(t *mcp.Tool) Hints {
	a := t.Annotations
	if a == nil {
		return Hints{Destructive: true, OpenWorld: true}
	}
	h := Hints{ReadOnly: a.ReadOnlyHint, Idempotent: a.IdempotentHint, Destructive: true, OpenWorld: true}
	if a.DestructiveHint != nil {
		h.Destructive = *a.DestructiveHint
	}
	if a.OpenWorldHint != nil {
		h.OpenWorld = *a.OpenWorldHint
	}
	return h
}
