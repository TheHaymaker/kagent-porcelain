package bigquery

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx/mcpxtest"
)

func TestCheckReadOnly(t *testing.T) {
	allowed := []string{
		"SELECT 1",
		"select * from `p.d.t` limit 10;",
		"WITH a AS (SELECT 1 AS x) SELECT x FROM a",
		"(SELECT 1) UNION ALL (SELECT 2)",
		"SELECT 'DELETE FROM x; DROP TABLE y' AS s",
		`SELECT "insert" , '''multi
		 line UPDATE''' FROM t`,
		"SELECT `delete`, `update` FROM t",
		"-- DROP TABLE x\nSELECT 1",
		"/* CREATE TABLE x */ SELECT 1",
		"# comment with MERGE\nSELECT 1",
		"SELECT * FROM t FOR SYSTEM_TIME AS OF TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 HOUR)",
		"SELECT REPEAT('a', 3), REPLACE(s, 'a', 'b') FROM t",
		`SELECT r'\d+ DELETE', b"x" FROM t`,
		"SELECT 'it\\'s DROP' FROM t",
	}
	for _, q := range allowed {
		if err := CheckReadOnly(q); err != nil {
			t.Errorf("CheckReadOnly(%q) = %v, want nil", q, err)
		}
	}
	rejected := []string{
		"",
		"   ",
		"DELETE FROM d.t WHERE true",
		"delete d.t where true",
		"INSERT INTO d.t VALUES (1)",
		"UPDATE d.t SET x = 1 WHERE true",
		"MERGE d.t USING s ON false WHEN NOT MATCHED THEN INSERT ROW",
		"CREATE TABLE d.t AS SELECT 1",
		"CREATE OR REPLACE VIEW d.v AS SELECT 1",
		"DROP TABLE d.t",
		"ALTER TABLE d.t ADD COLUMN y INT64",
		"TRUNCATE TABLE d.t",
		"GRANT `roles/bigquery.dataViewer` ON TABLE d.t TO 'user:x@y.com'",
		"EXPORT DATA OPTIONS(uri='gs://b/*') AS SELECT 1",
		"LOAD DATA INTO d.t FROM FILES(uris=['gs://x'])",
		"SELECT 1; DROP TABLE d.t",
		"SELECT 1; SELECT 2",
		"WITH a AS (SELECT 1) DELETE FROM d.t WHERE true",
		"DECLARE x INT64; SELECT x",
		"BEGIN SELECT 1; END",
		"EXECUTE IMMEDIATE 'DROP TABLE d.t'",
		"CALL d.proc()",
		"SET @@dataset_id = 'x'; SELECT 1",
		"SELECT 'unterminated",
		"SELECT 1 /* unterminated",
		"SELECT `unterminated",
		"SELECT 1 -- ok\n; DELETE FROM d.t WHERE true",
	}
	for _, q := range rejected {
		if err := CheckReadOnly(q); err == nil {
			t.Errorf("CheckReadOnly(%q) = nil, want error", q)
		}
	}
}

type fakeBQ struct {
	mu       sync.Mutex
	reqs     []string
	bodies   []map[string]any
	stmtType string
	bytes    string
	polls    int
	rows     []map[string]any
}

