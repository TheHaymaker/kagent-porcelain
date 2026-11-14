package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

// BigQueryConfig says where to export.
type BigQueryConfig struct {
	Project  string        `json:"project" yaml:"project"`
	Dataset  string        `json:"dataset" yaml:"dataset"`
	Table    string        `json:"table" yaml:"table"`
	Interval time.Duration `json:"interval" yaml:"interval"`
}

// BigQueryExporter ships audit events to BigQuery with the tabledata.insertAll
// API, in sequence order, recording its progress in export_cursor so a restart
// resumes where it stopped. insertId is the event ID, so a batch re-sent after
// a crash is deduplicated by BigQuery.
//
// The HTTP client must carry Google credentials (Workload Identity).
type BigQueryExporter struct {
	Store  *store.Store
	Client *http.Client
	Config BigQueryConfig
	// Endpoint overrides the BigQuery API base, for tests.
	Endpoint string
	Log      *slog.Logger
}

const cursorName = "bigquery"

// Start runs until ctx ends. It implements controller-runtime's Runnable.
func (x *BigQueryExporter) Start(ctx context.Context) error {
	interval := x.Config.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for {
			n, err := x.ExportOnce(ctx)
			if err != nil {
				x.log().ErrorContext(ctx, "audit export failed", "error", err)
				break
			}
			if n < batchSize {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// NeedLeaderElection keeps one exporter across replicas.
func (x *BigQueryExporter) NeedLeaderElection() bool { return true }

const batchSize = 500

// ExportOnce ships one batch and returns how many events it shipped.
func (x *BigQueryExporter) ExportOnce(ctx context.Context) (int, error) {
	after, err := x.Store.ExportCursor(ctx, cursorName)
	if err != nil {
		return 0, err
	}
	events, err := x.Store.ListAudit(ctx, store.AuditFilter{AfterSeq: after, Ascending: true}, store.Page{Size: batchSize})
	if err != nil || len(events) == 0 {
		return 0, err
	}
	rows := make([]map[string]any, len(events))
	for i, e := range events {
		detail, _ := json.Marshal(e.Detail)
		rows[i] = map[string]any{"insertId": e.ID, "json": map[string]any{
			"id": e.ID, "seq": e.Seq, "time": e.Time.UTC().Format(time.RFC3339Nano), "type": e.Type,
			"actor_id": e.ActorID, "on_behalf_of_id": e.OnBehalfOfID, "session_id": e.SessionID, "task_id": e.TaskID,
			"app_id": e.AppID, "approval_id": e.ApprovalID, "summary": e.Summary, "detail": string(detail), "surface": e.Surface,
		}}
	}
	body, _ := json.Marshal(map[string]any{"kind": "bigquery#tableDataInsertAllRequest", "rows": rows})
	base := x.Endpoint
	if base == "" {
		base = "https://bigquery.googleapis.com"
	}
	url := fmt.Sprintf("%s/bigquery/v2/projects/%s/datasets/%s/tables/%s/insertAll", base, x.Config.Project, x.Config.Dataset, x.Config.Table)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := x.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("bigquery insertAll: %s: %s", resp.Status, respBody)
	}
	var result struct {
		InsertErrors []json.RawMessage `json:"insertErrors"`
	}
	if err := json.Unmarshal(respBody, &result); err == nil && len(result.InsertErrors) > 0 {
		// Do not advance past rows BigQuery refused: the next pass retries
		// the whole batch, and insertId keeps the good rows from duplicating.
		return 0, fmt.Errorf("bigquery insertAll refused %d rows: %s", len(result.InsertErrors), result.InsertErrors[0])
	}
	return len(events), x.Store.SetExportCursor(ctx, cursorName, events[len(events)-1].Seq)
}

func (x *BigQueryExporter) log() *slog.Logger {
	if x.Log != nil {
		return x.Log
	}
	return slog.Default()
}

// TableSchema is the BigQuery schema the exporter writes, for provisioning.
const TableSchema = `[
  {"name":"id","type":"STRING","mode":"REQUIRED"},
  {"name":"seq","type":"INT64","mode":"REQUIRED"},
  {"name":"time","type":"TIMESTAMP","mode":"REQUIRED"},
  {"name":"type","type":"STRING","mode":"REQUIRED"},
  {"name":"actor_id","type":"STRING"},
  {"name":"on_behalf_of_id","type":"STRING"},
  {"name":"session_id","type":"STRING"},
  {"name":"task_id","type":"STRING"},
  {"name":"app_id","type":"STRING"},
  {"name":"approval_id","type":"STRING"},
  {"name":"summary","type":"STRING"},
  {"name":"detail","type":"JSON"},
  {"name":"surface","type":"STRING"}
]`
