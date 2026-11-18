// Command build-mcp is the Workbench image build MCP server: it builds an
// app's image from its GitLab project with Cloud Build.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"time"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/app"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/build"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/config"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/gcpauth"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/gitlab"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
)

const name = "build-mcp"

func main() {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	common := app.CommonFlags(fs)
	baseURL := config.String(fs, "gitlab-url", "GITLAB_BASE_URL", "", "GitLab base URL")
	group := config.String(fs, "allowed-group", "GITLAB_ALLOWED_GROUP", "", "full path of the group Workbench projects live in")
	tmplGroup := config.String(fs, "template-group", "GITLAB_TEMPLATE_GROUP", "", "full path of the template group")
	tokenFile := config.String(fs, "token-file", "GITLAB_TOKEN_FILE", "/var/run/secrets/workbench/gitlab/token", "file containing the GitLab token")
	project := config.String(fs, "project", "BUILD_PROJECT", "", "project running Cloud Build and holding the builds repository")
	location := config.String(fs, "location", "BUILD_LOCATION", "", "builds repository location")
	repo := config.String(fs, "repository", "BUILD_REPOSITORY", "builds", "builds repository id")
	bucket := config.String(fs, "bucket", "BUILD_BUCKET", "", "staging bucket for build sources")
	sa := config.String(fs, "service-account", "BUILD_SERVICE_ACCOUNT", "", "service account builds run as")
	_ = fs.Parse(os.Args[1:])

	gcfg := gitlab.Config{BaseURL: *baseURL, AllowedGroup: *group, TemplateGroup: *tmplGroup, TokenFile: *tokenFile}
	if err := gcfg.Validate(); err != nil {
		app.Fatal(name, err)
	}
	tok := gitlab.FileToken(gcfg.TokenFile)
	if _, err := tok(); err != nil {
		app.Fatal(name, err)
	}
	cfg := build.Config{Project: *project, Location: *location, Repository: *repo, Bucket: *bucket, ServiceAccount: *sa}
	if err := cfg.Validate(); err != nil {
		app.Fatal(name, err)
	}
	hc, err := gcpauth.Client(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		app.Fatal(name, err)
	}
	red := mcpx.NewRedactor()
	red.AddSource(func() []string {
		if v, err := tok(); err == nil {
			return []string{v}
		}
		return nil
	})
	s := common.NewServer(name, build.Instructions, red, 0)
	src := gitlab.NewClient(gcfg, &http.Client{Timeout: 120 * time.Second}, tok)
	build.Register(s, build.NewClient(cfg, src, hc))
	if err := s.ListenAndServe(*common.Listen); err != nil && err != http.ErrServerClosed {
		app.Fatal(name, err)
	}
}