func (f *fakeBQ) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	f.bodies = append(f.bodies, body)
	enc := json.NewEncoder(w)
	base := "/bigquery/v2/projects/wb-data"
	p := r.URL.Path
	schema := map[string]any{"fields": []map[string]any{
		{"name": "name", "type": "STRING"},
		{"name": "n", "type": "INTEGER"},
		{"name": "tags", "type": "STRING", "mode": "REPEATED"},
		{"name": "addr", "type": "RECORD", "fields": []map[string]any{{"name": "city", "type": "STRING"}}},
	}}
	switch {
	case p == base+"/jobs" && r.Method == http.MethodPost:
		_ = enc.Encode(map[string]any{"statistics": map[string]any{"query": map[string]any{
			"statementType": f.stmtType, "totalBytesProcessed": f.bytes,
			"referencedTables": []map[string]any{{"projectId": "wb-data", "datasetId": "sales", "tableId": "orders"}},
		}}})
	case p == base+"/queries" && r.Method == http.MethodPost:
		_ = enc.Encode(map[string]any{"jobComplete": false, "jobReference": map[string]any{"jobId": "job_123", "location": "EU"}})
	case p == base+"/queries/job_123":
		f.polls++
		_ = enc.Encode(map[string]any{"jobComplete": true, "jobReference": map[string]any{"jobId": "job_123"},
			"schema": schema, "rows": f.rows, "totalRows": "1000", "totalBytesProcessed": "2048"})
	case p == base+"/datasets" && r.Method == http.MethodGet:
		_ = enc.Encode(map[string]any{"datasets": []map[string]any{{"datasetReference": map[string]any{"datasetId": "sales"}, "location": "EU"}}, "nextPageToken": "n2"})
	case p == base+"/datasets" && r.Method == http.MethodPost:
		if body["datasetReference"].(map[string]any)["datasetId"] == "wb_exists" {
			w.WriteHeader(409)
			_ = enc.Encode(map[string]any{"error": map[string]any{"code": 409, "message": "Already Exists"}})
			return
		}
		_ = enc.Encode(map[string]any{"location": "EU"})
	case p == base+"/datasets/sales/tables":
		_ = enc.Encode(map[string]any{"tables": []map[string]any{{"tableReference": map[string]any{"tableId": "orders"}, "type": "TABLE", "creationTime": "1700000000000"}}, "totalItems": 1})
	case p == base+"/datasets/sales/tables/orders" && r.Method == http.MethodGet:
		_ = enc.Encode(map[string]any{"type": "TABLE", "numRows": "42", "numBytes": "1024", "schema": schema,
			"timePartitioning": map[string]any{"type": "DAY", "field": "created"}})
	case p == base+"/datasets/wb_scratch/tables/tmp" && r.Method == http.MethodDelete:
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
		_ = enc.Encode(map[string]any{"error": map[string]any{"code": 404, "status": "NOT_FOUND", "message": "Not found: " + p}})
	}
}

