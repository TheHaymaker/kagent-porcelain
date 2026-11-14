// Command cloudrun-mcp is the Workbench Cloud Run (preview-only) MCP server.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"time"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/app"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/cloudrun"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/config"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/gcpauth"
)

const name = "cloudrun-mcp"

func main() {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	common := app.CommonFlags(fs)
	project := config.String(fs, "preview-project", "CLOUDRUN_PREVIEW_PROJECT", "", "the ONLY project this server deploys to")
	region := config.String(fs, "region", "CLOUDRUN_REGION", "", "Cloud Run region, e.g. europe-west1")
	prod := config.String(fs, "production-projects", "WB_PRODUCTION_PROJECTS", "", "comma-separated production project ids; startup fails if the preview project is one of them")
	imagePrefix := config.String(fs, "image-prefix", "CLOUDRUN_IMAGE_PREFIX", "", "deployable images must start with this, e.g. europe-west1-docker.pkg.dev/wb-builds/builds/")
	runtimeSA := config.String(fs, "runtime-service-account", "CLOUDRUN_RUNTIME_SERVICE_ACCOUNT", "", "service account preview revisions run as (optional)")
	maxInst := config.Int64(fs, "max-instances", "CLOUDRUN_MAX_INSTANCES", 2, "cap on max instances per preview service")
	public := config.Bool(fs, "allow-public-invoker", "CLOUDRUN_ALLOW_PUBLIC_INVOKER", false, "allow allUsers/allAuthenticatedUsers invoker grants")
	domains := config.String(fs, "invoker-domains", "CLOUDRUN_INVOKER_DOMAINS", "", "comma-separated email domains allowed as invokers (empty: any)")
	wait := config.Duration(fs, "deploy-wait", "CLOUDRUN_DEPLOY_WAIT", 3*time.Minute, "how long deploy waits for a rollout")
	_ = fs.Parse(os.Args[1:])

	cfg := cloudrun.Config{
		PreviewProject: *project, Region: *region, ProductionProjects: config.List(*prod),
		ImagePrefix: *imagePrefix, RuntimeServiceAccount: *runtimeSA, MaxInstances: int(*maxInst),
		AllowPublicInvoker: *public, InvokerDomains: config.List(*domains), DeployWait: *wait,
	}
	if err := cfg.Validate(); err != nil {
		app.Fatal(name, err)
	}
	hc, err := gcpauth.Client(context.Background())
	if err != nil {
		app.Fatal(name, err)
	}
	s := common.NewServer(name, cloudrun.Instructions, nil, 0)
	cloudrun.Register(s, cloudrun.NewClient(cfg, hc))
	if err := s.ListenAndServe(*common.Listen); err != nil && err != http.ErrServerClosed {
		app.Fatal(name, err)
	}
}
