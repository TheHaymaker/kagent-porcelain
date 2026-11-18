package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/testdb"
)

func TestExportAdvancesOnlyOnSuccess(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.AppendAudit(ctx, store.AuditEvent{Type: "x", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	fail := true
	var got [][]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Rows []any }
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body.Rows)
		if fail {
			_, _ = w.Write([]byte(`{"insertErrors":[{"index":0}]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	x := &BigQueryExporter{Store: s, Client: srv.Client(), Endpoint: srv.URL, Config: BigQueryConfig{Project: "p", Dataset: "d", Table: "t"}}
	if _, err := x.ExportOnce(ctx); err == nil {
		t.Fatal("refused rows must be an error")
	}
	if c, _ := s.ExportCursor(ctx, cursorName); c != 0 {
		t.Fatal("cursor must not advance past refused rows")
	}
	fail = false
	n, err := x.ExportOnce(ctx)
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	n, _ = x.ExportOnce(ctx)
	if n != 0 || len(got) != 2 || len(got[1]) != 3 {
		t.Fatalf("second pass must ship nothing new: %d %d", n, len(got))
	}
}
