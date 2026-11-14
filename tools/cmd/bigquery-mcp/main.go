// Command bigquery-mcp is the Workbench BigQuery MCP server.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"time"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/app"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/bigquery"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/config"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/gcpauth"
)

const name = "bigquery-mcp"

func main() {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	common := app.CommonFlags(fs)
	project := config.String(fs, "project", "BQ_PROJECT", "", "project queries run and are billed in, and datasets live in")
	location := config.String(fs, "location", "BQ_LOCATION", "", "BigQuery location, e.g. EU")
	maxBytes := config.Int64(fs, "max-bytes-billed", "BQ_MAX_BYTES_BILLED", 1<<30, "per-query maximum bytes billed")
	maxRows := config.Int64(fs, "max-rows", "BQ_MAX_ROWS", 200, "max rows returned by bigquery_run_query")
	prefix := config.String(fs, "dataset-prefix", "BQ_DATASET_PREFIX", "wb_", "prefix of datasets Workbench may create and delete tables from")
	timeout := config.Duration(fs, "query-timeout", "BQ_QUERY_TIMEOUT", 60*time.Second, "how long bigquery_run_query waits for a job")
	_ = fs.Parse(os.Args[1:])

	cfg := bigquery.Config{Project: *project, Location: *location, MaxBytesBilled: *maxBytes, MaxRows: int(*maxRows),
		DatasetPrefix: *prefix, QueryTimeout: *timeout}
	if err := cfg.Validate(); err != nil {
		app.Fatal(name, err)
	}
	hc, err := gcpauth.Client(context.Background(), "https://www.googleapis.com/auth/bigquery")
	if err != nil {
		app.Fatal(name, err)
	}
	s := common.NewServer(name, bigquery.Instructions, nil, 0)
	bigquery.Register(s, bigquery.NewClient(cfg, hc))
	if err := s.ListenAndServe(*common.Listen); err != nil && err != http.ErrServerClosed {
		app.Fatal(name, err)
	}
}
