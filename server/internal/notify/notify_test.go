package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"

	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/testdb"
)

type capture struct {
	ch   string
	sent []store.Notification
}

func (c *capture) Channel() string { return c.ch }
func (c *capture) Deliver(_ context.Context, _ store.Person, n store.Notification) error {
	c.sent = append(c.sent, n)
	return nil
}

func TestPreferencesAndDedupe(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	_ = s.UpsertPerson(ctx, store.Person{ID: "alice", Email: "a@example.com"})
	email, teams := &capture{ch: Email}, &capture{ch: Teams}
	n := &Notifier{Store: s, Deliverers: []Deliverer{email, teams}, BaseURL: "https://wb.example.com", Sync: true}
	n.Notify(ctx, []string{"alice", "alice"}, Notice{Kind: ApprovalNeeded, Title: "t", Link: "/approvals/1", DedupeKey: "k"})
	n.Notify(ctx, []string{"alice"}, Notice{Kind: ApprovalNeeded, Title: "t", DedupeKey: "k"})
	if len(email.sent) != 1 || len(teams.sent) != 1 {
		t.Fatalf("dedupe: %d %d", len(email.sent), len(teams.sent))
	}
	if email.sent[0].Link != "https://wb.example.com/approvals/1" {
		t.Fatalf("absolute link: %s", email.sent[0].Link)
	}
	_ = s.SetPreference(ctx, "alice", TaskFinished, []string{InApp})
	n.Notify(ctx, []string{"alice"}, Notice{Kind: TaskFinished, Title: "done"})
	if len(email.sent) != 1 || len(teams.sent) != 1 {
		t.Fatal("preference must suppress other channels")
	}
	list, _, _ := s.ListNotifications(ctx, "alice", false, store.Page{})
	if len(list) != 2 {
		t.Fatalf("in-app copies: %d", len(list))
	}
}

func TestSMTPHeaderInjection(t *testing.T) {
	var msg string
	s := SMTP{Addr: "x:25", From: "wb@example.com", send: func(_ string, _ smtp.Auth, _ string, _ []string, m []byte) error {
		msg = string(m)
		return nil
	}}
	_ = s.Deliver(context.Background(), store.Person{Email: "a@example.com"}, store.Notification{Title: "hi\r\nBcc: evil@example.com", Body: "b"})
	if strings.Contains(msg, "\r\nBcc:") {
		t.Fatal("title must not inject headers")
	}
}

func TestWebhookSigned(t *testing.T) {
	var sig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sig = r.Header.Get("X-Workbench-Signature") }))
	defer srv.Close()
	w := Webhook{Name: Teams, URL: srv.URL, Secret: []byte("s")}
	if err := w.Deliver(context.Background(), store.Person{ID: "a"}, store.Notification{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sig, "sha256=") {
		t.Fatal("unsigned")
	}
}