func setup(t *testing.T) (*fakeBQ, *mcp.ClientSession) {
	f := &fakeBQ{stmtType: "SELECT", bytes: "1048576"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := Config{Project: "wb-data", Location: "EU", MaxBytesBilled: 1 << 30, MaxRows: 5, MaxResultBytes: 64 << 10,
		BaseURL: srv.URL, PollInterval: time.Millisecond, QueryTimeout: 5 * time.Second}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s := mcpx.NewServer(mcpx.Options{Name: "bigquery-mcp", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	Register(s, NewClient(cfg, srv.Client()))
	return f, mcpxtest.Connect(t, s, "Alice@Example.com")
}

func (f *fakeBQ) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func TestToolsAndAnnotations(t *testing.T) {
	_, cs := setup(t)
	tools := mcpxtest.ToolMap(t, cs)
	want := map[string]mcpxtest.Hints{
		"bigquery_list_datasets":  {ReadOnly: true, Idempotent: true},
		"bigquery_list_tables":    {ReadOnly: true, Idempotent: true},
		"bigquery_get_schema":     {ReadOnly: true, Idempotent: true},
		"bigquery_dry_run_query":  {ReadOnly: true, Idempotent: true, OpenWorld: true},
		"bigquery_run_query":      {ReadOnly: true, Idempotent: true, OpenWorld: true},
		"bigquery_create_dataset": {ReadOnly: false, Destructive: false, Idempotent: false},
		"bigquery_delete_table":   {ReadOnly: false, Destructive: true, Idempotent: true},
	}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools", len(tools))
	}
	for name, h := range want {
		if got := mcpxtest.HintsOf(tools[name]); got != h {
			t.Errorf("%s: %+v want %+v", name, got, h)
		}
	}
}

func TestRunQueryRejectsDMLBeforeAnyAPICall(t *testing.T) {
	f, cs := setup(t)
	for _, q := range []string{"DELETE FROM sales.orders WHERE true", "DROP TABLE sales.orders", "SELECT 1; DELETE FROM sales.orders WHERE true", "INSERT sales.orders (x) VALUES (1)"} {
		mcpxtest.MustErr(t, cs, "bigquery_run_query", map[string]any{"query": q}, "invalid argument")
	}
	if n := f.count(""); n != 0 {
		t.Fatalf("DML reached BigQuery (%d requests)", n)
	}
}

func TestRunQueryRejectsNonSelectPerDryRun(t *testing.T) {
	f, cs := setup(t)
	f.stmtType = "SCRIPT" // lexer passed it but BigQuery disagrees
	mcpxtest.MustErr(t, cs, "bigquery_run_query", map[string]any{"query": "SELECT 1"}, "BigQuery reports SCRIPT")
	if f.count("POST /bigquery/v2/projects/wb-data/queries") != 0 {
		t.Fatal("query ran despite non-SELECT dry run")
	}
}

func TestRunQueryRejectsOverCap(t *testing.T) {
	f, cs := setup(t)
	f.bytes = "5368709120" // 5 GiB
	mcpxtest.MustErr(t, cs, "bigquery_run_query", map[string]any{"query": "SELECT * FROM sales.orders"}, "5.0 GiB", "1.0 GiB cap")
	if f.count("POST /bigquery/v2/projects/wb-data/queries") != 0 {
		t.Fatal("over-cap query ran")
	}
}

func TestRunQuery(t *testing.T) {
	f, cs := setup(t)
	for i := 0; i < 7; i++ {
		f.rows = append(f.rows, map[string]any{"f": []map[string]any{
			{"v": strings.Repeat("z", 3000)}, {"v": "7"}, {"v": []map[string]any{{"v": "a"}, {"v": "b"}}}, {"v": map[string]any{"f": []map[string]any{{"v": "Paris"}}}},
		}})
	}
	var out RunQueryOut
	mcpxtest.MustOK(t, cs, "bigquery_run_query", map[string]any{"query": "SELECT * FROM sales.orders", "max_rows": 100}, &out)
	if out.JobID != "job_123" || out.ReturnedRows != 5 || !out.Truncated || out.TotalRows != 1000 || len(out.Columns) != 4 {
		t.Fatalf("unexpected %+v", out)
	}
	r := out.Rows[0]
	if s := r["name"].(string); len(s) > MaxCellBytes+20 {
		t.Fatalf("cell not truncated: %d", len(s))
	}
	if r["n"] != "7" || len(r["tags"].([]any)) != 2 || r["addr"].(map[string]any)["city"] != "Paris" {
		t.Fatalf("row conversion %+v", r)
	}
	if f.polls == 0 {
		t.Fatal("did not poll for completion")
	}
	// The job request carries the cap, location and requester label.
	var qb map[string]any
	for i, req := range f.reqs {
		if strings.HasPrefix(req, "POST /bigquery/v2/projects/wb-data/queries") {
			qb = f.bodies[i]
		}
	}
	if qb["maximumBytesBilled"] != "1073741824" || qb["location"] != "EU" || qb["useLegacySql"] != false || qb["maxResults"] != float64(6) {
		t.Fatalf("query body %+v", qb)
	}
	if qb["labels"].(map[string]any)["wb-requester"] != "alice_example_com" {
		t.Fatalf("labels %+v", qb["labels"])
	}
}

func TestRunQueryResultByteBudget(t *testing.T) {
	f, cs := setup(t)
	for i := 0; i < 5; i++ {
		f.rows = append(f.rows, map[string]any{"f": []map[string]any{{"v": strings.Repeat("q", 1500)}, {"v": nil}, {"v": []any{}}, {"v": nil}}})
	}
	// Shrink the budget by using a tiny-budget server.
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg := Config{Project: "wb-data", Location: "EU", MaxBytesBilled: 1 << 30, MaxRows: 5, MaxResultBytes: 4000, BaseURL: srv.URL, PollInterval: time.Millisecond}
	_ = cfg.Validate()
	s := mcpx.NewServer(mcpx.Options{Name: "bq", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	Register(s, NewClient(cfg, srv.Client()))
	cs = mcpxtest.Connect(t, s, "x")
	var out RunQueryOut
	mcpxtest.MustOK(t, cs, "bigquery_run_query", map[string]any{"query": "SELECT 1"}, &out)
	if out.ReturnedRows != 2 || !out.Truncated {
		t.Fatalf("expected byte budget to cut at 2 rows, got %d (truncated=%v)", out.ReturnedRows, out.Truncated)
	}
}

func TestDryRun(t *testing.T) {
	f, cs := setup(t)
	var out DryRunOut
	mcpxtest.MustOK(t, cs, "bigquery_dry_run_query", map[string]any{"query": "SELECT * FROM sales.orders"}, &out)
	if out.StatementType != "SELECT" || out.TotalBytesProcessed != 1048576 || out.HumanBytes != "1.0 MiB" || !out.WithinCap || !out.RunnableReadOnly || out.ReferencedTables[0] != "wb-data.sales.orders" {
		t.Fatalf("unexpected %+v", out)
	}
	if body := f.bodies[0]; body["configuration"].(map[string]any)["dryRun"] != true {
		t.Fatalf("not a dry run: %+v", body)
	}
	f.stmtType = "DELETE"
	out = DryRunOut{}
	mcpxtest.MustOK(t, cs, "bigquery_dry_run_query", map[string]any{"query": "DELETE FROM sales.orders WHERE true"}, &out)
	if out.RunnableReadOnly {
		t.Fatal("DELETE must not be reported runnable")
	}
	mcpxtest.MustErr(t, cs, "bigquery_dry_run_query", map[string]any{"query": strings.Repeat("x", MaxQueryBytes+1)}, "longer than")
}

func TestBrowse(t *testing.T) {
	_, cs := setup(t)
	var ds ListDatasetsOut
	mcpxtest.MustOK(t, cs, "bigquery_list_datasets", map[string]any{}, &ds)
	if ds.Project != "wb-data" || len(ds.Datasets) != 1 || ds.Datasets[0].ID != "sales" || ds.NextPageToken != "n2" {
		t.Fatalf("unexpected %+v", ds)
	}
	var ts ListTablesOut
	mcpxtest.MustOK(t, cs, "bigquery_list_tables", map[string]any{"dataset": "sales"}, &ts)
	if len(ts.Tables) != 1 || ts.Tables[0].ID != "orders" || ts.Tables[0].CreationTime != "2023-11-14T22:13:20Z" {
		t.Fatalf("unexpected %+v", ts)
	}
	var sc SchemaOut
	mcpxtest.MustOK(t, cs, "bigquery_get_schema", map[string]any{"dataset": "sales", "table": "orders"}, &sc)
	if sc.NumRows != 42 || sc.PartitionField != "created" || len(sc.Columns) != 5 || sc.Columns[4].Name != "addr.city" {
		t.Fatalf("unexpected %+v", sc)
	}
	mcpxtest.MustErr(t, cs, "bigquery_get_schema", map[string]any{"dataset": "sales", "table": "missing"}, "not found")
	for _, args := range []map[string]any{{"dataset": "sales;drop", "table": "x"}, {"dataset": "sales", "table": "t$20240101"}, {"dataset": "../x", "table": "t"}} {
		mcpxtest.MustErr(t, cs, "bigquery_get_schema", args, "invalid argument")
	}
}

func TestCreateDataset(t *testing.T) {
	f, cs := setup(t)
	var out CreateDatasetOut
	mcpxtest.MustOK(t, cs, "bigquery_create_dataset", map[string]any{"dataset_id": "wb_expenses", "description": "x", "default_table_expiration_days": 30}, &out)
	if out.Dataset != "wb-data.wb_expenses" || out.Location != "EU" {
		t.Fatalf("unexpected %+v", out)
	}
	body := f.bodies[len(f.bodies)-1]
	if body["location"] != "EU" || body["defaultTableExpirationMs"] != "2592000000" || body["labels"].(map[string]any)["wb-requester"] != "alice_example_com" {
		t.Fatalf("body %+v", body)
	}
	mcpxtest.MustErr(t, cs, "bigquery_create_dataset", map[string]any{"dataset_id": "prod_sales"}, "must start with \"wb_\"")
	mcpxtest.MustErr(t, cs, "bigquery_create_dataset", map[string]any{"dataset_id": "wb_exists"}, "already exists")
}

func TestDeleteTable(t *testing.T) {
	f, cs := setup(t)
	var out DeleteTableOut
	mcpxtest.MustOK(t, cs, "bigquery_delete_table", map[string]any{"dataset": "wb_scratch", "table": "tmp"}, &out)
	if !out.Deleted || out.Table != "wb-data.wb_scratch.tmp" {
		t.Fatalf("unexpected %+v", out)
	}
	n := f.count("DELETE")
	mcpxtest.MustErr(t, cs, "bigquery_delete_table", map[string]any{"dataset": "sales", "table": "orders"}, "Workbench datasets")
	mcpxtest.MustErr(t, cs, "bigquery_delete_table", map[string]any{"dataset": "wb_scratch", "table": "t$2024"}, "invalid argument")
	if f.count("DELETE") != n {
		t.Fatal("refused deletes reached BigQuery")
	}
	mcpxtest.MustErr(t, cs, "bigquery_delete_table", map[string]any{"dataset": "wb_scratch", "table": "gone"}, "not found")
}

func TestConfigValidate(t *testing.T) {
	c := Config{Project: "wb-data", Location: "EU"}
	if c.Validate() == nil {
		t.Fatal("max bytes billed must be required")
	}
}
