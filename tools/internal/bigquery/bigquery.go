// Package bigquery implements the bigquery-mcp tools against the BigQuery
// REST API v2: browsing, dry runs, capped read-only queries, dataset creation
// (spend) and table deletion (data deletion). Everything runs in one
// configured project; jobs are billed there with a maximum-bytes-billed cap.
package bigquery

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/httpx"
)

// Config is server-side configuration.
type Config struct {
	Project  string // project jobs run (and are billed) in, and datasets live in
	Location string // e.g. EU, US, europe-west1
	// MaxBytesBilled caps every query job. Required, > 0.
	MaxBytesBilled int64
	// MaxRows bounds rows returned by run_query (default 200).
	MaxRows int
	// MaxResultBytes bounds the total returned cell bytes (default 256 KiB).
	MaxResultBytes int
	// DatasetPrefix: create_dataset and delete_table only operate on datasets
	// whose id starts with it (default "wb_").
	DatasetPrefix string
	// QueryTimeout bounds how long run_query waits for a job (default 60s).
	QueryTimeout time.Duration
	BaseURL      string // default https://bigquery.googleapis.com
	// PollInterval between getQueryResults calls.
	PollInterval time.Duration
}

var (
	projectRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	locationRe = regexp.MustCompile(`^[A-Za-z]+(-[a-z]+[0-9]+)?$`)
	prefixRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,30}$`)
)

// Validate checks configuration and fills defaults.
func (c *Config) Validate() error {
	if !projectRe.MatchString(c.Project) {
		return fmt.Errorf("bigquery: invalid project %q", c.Project)
	}
	if !locationRe.MatchString(c.Location) {
		return fmt.Errorf("bigquery: invalid location %q", c.Location)
	}
	if c.MaxBytesBilled <= 0 {
		return fmt.Errorf("bigquery: max bytes billed must be positive")
	}
	if c.MaxRows <= 0 {
		c.MaxRows = 200
	}
	if c.MaxResultBytes <= 0 {
		c.MaxResultBytes = 256 << 10
	}
	if c.DatasetPrefix == "" {
		c.DatasetPrefix = "wb_"
	}
	if !prefixRe.MatchString(c.DatasetPrefix) {
		return fmt.Errorf("bigquery: invalid dataset prefix %q", c.DatasetPrefix)
	}
	if c.QueryTimeout <= 0 {
		c.QueryTimeout = 60 * time.Second
	}
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://bigquery.googleapis.com"
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return nil
}

// Client calls BigQuery.
type Client struct {
	cfg  Config
	http httpx.Doer
}

// NewClient builds a client. cfg must have been validated.
func NewClient(cfg Config, d httpx.Doer) *Client { return &Client{cfg: cfg, http: d} }

func (c *Client) call(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := c.cfg.BaseURL + "/bigquery/v2/projects/" + c.cfg.Project + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	_, err := httpx.Do(ctx, c.http, httpx.Request{Method: method, URL: u, Body: body}, out)
	return err
}

// ---- API shapes ----

type field struct {
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Mode        string  `json:"mode,omitempty"`
	Description string  `json:"description,omitempty"`
	Fields      []field `json:"fields,omitempty"`
}

type tableRef struct {
	ProjectID string `json:"projectId"`
	DatasetID string `json:"datasetId"`
	TableID   string `json:"tableId"`
}

type dryRunResult struct {
	StatementType    string
	TotalBytes       int64
	ReferencedTables []string
}

// dryRun validates and estimates a query without running it.
func (c *Client) dryRun(ctx context.Context, sql string) (dryRunResult, error) {
	body := map[string]any{
		"configuration": map[string]any{
			"dryRun": true,
			"query":  map[string]any{"query": sql, "useLegacySql": false},
		},
		"jobReference": map[string]any{"projectId": c.cfg.Project, "location": c.cfg.Location},
	}
	var res struct {
		Statistics struct {
			Query struct {
				StatementType       string     `json:"statementType"`
				TotalBytesProcessed string     `json:"totalBytesProcessed"`
				ReferencedTables    []tableRef `json:"referencedTables"`
			} `json:"query"`
		} `json:"statistics"`
	}
	if err := c.call(ctx, http.MethodPost, "/jobs", nil, body, &res); err != nil {
		return dryRunResult{}, err
	}
	q := res.Statistics.Query
	out := dryRunResult{StatementType: q.StatementType, TotalBytes: parseInt(q.TotalBytesProcessed)}
	for i, t := range q.ReferencedTables {
		if i >= 50 {
			break
		}
		out.ReferencedTables = append(out.ReferencedTables, t.ProjectID+"."+t.DatasetID+"."+t.TableID)
	}
	return out, nil
}

func parseInt(s string) int64 {
	var n int64
	_, _ = fmt.Sscan(s, &n)
	return n
}

// HumanBytes renders a byte count for people.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
