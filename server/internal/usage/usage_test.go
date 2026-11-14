package usage

import (
	"context"
	"testing"
	"time"

	"github.com/thehaymaker/kagent-porcelain/server/internal/notify"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/testdb"
)

type count struct{ n int }

func (c *count) Notify(context.Context, []string, notify.Notice) { c.n++ }

func TestLimits(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	_ = s.UpsertPerson(ctx, store.Person{ID: "alice", Groups: []string{"finance"}})
	_ = s.UpsertPerson(ctx, store.Person{ID: "bob", Groups: []string{"finance"}})
	_ = s.SetLimit(ctx, store.Limit{Subject: "team", SubjectID: "finance", MonthlyCents: 1000})
	_ = s.SetLimit(ctx, store.Limit{Subject: "person", SubjectID: "alice", MonthlyCents: 5000})
	n := &count{}
	m := &Meter{Store: s, Notify: n}
	if err := m.Record(ctx, store.UsageRecord{PersonID: "bob", Model: "m", CostMicros: 6_000_000}); err != nil {
		t.Fatal(err)
	}
	st, _ := m.Check(ctx, "alice", nil)
	if st.Over {
		t.Fatalf("not yet over: %+v", st)
	}
	_ = m.Record(ctx, store.UsageRecord{PersonID: "alice", Model: "m", CostMicros: 5_000_000})
	st, _ = m.Check(ctx, "alice", nil)
	if !st.Over || st.Subject != "team:finance" {
		t.Fatalf("team limit is spent across the team: %+v", st)
	}
	if n.n != 1 {
		t.Fatalf("alice warned once, got %d", n.n)
	}
	if err := m.Record(ctx, store.UsageRecord{PersonID: "alice", Model: "m", CostMicros: -1}); err == nil {
		t.Fatal("negative usage")
	}
	if MonthStart(time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)).Day() != 1 {
		t.Fatal("month start")
	}
}
