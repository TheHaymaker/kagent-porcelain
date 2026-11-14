// Package mcpx is the shared MCP plumbing for the Workbench tool servers:
// Streamable HTTP serving, a typed tool registration wrapper that extracts the
// caller identity, writes an audit log line per call, maps errors to readable
// and redacted tool errors, and helpers for tool annotations.
package mcpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/validate"
)

// UserIDHeader is the header kagent forwards with the calling user's id.
const UserIDHeader = "X-User-Id"

// UnknownRequester is used when a call arrives without X-User-Id.
const UnknownRequester = "unknown"

// Call carries per-call context into tool handlers.
type Call struct {
	Tool string
	// Requester is the sanitized X-User-Id of the caller, or "unknown". It is
	// caller-asserted (kagent sets it); use it for audit and attribution only,
	// never for authorization.
	Requester string
}

// Server wraps an *mcp.Server with the shared policy.
type Server struct {
	MCP    *mcp.Server
	Logger *slog.Logger
	// Redactor scrubs secrets from error text returned to callers.
	Redactor *Redactor
	// RequireRequester makes non-read-only tools fail when X-User-Id is absent.
	RequireRequester bool
	// MaxRequestBytes bounds MCP request bodies (0: SDK default, 4 MiB).
	MaxRequestBytes int64

	mu    sync.Mutex
	tools []*mcp.Tool
}

// Options configure NewServer.
type Options struct {
	Name             string
	Version          string
	Instructions     string
	Logger           *slog.Logger
	Redactor         *Redactor
	RequireRequester bool
	// MaxRequestBytes bounds MCP request bodies (0: SDK default, 4 MiB).
	MaxRequestBytes int64
}

// NewServer creates a server.
func NewServer(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	if o.Redactor == nil {
		o.Redactor = NewRedactor()
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	s := mcp.NewServer(&mcp.Implementation{Name: o.Name, Version: o.Version},
		&mcp.ServerOptions{Instructions: o.Instructions})
	return &Server{MCP: s, Logger: o.Logger, Redactor: o.Redactor, RequireRequester: o.RequireRequester, MaxRequestBytes: o.MaxRequestBytes}
}

// Tools returns the registered tool definitions (for tests and docs).
func (s *Server) Tools() []*mcp.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*mcp.Tool(nil), s.tools...)
}

// Handler is a typed tool implementation.
type Handler[In, Out any] func(ctx context.Context, call Call, in In) (Out, error)

// toolName enforces the Workbench naming convention <server>_<verb>_<noun>.
var toolName = regexp.MustCompile(`^[a-z]+_[a-z]+_[a-z_]+$`)

// AddTool registers a typed tool with the shared wrapper. Output is returned
// as structured content (and mirrored as JSON text by the SDK).
func AddTool[In, Out any](s *Server, t *mcp.Tool, h Handler[In, Out]) {
	if !toolName.MatchString(t.Name) {
		panic(fmt.Sprintf("tool name %q does not follow <server>_<verb>_<noun>", t.Name))
	}
	if t.Annotations == nil {
		panic(fmt.Sprintf("tool %q has no annotations", t.Name))
	}
	readOnly := t.Annotations.ReadOnlyHint
	s.mu.Lock()
	s.tools = append(s.tools, t)
	s.mu.Unlock()

	mcp.AddTool(s.MCP, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		call := Call{Tool: t.Name, Requester: requesterFrom(req)}
		start := time.Now()
		var out Out
		var err error
		if s.RequireRequester && !readOnly && call.Requester == UnknownRequester {
			err = errors.New("this tool requires a caller identity (X-User-Id header) and none was provided")
		} else {
			out, err = safeCall(ctx, h, call, in)
		}
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		s.Logger.LogAttrs(ctx, slog.LevelInfo, "tool_call",
			slog.String("audit", "workbench.tool"),
			slog.String("tool", t.Name),
			slog.String("requester", call.Requester),
			slog.Bool("read_only", readOnly),
			slog.String("outcome", outcome),
			slog.Duration("duration", time.Since(start)),
			slog.String("error", errText(s.Redactor, err)),
		)
		if err != nil {
			return nil, zero, errors.New(userError(s.Redactor, t.Name, err))
		}
		return nil, out, nil
	})
}

func safeCall[In, Out any](ctx context.Context, h Handler[In, Out], call Call, in In) (out Out, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error in %s", call.Tool)
		}
	}()
	return h(ctx, call, in)
}

func errText(r *Redactor, err error) string {
	if err == nil {
		return ""
	}
	return r.Redact(err.Error())
}

func userError(r *Redactor, tool string, err error) string {
	msg := r.Redact(err.Error())
	if validate.IsInvalid(err) {
		return "invalid argument: " + msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return tool + ": timed out: " + msg
	}
	return tool + " failed: " + msg
}

// requesterFrom extracts and sanitizes X-User-Id.
func requesterFrom(req *mcp.CallToolRequest) string {
	if req == nil || req.Extra == nil || req.Extra.Header == nil {
		return UnknownRequester
	}
	return SanitizeRequester(req.Extra.Header.Get(UserIDHeader))
}

// SanitizeRequester bounds and cleans a caller-asserted identity so it is safe
// to put in logs, commit trailers and labels.
func SanitizeRequester(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return UnknownRequester
	}
	var b strings.Builder
	for _, r := range v {
		if b.Len() >= 128 {
			break
		}
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return UnknownRequester
	}
	return b.String()
}

// Annotation helpers. Booleans are explicit so each tool states its hints.

// ReadOnly annotates a tool that does not modify its environment.
func ReadOnly(title string, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		IdempotentHint:  true,
		DestructiveHint: ptr(false),
		OpenWorldHint:   ptr(openWorld),
	}
}

// Mutating annotates a tool that changes state.
func Mutating(title string, destructive, idempotent, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		IdempotentHint:  idempotent,
		DestructiveHint: ptr(destructive),
		OpenWorldHint:   ptr(openWorld),
	}
}

func ptr[T any](v T) *T { return &v }

// HTTPHandler returns the mux serving MCP at /mcp and liveness at /healthz.
// The MCP endpoint is stateless Streamable HTTP so replicas need no affinity.
func (s *Server) HTTPHandler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.MCP },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: s.Logger, MaxRequestBodyBytes: s.MaxRequestBytes})
	mux := http.NewServeMux()
	mux.Handle("/mcp", h)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// ListenAndServe serves until SIGINT/SIGTERM, then shuts down gracefully.
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.HTTPHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.Logger.Info("listening", "addr", addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
