package bigquery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/httpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/validate"
)

// Limits.
const (
	MaxQueryBytes  = 100 << 10
	MaxCellBytes   = 2000
	MaxSchemaCols  = 500
	MaxListResults = 200
)

// Instructions is advertised to MCP clients.
const Instructions = `BigQuery tools for Workbench. Always bigquery_dry_run_query before bigquery_run_query to check the bytes estimate.
bigquery_run_query only runs a single read-only SELECT (Standard SQL) with a server-enforced maximum-bytes-billed cap and bounded rows.
Datasets you create get the configured prefix; tables can only be deleted from such datasets (approval-gated).`

// Register adds the tools.
func Register(s *mcpx.Server, c *Client) {
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "bigquery_list_datasets",
		Description: "List datasets in the Workbench BigQuery project (paginated).",
		Annotations: mcpx.ReadOnly("List BigQuery datasets", false),
	}, c.listDatasets)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "bigquery_list_tables",
		Description: "List tables and views in a dataset (paginated).",
		Annotations: mcpx.ReadOnly("List BigQuery tables", false),
	}, c.listTables)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "bigquery_get_schema",
		Description: fmt.Sprintf("Get a table's schema (up to %d columns, nested fields flattened with dots), row count, size and partitioning.", MaxSchemaCols),
		Annotations: mcpx.ReadOnly("Get BigQuery table schema", false),
	}, c.getSchema)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "bigquery_dry_run_query",
		Description: "Validate a Standard SQL query and estimate the bytes it would process, without running it or incurring cost.",
		Annotations: mcpx.ReadOnly("Dry-run BigQuery query", true),
	}, c.dryRunQuery)
	mcpx.AddTool(s, &mcp.Tool{
		Name: "bigquery_run_query",
		Description: fmt.Sprintf("Run a single read-only SELECT query (Standard SQL). DML, DDL and scripts are rejected. "+
			"Jobs are capped at %s billed; results are limited to %d rows and %s.",
			HumanBytes(c.cfg.MaxBytesBilled), c.cfg.MaxRows, HumanBytes(int64(c.cfg.MaxResultBytes))),
		Annotations: mcpx.ReadOnly("Run read-only BigQuery query", true),
	}, c.runQuery)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "bigquery_create_dataset",
		Description: fmt.Sprintf("Create a dataset (id must start with %q) in the Workbench project. New spend; approval-gated.", c.cfg.DatasetPrefix),
		Annotations: mcpx.Mutating("Create BigQuery dataset", false, false, false),
	}, c.createDataset)
	mcpx.AddTool(s, &mcp.Tool{
		Name:        "bigquery_delete_table",
		Description: fmt.Sprintf("Delete a table from a Workbench dataset (id starting with %q). Irreversible data deletion; approval-gated.", c.cfg.DatasetPrefix),
		Annotations: mcpx.Mutating("Delete BigQuery table", true, true, false),
	}, c.deleteTable)
}

// ---- listing ----

// ListDatasetsIn are the arguments of bigquery_list_datasets.
type ListDatasetsIn struct {
	PageSize  int    `json:"page_size,omitempty" jsonschema:"default 50, max 200"`
	PageToken string `json:"page_token,omitempty"`
}

