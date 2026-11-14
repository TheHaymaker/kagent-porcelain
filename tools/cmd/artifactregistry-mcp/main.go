// Command artifactregistry-mcp is the Workbench Artifact Registry (read-only) MCP server.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/app"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/artifactregistry"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/config"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/gcpauth"
)

const name = "artifactregistry-mcp"

func main() {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	common := app.CommonFlags(fs)
	project := config.String(fs, "project", "AR_PROJECT", "", "project of the builds repository")
	location := config.String(fs, "location", "AR_LOCATION", "", "repository location, e.g. europe-west1")
	repo := config.String(fs, "repository", "AR_REPOSITORY", "builds", "repository id")
	_ = fs.Parse(os.Args[1:])

	cfg := artifactregistry.Config{Project: *project, Location: *location, Repository: *repo}
	if err := cfg.Validate(); err != nil {
		app.Fatal(name, err)
	}
	hc, err := gcpauth.Client(context.Background(), "https://www.googleapis.com/auth/cloud-platform.read-only")
	if err != nil {
		app.Fatal(name, err)
	}
	s := common.NewServer(name, artifactregistry.Instructions, nil, 0)
	artifactregistry.Register(s, artifactregistry.NewClient(cfg, hc))
	if err := s.ListenAndServe(*common.Listen); err != nil && err != http.ErrServerClosed {
		app.Fatal(name, err)
	}
}
