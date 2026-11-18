// Package app holds the shared main() scaffolding of the tool servers.
package app

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/config"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
)

// Version is set at build time with -ldflags "-X .../internal/app.Version=...".
var Version = "dev"

// Common are flags every server has.
type Common struct {
	Listen           *string
	RequireRequester *bool
	LogLevel         *string
}

// CommonFlags registers the shared flags on fs.
func CommonFlags(fs *flag.FlagSet) Common {
	return Common{
		Listen:           config.String(fs, "listen", "LISTEN_ADDR", ":8080", "HTTP listen address; MCP is served at /mcp, liveness at /healthz"),
		RequireRequester: config.Bool(fs, "require-requester", "REQUIRE_REQUESTER", false, "reject non-read-only tool calls that carry no X-User-Id header"),
		LogLevel:         config.String(fs, "log-level", "LOG_LEVEL", "info", "debug, info, warn or error"),
	}
}

// Logger returns the JSON audit/operational logger.
func (c Common) Logger(name string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*c.LogLevel)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})).With("server", name, "version", Version)
}

// NewServer builds the mcpx server from common flags.
func (c Common) NewServer(name, instructions string, red *mcpx.Redactor, maxRequestBytes int64) *mcpx.Server {
	return mcpx.NewServer(mcpx.Options{
		Name: name, Version: Version, Instructions: instructions,
		Logger: c.Logger(name), Redactor: red, RequireRequester: *c.RequireRequester,
		MaxRequestBytes: maxRequestBytes,
	})
}

// Fatal prints err and exits.
func Fatal(name string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
	os.Exit(1)
}
