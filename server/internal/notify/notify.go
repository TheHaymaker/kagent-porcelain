// Package notify tells people when an agent needs them or finishes. Every
// notice is stored for the in-app list; people choose which other channels
// (email, Teams, mobile push) each kind also goes to.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"slices"
	"strings"
	"time"

	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

// Kinds match the NotificationKind enum, lower-cased without prefix.
const (
	ApprovalNeeded  = "approval_needed"
	ApprovalDecided = "approval_decided"
	InputNeeded     = "input_needed"
	TaskFinished    = "task_finished"
	TaskFailed      = "task_failed"
	AppPromoted     = "app_promoted"
	LimitReached    = "limit_reached"
)

// Channels.
const (
	InApp      = "in_app"
	Email      = "email"
	Teams      = "teams"
	MobilePush = "mobile_push"
)

// Defaults are the channels a person gets before choosing their own.
var Defaults = map[string][]string{
	ApprovalNeeded:  {InApp, Email, Teams, MobilePush},
	ApprovalDecided: {InApp, Teams, MobilePush},
	InputNeeded:     {InApp, Teams, MobilePush},
	TaskFinished:    {InApp, MobilePush},
	TaskFailed:      {InApp, Email},
	AppPromoted:     {InApp, Email},
	LimitReached:    {InApp, Email},
}

// Notice is what to say.
type Notice struct {
	Kind       string
	Title      string
	Body       string
	Link       string
	SessionID  string
	ApprovalID string
	AppID      string
	// DedupeKey makes repeated notices (a push delivered twice) a no-op.
	DedupeKey string
}

// Deliverer sends a stored notification on one channel.
type Deliverer interface {
	Channel() string
	Deliver(ctx context.Context, to store.Person, n store.Notification) error
}

// Notifier stores notices and fans them out.
type Notifier struct {
	Store      *store.Store
	Deliverers []Deliverer
	// BaseURL turns a link path into an absolute URL for email and push.
	BaseURL string
	Log     *slog.Logger
	// Async delivery is skipped in tests.
	Sync bool
}

// Notify tells each person. It never fails the caller: a notice that cannot
// be sent is logged, and the in-app copy remains.
func (n *Notifier) Notify(ctx context.Context, personIDs []string, notice Notice) {
	slices.Sort(personIDs)
	for _, id := range slices.Compact(personIDs) {
		if id == "" {
			continue
		}
		stored, created, err := n.Store.AddNotification(ctx, store.Notification{
			PersonID: id, Kind: notice.Kind, Title: notice.Title, Body: notice.Body, Link: notice.Link,
			SessionID: notice.SessionID, ApprovalID: notice.ApprovalID, AppID: notice.AppID, DedupeKey: notice.DedupeKey,
		})
		if err != nil {
			n.log().ErrorContext(ctx, "store notification", "person", id, "error", err)
			continue
		}
		if !created || len(n.Deliverers) == 0 {
			continue
		}
		deliver := func(ctx context.Context) { n.fanOut(ctx, stored) }
		if n.Sync {
			deliver(ctx)
		} else {
			go func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer cancel()
				deliver(ctx)
			}()
		}
	}
}

func (n *Notifier) fanOut(ctx context.Context, sn store.Notification) {
	prefs, err := n.Store.Preferences(ctx, sn.PersonID)
	if err != nil {
		n.log().ErrorContext(ctx, "read preferences", "error", err)
		return
	}
	channels, ok := prefs[sn.Kind]
	if !ok {
		channels = Defaults[sn.Kind]
	}
	person, err := n.Store.GetPerson(ctx, sn.PersonID)
	if err != nil {
		n.log().WarnContext(ctx, "notify unknown person", "person", sn.PersonID, "error", err)
		return
	}
	if n.BaseURL != "" && strings.HasPrefix(sn.Link, "/") {
		sn.Link = strings.TrimRight(n.BaseURL, "/") + sn.Link
	}
	for _, d := range n.Deliverers {
		if !slices.Contains(channels, d.Channel()) {
			continue
		}
		if err := d.Deliver(ctx, person, sn); err != nil {
			n.log().WarnContext(ctx, "notification delivery failed", "channel", d.Channel(), "person", sn.PersonID, "error", err)
		}
	}
}

func (n *Notifier) log() *slog.Logger {
	if n.Log != nil {
		return n.Log
	}
	return slog.Default()
}

// SMTP delivers email.
type SMTP struct {
	Addr string // host:port
	From string
	Auth smtp.Auth
	// send is swapped in tests.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// Channel implements Deliverer.
func (SMTP) Channel() string { return Email }

// Deliver implements Deliverer.
func (s SMTP) Deliver(_ context.Context, to store.Person, n store.Notification) error {
	if to.Email == "" {
		return nil
	}
	send := s.send
	if send == nil {
		send = smtp.SendMail
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", s.From, to.Email, headerSafe(n.Title), n.Body)
	if n.Link != "" {
		fmt.Fprintf(&b, "\r\nOpen in Workbench: %s\r\n", n.Link)
	}
	return send(s.Addr, s.Auth, s.From, []string{to.Email}, b.Bytes())
}

func headerSafe(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

// Webhook posts notifications to a service that owns a channel: the Teams
// bot (which holds each person's conversation reference) or a push relay.
// The body is signed with HMAC-SHA256 in X-Workbench-Signature.
type Webhook struct {
	Name   string // channel name
	URL    string
	Secret []byte
	Client *http.Client
}

// Channel implements Deliverer.
func (w Webhook) Channel() string { return w.Name }

// Deliver implements Deliverer.
func (w Webhook) Deliver(ctx context.Context, to store.Person, n store.Notification) error {
	body, _ := json.Marshal(map[string]any{
		"personId": to.ID, "email": to.Email, "kind": n.Kind, "title": n.Title, "body": n.Body, "link": n.Link,
		"sessionId": n.SessionID, "approvalId": n.ApprovalID, "appId": n.AppID, "notificationId": n.ID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workbench-Signature", Sign(w.Secret, body))
	c := w.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s webhook: %s", w.Name, resp.Status)
	}
	return nil
}

// Sign returns the hex HMAC-SHA256 of body.
func Sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// FCM sends mobile push through Firebase Cloud Messaging's HTTP v1 API to
// every device a person registered. Client must carry Google credentials.
type FCM struct {
	Project  string
	Client   *http.Client
	Store    *store.Store
	Endpoint string
}

// Channel implements Deliverer.
func (FCM) Channel() string { return MobilePush }

// Deliver implements Deliverer.
func (f FCM) Deliver(ctx context.Context, to store.Person, n store.Notification) error {
	devices, err := f.Store.Devices(ctx, to.ID)
	if err != nil {
		return err
	}
	base := f.Endpoint
	if base == "" {
		base = "https://fcm.googleapis.com"
	}
	for _, d := range devices {
		body, _ := json.Marshal(map[string]any{"message": map[string]any{
			"token":        d.PushToken,
			"notification": map[string]any{"title": n.Title, "body": n.Body},
			"data":         map[string]string{"link": n.Link, "kind": n.Kind, "approvalId": n.ApprovalID, "sessionId": n.SessionID},
		}})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/v1/projects/%s/messages:send", base, f.Project), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := f.Client.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		// FCM answers 404 UNREGISTERED for a dead token.
		if resp.StatusCode == http.StatusNotFound {
			_ = f.Store.RemoveDevice(ctx, d.PushToken)
		} else if resp.StatusCode/100 != 2 {
			return fmt.Errorf("fcm: %s", resp.Status)
		}
	}
	return nil
}