// Dataset is a dataset summary.
type Dataset struct {
	ID       string            `json:"id"`
	Location string            `json:"location,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// ListDatasetsOut is the result of bigquery_list_datasets.
type ListDatasetsOut struct {
	Project       string    `json:"project"`
	Datasets      []Dataset `json:"datasets"`
	NextPageToken string    `json:"next_page_token,omitempty"`
}

func pageQuery(size int, token string) (url.Values, error) {
	if err := validate.PageToken("page_token", token); err != nil {
		return nil, err
	}
	q := url.Values{"maxResults": {strconv.Itoa(validate.Bounded(size, 50, MaxListResults))}}
	if token != "" {
		q.Set("pageToken", token)
	}
	return q, nil
}

func (c *Client) listDatasets(ctx context.Context, _ mcpx.Call, in ListDatasetsIn) (ListDatasetsOut, error) {
	q, err := pageQuery(in.PageSize, in.PageToken)
	if err != nil {
		return ListDatasetsOut{}, err
	}
	var res struct {
		Datasets []struct {
			DatasetReference struct {
				DatasetID string `json:"datasetId"`
			} `json:"datasetReference"`
			Location string            `json:"location"`
			Labels   map[string]string `json:"labels"`
		} `json:"datasets"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := c.call(ctx, http.MethodGet, "/datasets", q, nil, &res); err != nil {
		return ListDatasetsOut{}, err
	}
	out := ListDatasetsOut{Project: c.cfg.Project, Datasets: make([]Dataset, 0, len(res.Datasets)), NextPageToken: res.NextPageToken}
	for _, d := range res.Datasets {
		out.Datasets = append(out.Datasets, Dataset{ID: d.DatasetReference.DatasetID, Location: d.Location, Labels: d.Labels})
	}
	return out, nil
}

// ListTablesIn are the arguments of bigquery_list_tables.
type ListTablesIn struct {
	Dataset   string `json:"dataset" jsonschema:"dataset id"`
	PageSize  int    `json:"page_size,omitempty" jsonschema:"default 50, max 200"`
	PageToken string `json:"page_token,omitempty"`
}

// Table is a table summary.
type Table struct {
	ID           string `json:"id"`
	Type         string `json:"type" jsonschema:"TABLE, VIEW, MATERIALIZED_VIEW, EXTERNAL or SNAPSHOT"`
	CreationTime string `json:"creation_time,omitempty"`
}

// ListTablesOut is the result of bigquery_list_tables.
type ListTablesOut struct {
	Dataset       string  `json:"dataset"`
	Tables        []Table `json:"tables"`
	TotalItems    int     `json:"total_items,omitempty"`
	NextPageToken string  `json:"next_page_token,omitempty"`
}

func checkDataset(field, v string) error {
	return validate.Match(field, v, validate.BQDataset, "a dataset id (letters, digits, underscores)")
}

func checkTable(field, v string) error {
	return validate.Match(field, v, validate.BQTable, "a table id (letters, digits, underscores, hyphens; no decorators)")
}

func msToRFC3339(ms string) string {
	n := parseInt(ms)
	if n == 0 {
		return ""
	}
	return time.UnixMilli(n).UTC().Format(time.RFC3339)
}

