// Command gitlab-mcp is the Workbench GitLab MCP server (Streamable HTTP).
package main

import (
	"flag"
	"net/http"
	"os"
	"time"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/app"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/config"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/gitlab"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
)

const name = "gitlab-mcp"

func main() {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	common := app.CommonFlags(fs)
	baseURL := config.String(fs, "gitlab-url", "GITLAB_BASE_URL", "", "GitLab base URL, e.g. https://gitlab.example.com")
	group := config.String(fs, "allowed-group", "GITLAB_ALLOWED_GROUP", "", "full path of the group Workbench projects live in")
	tmplGroup := config.String(fs, "template-group", "GITLAB_TEMPLATE_GROUP", "", "full path of the group holding scaffold templates")
	tokenFile := config.String(fs, "token-file", "GITLAB_TOKEN_FILE", "/var/run/secrets/workbench/gitlab/token", "file containing the GitLab token (mounted Secret)")
	_ = fs.Parse(os.Args[1:])

	cfg := gitlab.Config{BaseURL: *baseURL, AllowedGroup: *group, TemplateGroup: *tmplGroup, TokenFile: *tokenFile}
	if err := cfg.Validate(); err != nil {
		app.Fatal(name, err)
	}
	tok := gitlab.FileToken(cfg.TokenFile)
	if _, err := tok(); err != nil {
		app.Fatal(name, err) // fail fast; the error names the path, never the value
	}
	red := mcpx.NewRedactor()
	red.AddSource(func() []string {
		v, err := tok()
		if err != nil {
			return nil
		}
		return []string{v}
	})
	s := common.NewServer(name, gitlab.Instructions, red, gitlab.MaxRequestBytes)
	gitlab.Register(s, gitlab.NewClient(cfg, &http.Client{Timeout: 60 * time.Second}, tok))
	if err := s.ListenAndServe(*common.Listen); err != nil && err != http.ErrServerClosed {
		app.Fatal(name, err)
	}
}