func (c *Client) listTables(ctx context.Context, _ mcpx.Call, in ListTablesIn) (ListTablesOut, error) {
	if err := checkDataset("dataset", in.Dataset); err != nil {
		return ListTablesOut{}, err
	}
	q, err := pageQuery(in.PageSize, in.PageToken)
	if err != nil {
		return ListTablesOut{}, err
	}
	var res struct {
		Tables []struct {
			TableReference tableRef `json:"tableReference"`
			Type           string   `json:"type"`
			CreationTime   string   `json:"creationTime"`
		} `json:"tables"`
		TotalItems    int    `json:"totalItems"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := c.call(ctx, http.MethodGet, "/datasets/"+in.Dataset+"/tables", q, nil, &res); err != nil {
		if httpx.IsNotFound(err) {
			return ListTablesOut{}, fmt.Errorf("dataset %q not found", in.Dataset)
		}
		return ListTablesOut{}, err
	}
	out := ListTablesOut{Dataset: in.Dataset, Tables: make([]Table, 0, len(res.Tables)), TotalItems: res.TotalItems, NextPageToken: res.NextPageToken}
	for _, t := range res.Tables {
		out.Tables = append(out.Tables, Table{ID: t.TableReference.TableID, Type: t.Type, CreationTime: msToRFC3339(t.CreationTime)})
	}
	return out, nil
}

// TableIn identifies a table.
type TableIn struct {
	Dataset string `json:"dataset" jsonschema:"dataset id"`
	Table   string `json:"table" jsonschema:"table id"`
}

// Column is one (possibly nested, dot-joined) column.
type Column struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Mode        string `json:"mode,omitempty"`
	Description string `json:"description,omitempty"`
}

// SchemaOut is the result of bigquery_get_schema.
type SchemaOut struct {
	Table          string   `json:"table" jsonschema:"project.dataset.table"`
	Type           string   `json:"type"`
	Description    string   `json:"description,omitempty"`
	NumRows        int64    `json:"num_rows"`
	NumBytes       int64    `json:"num_bytes"`
	PartitionField string   `json:"partition_field,omitempty"`
	PartitionType  string   `json:"partition_type,omitempty"`
	Columns        []Column `json:"columns"`
	Truncated      bool     `json:"truncated"`
}

func flatten(prefix string, fs []field, out *[]Column, truncated *bool) {
	for _, f := range fs {
		if len(*out) >= MaxSchemaCols {
			*truncated = true
			return
		}
		name := f.Name
		if prefix != "" {
			name = prefix + "." + f.Name
		}
		*out = append(*out, Column{Name: name, Type: f.Type, Mode: f.Mode, Description: httpx.Truncate(f.Description, 200)})
		if len(f.Fields) > 0 {
			flatten(name, f.Fields, out, truncated)
		}
	}
}

func (c *Client) getSchema(ctx context.Context, _ mcpx.Call, in TableIn) (SchemaOut, error) {
	if err := checkDataset("dataset", in.Dataset); err != nil {
		return SchemaOut{}, err
	}
	if err := checkTable("table", in.Table); err != nil {
		return SchemaOut{}, err
	}
	var res struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		NumRows     string `json:"numRows"`
		NumBytes    string `json:"numBytes"`
		Schema      struct {
			Fields []field `json:"fields"`
		} `json:"schema"`
		TimePartitioning *struct {
			Type  string `json:"type"`
			Field string `json:"field"`
		} `json:"timePartitioning"`
	}
	if err := c.call(ctx, http.MethodGet, "/datasets/"+in.Dataset+"/tables/"+in.Table, nil, nil, &res); err != nil {
		if httpx.IsNotFound(err) {
			return SchemaOut{}, fmt.Errorf("table %s.%s not found", in.Dataset, in.Table)
		}
		return SchemaOut{}, err
	}
	out := SchemaOut{Table: c.cfg.Project + "." + in.Dataset + "." + in.Table, Type: res.Type, Description: httpx.Truncate(res.Description, 500),
		NumRows: parseInt(res.NumRows), NumBytes: parseInt(res.NumBytes), Columns: []Column{}}
	if res.TimePartitioning != nil {
		out.PartitionType = res.TimePartitioning.Type
		out.PartitionField = res.TimePartitioning.Field
		if out.PartitionField == "" {
			out.PartitionField = "_PARTITIONTIME"
		}
	}
	flatten("", res.Schema.Fields, &out.Columns, &out.Truncated)
	return out, nil
}

// ---- queries ----

// QueryIn is a query.
type QueryIn struct {
	Query string `json:"query" jsonschema:"GoogleSQL (Standard SQL) query text"`
}

// DryRunOut is the result of bigquery_dry_run_query.
type DryRunOut struct {
	StatementType       string   `json:"statement_type"`
	TotalBytesProcessed int64    `json:"total_bytes_processed"`
	HumanBytes          string   `json:"human_bytes"`
	MaxBytesBilled      int64    `json:"max_bytes_billed" jsonschema:"server-enforced per-query cap"`
	WithinCap           bool     `json:"within_cap" jsonschema:"false means bigquery_run_query would be refused"`
	RunnableReadOnly    bool     `json:"runnable_read_only" jsonschema:"true if bigquery_run_query would accept this statement"`
	ReferencedTables    []string `json:"referenced_tables,omitempty"`
}

func checkQueryText(q string) error {
	if strings.TrimSpace(q) == "" {
		return validate.Errorf("query", "is required")
	}
	if len(q) > MaxQueryBytes {
		return validate.Errorf("query", "is longer than %d bytes", MaxQueryBytes)
	}
	return nil
}

func (c *Client) dryRunQuery(ctx context.Context, _ mcpx.Call, in QueryIn) (DryRunOut, error) {
	if err := checkQueryText(in.Query); err != nil {
		return DryRunOut{}, err
	}
	dr, err := c.dryRun(ctx, in.Query)
	if err != nil {
		return DryRunOut{}, err
	}
	return DryRunOut{
		StatementType: dr.StatementType, TotalBytesProcessed: dr.TotalBytes, HumanBytes: HumanBytes(dr.TotalBytes),
		MaxBytesBilled: c.cfg.MaxBytesBilled, WithinCap: dr.TotalBytes <= c.cfg.MaxBytesBilled,
		RunnableReadOnly: dr.StatementType == "SELECT" && CheckReadOnly(in.Query) == nil,
		ReferencedTables: dr.ReferencedTables,
	}, nil
}

// RunQueryIn are the arguments of bigquery_run_query.
type RunQueryIn struct {
	Query   string `json:"query" jsonschema:"a single read-only SELECT (GoogleSQL)"`
	MaxRows int    `json:"max_rows,omitempty" jsonschema:"rows to return; capped by server policy"`
}

// RunQueryOut is the result of bigquery_run_query.
type RunQueryOut struct {
	JobID               string           `json:"job_id"`
	Columns             []Column         `json:"columns"`
	Rows                []map[string]any `json:"rows"`
	TotalRows           int64            `json:"total_rows"`
	ReturnedRows        int              `json:"returned_rows"`
	Truncated           bool             `json:"truncated" jsonschema:"true if rows or cell values were cut to stay within limits"`
	TotalBytesProcessed int64            `json:"total_bytes_processed"`
	CacheHit            bool             `json:"cache_hit"`
}

type queryResponse struct {
	JobComplete  bool `json:"jobComplete"`
	JobReference struct {
		JobID    string `json:"jobId"`
		Location string `json:"location"`
	} `json:"jobReference"`
	Schema struct {
		Fields []field `json:"fields"`
	} `json:"schema"`
	Rows                []json.RawMessage `json:"rows"`
	TotalRows           string            `json:"totalRows"`
	TotalBytesProcessed string            `json:"totalBytesProcessed"`
	CacheHit            bool              `json:"cacheHit"`
	Errors              []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

var jobIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,1000}$`)

func (c *Client) runQuery(ctx context.Context, call mcpx.Call, in RunQueryIn) (RunQueryOut, error) {
	if err := checkQueryText(in.Query); err != nil {
		return RunQueryOut{}, err
	}
	if err := CheckReadOnly(in.Query); err != nil {
		return RunQueryOut{}, validate.Errorf("query", "%s", err.Error())
	}
	// Authoritative check: BigQuery's own parse of the statement.
	dr, err := c.dryRun(ctx, in.Query)
	if err != nil {
		return RunQueryOut{}, err
	}
	if dr.StatementType != "SELECT" {
		return RunQueryOut{}, validate.Errorf("query", "only SELECT statements may run (BigQuery reports %s)", dr.StatementType)
	}
	if dr.TotalBytes > c.cfg.MaxBytesBilled {
		return RunQueryOut{}, fmt.Errorf("query would process %s, above the %s cap; add filters (partition column, fewer columns) and dry-run again",
			HumanBytes(dr.TotalBytes), HumanBytes(c.cfg.MaxBytesBilled))
	}
	maxRows := validate.Bounded(in.MaxRows, c.cfg.MaxRows, c.cfg.MaxRows)
	ctx, cancel := context.WithTimeout(ctx, c.cfg.QueryTimeout)
	defer cancel()
	body := map[string]any{
		"query":              in.Query,
		"useLegacySql":       false,
		"maximumBytesBilled": strconv.FormatInt(c.cfg.MaxBytesBilled, 10),
		"maxResults":         maxRows + 1,
		"timeoutMs":          int(min(c.cfg.QueryTimeout, 30*time.Second) / time.Millisecond),
		"location":           c.cfg.Location,
		"labels":             map[string]string{"wb-requester": validate.LabelValue(call.Requester), "wb-tool": "bigquery_run_query"},
	}
	var res queryResponse
	if err := c.call(ctx, http.MethodPost, "/queries", nil, body, &res); err != nil {
		return RunQueryOut{}, err
	}
	for !res.JobComplete {
		if !jobIDRe.MatchString(res.JobReference.JobID) {
			return RunQueryOut{}, fmt.Errorf("unexpected job id %q", validate.Clip(res.JobReference.JobID, 80))
		}
		select {
		case <-ctx.Done():
			return RunQueryOut{}, fmt.Errorf("query job %s did not finish within %s", res.JobReference.JobID, c.cfg.QueryTimeout)
		case <-time.After(c.cfg.PollInterval):
		}
		q := url.Values{"location": {c.cfg.Location}, "maxResults": {strconv.Itoa(maxRows + 1)}, "timeoutMs": {"10000"}}
		jobID := res.JobReference.JobID
		res = queryResponse{}
		if err := c.call(ctx, http.MethodGet, "/queries/"+jobID, q, nil, &res); err != nil {
			return RunQueryOut{}, err
		}
		if res.JobReference.JobID == "" {
			res.JobReference.JobID = jobID
		}
	}
	if len(res.Errors) > 0 {
		return RunQueryOut{}, fmt.Errorf("query failed: %s", httpx.Truncate(res.Errors[0].Message, 600))
	}
	out := RunQueryOut{JobID: res.JobReference.JobID, Columns: []Column{}, Rows: []map[string]any{},
		TotalRows: parseInt(res.TotalRows), TotalBytesProcessed: parseInt(res.TotalBytesProcessed), CacheHit: res.CacheHit}
	var colTrunc bool
	for _, f := range res.Schema.Fields {
		if len(out.Columns) >= MaxSchemaCols {
			colTrunc = true
			break
		}
		out.Columns = append(out.Columns, Column{Name: f.Name, Type: f.Type, Mode: f.Mode})
	}
	budget := c.cfg.MaxResultBytes
	for i, raw := range res.Rows {
		if i >= maxRows {
			out.Truncated = true
			break
		}
		row, size, cut := convertRow(res.Schema.Fields, raw)
		if size > budget {
			out.Truncated = true
			break
		}
		budget -= size
		out.Truncated = out.Truncated || cut
		out.Rows = append(out.Rows, row)
	}
	out.ReturnedRows = len(out.Rows)
	if out.TotalRows > int64(out.ReturnedRows) || colTrunc {
		out.Truncated = true
	}
	return out, nil
}

// convertRow turns BigQuery's {"f":[{"v":...}]} encoding into a map keyed by
// column name, truncating long cells. It returns the approximate size.
func convertRow(fields []field, raw json.RawMessage) (map[string]any, int, bool) {
	var r struct {
		F []struct {
			V any `json:"v"`
		} `json:"f"`
	}
	_ = json.Unmarshal(raw, &r)
	row := make(map[string]any, len(fields))
	size, cut := 0, false
	for i, f := range fields {
		if i >= len(r.F) {
			break
		}
		v, s, c := convertValue(f, r.F[i].V)
		row[f.Name] = v
		size += s + len(f.Name)
		cut = cut || c
	}
	return row, size, cut
}

func convertValue(f field, v any) (any, int, bool) {
	switch x := v.(type) {
	case nil:
		return nil, 4, false
	case string:
		if len(x) > MaxCellBytes {
			return httpx.Truncate(x, MaxCellBytes), MaxCellBytes, true
		}
		return x, len(x), false
	case []any: // REPEATED: [{"v": ...}]
		out := make([]any, 0, len(x))
		size, cut := 0, false
		elem := f
		elem.Mode = ""
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				ev, s, c := convertValue(elem, m["v"])
				out = append(out, ev)
				size += s
				cut = cut || c
			}
			if size > MaxCellBytes*10 {
				return out, size, true
			}
		}
		return out, size, cut
	case map[string]any: // RECORD: {"f": [...]}
		b, _ := json.Marshal(x)
		row, size, cut := convertRow(f.Fields, b)
		return row, size, cut
	default:
		b, _ := json.Marshal(x)
		return string(b), len(b), false
	}
}

// ---- create dataset ----

// CreateDatasetIn are the arguments of bigquery_create_dataset.
type CreateDatasetIn struct {
	DatasetID                  string `json:"dataset_id" jsonschema:"new dataset id; must start with the configured prefix"`
	Description                string `json:"description,omitempty"`
	DefaultTableExpirationDays int    `json:"default_table_expiration_days,omitempty" jsonschema:"tables expire after this many days (0 = never)"`
}

// CreateDatasetOut is the result of bigquery_create_dataset.
type CreateDatasetOut struct {
	Dataset  string `json:"dataset" jsonschema:"project.dataset"`
	Location string `json:"location"`
}

func (c *Client) createDataset(ctx context.Context, call mcpx.Call, in CreateDatasetIn) (CreateDatasetOut, error) {
	if err := checkDataset("dataset_id", in.DatasetID); err != nil {
		return CreateDatasetOut{}, err
	}
	if !strings.HasPrefix(in.DatasetID, c.cfg.DatasetPrefix) {
		return CreateDatasetOut{}, validate.Errorf("dataset_id", "must start with %q", c.cfg.DatasetPrefix)
	}
	if err := validate.Text("description", in.Description, false, 1000); err != nil {
		return CreateDatasetOut{}, err
	}
	if in.DefaultTableExpirationDays < 0 || in.DefaultTableExpirationDays > 3650 {
		return CreateDatasetOut{}, validate.Errorf("default_table_expiration_days", "must be 0-3650")
	}
	body := map[string]any{
		"datasetReference": map[string]any{"projectId": c.cfg.Project, "datasetId": in.DatasetID},
		"location":         c.cfg.Location,
		"description":      in.Description,
		"labels":           map[string]string{"wb-managed": "true", "wb-requester": validate.LabelValue(call.Requester)},
	}
	if in.DefaultTableExpirationDays > 0 {
		body["defaultTableExpirationMs"] = strconv.FormatInt(int64(in.DefaultTableExpirationDays)*24*3600*1000, 10)
	}
	var res struct {
		Location string `json:"location"`
	}
	if err := c.call(ctx, http.MethodPost, "/datasets", nil, body, &res); err != nil {
		if httpx.StatusOf(err) == http.StatusConflict {
			return CreateDatasetOut{}, fmt.Errorf("dataset %q already exists", in.DatasetID)
		}
		return CreateDatasetOut{}, err
	}
	return CreateDatasetOut{Dataset: c.cfg.Project + "." + in.DatasetID, Location: res.Location}, nil
}

// ---- delete table ----

// DeleteTableOut is the result of bigquery_delete_table.
type DeleteTableOut struct {
	Table   string `json:"table" jsonschema:"project.dataset.table"`
	Deleted bool   `json:"deleted"`
}

func (c *Client) deleteTable(ctx context.Context, _ mcpx.Call, in TableIn) (DeleteTableOut, error) {
	if err := checkDataset("dataset", in.Dataset); err != nil {
		return DeleteTableOut{}, err
	}
	if err := checkTable("table", in.Table); err != nil {
		return DeleteTableOut{}, err
	}
	if !strings.HasPrefix(in.Dataset, c.cfg.DatasetPrefix) {
		return DeleteTableOut{}, validate.Errorf("dataset", "tables can only be deleted from Workbench datasets (prefix %q)", c.cfg.DatasetPrefix)
	}
	if err := c.call(ctx, http.MethodDelete, "/datasets/"+in.Dataset+"/tables/"+in.Table, nil, nil, nil); err != nil {
		if httpx.IsNotFound(err) {
			return DeleteTableOut{}, fmt.Errorf("table %s.%s not found", in.Dataset, in.Table)
		}
		return DeleteTableOut{}, err
	}
	return DeleteTableOut{Table: c.cfg.Project + "." + in.Dataset + "." + in.Table, Deleted: true}, nil
}
