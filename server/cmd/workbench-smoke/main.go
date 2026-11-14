// Command workbench-smoke checks a deployed Workbench end to end, through
// the same APIs a client uses.
//
//	workbench-smoke keys -dir DIR    make the test issuer's signing key and JWKS
//	workbench-smoke run  [flags]     run the checks against a live server
//
// The run signs tokens for two test users, alice (a requester) and bob (an
// approver), with the key from `keys`. The server trusts them because its
// configuration lists the test issuer with jwksFile pointing at the JWKS.
//
// What it checks, in order:
//  1. both users can sign in, with the roles their groups give them;
//  2. alice starts a conversation with the smoke agent, which asks to put an
//     app into production; kagent pauses the task;
//  3. the pause becomes a production-deploy approval, found by listing the
//     session's approvals (the read-time sync path);
//  4. alice may not decide it, and the server refuses when she tries;
//  5. bob finds it in his inbox and approves it;
//  6. the decision reaches the paused task, which resumes and completes;
//  7. the audit trail records the request, bob's decision on alice's
//     behalf, and the delivery;
//  8. kagent's push notification arrives and alice is told the task finished.
package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/lestrrat-go/jwx/jwt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	wb "github.com/thehaymaker/kagent-porcelain/server/gen/workbench/v1"
	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keys":
		err = keys(os.Args[2:])
	case "run":
		err = run(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: workbench-smoke keys -dir DIR | run [flags]")
	os.Exit(2)
}

// keys writes signing-key.json (private) and jwks.json (public).
func keys(args []string) error {
	fs := flag.NewFlagSet("keys", flag.ExitOnError)
	dir := fs.String("dir", ".", "directory to write the keys to")
	_ = fs.Parse(args)
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	priv, err := jwk.New(raw)
	if err != nil {
		return err
	}
	_ = priv.Set(jwk.KeyIDKey, "workbench-smoke")
	_ = priv.Set(jwk.AlgorithmKey, jwa.RS256)
	pub, err := jwk.PublicKeyOf(priv)
	if err != nil {
		return err
	}
	set := jwk.NewSet()
	set.Add(pub)
	if err := writeJSON(filepath.Join(*dir, "signing-key.json"), priv, 0o600); err != nil {
		return err
	}
	return writeJSON(filepath.Join(*dir, "jwks.json"), set, 0o644)
}

func writeJSON(path string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, mode)
}

type config struct {
	api, agent, keyFile, issuer, audience string
	requesterGroup, approverGroup         string
	timeout                               time.Duration
}

func run(args []string) error {
	var c config
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	fs.StringVar(&c.api, "api", "http://localhost:8083", "Workbench server address (a port-forward to the controller)")
	fs.StringVar(&c.agent, "agent", "workbench-smoke/workbench-smoke", "smoke agent, namespace/name")
	fs.StringVar(&c.keyFile, "key", "signing-key.json", "test issuer signing key from `keys`")
	fs.StringVar(&c.issuer, "issuer", "https://smoke.workbench.test", "test issuer URL, as configured on the server")
	fs.StringVar(&c.audience, "audience", "workbench", "token audience the server accepts")
	fs.StringVar(&c.requesterGroup, "requester-group", "workbench-requesters", "group that makes alice a requester")
	fs.StringVar(&c.approverGroup, "approver-group", "workbench-approvers", "group that makes bob an approver")
	fs.DurationVar(&c.timeout, "timeout", 3*time.Minute, "how long to wait for each asynchronous step")
	_ = fs.Parse(args)

	raw, err := os.ReadFile(c.keyFile)
	if err != nil {
		return fmt.Errorf("read signing key (run `keys` first): %w", err)
	}
	key, err := jwk.ParseKey(raw)
	if err != nil {
		return err
	}
	u, err := url.Parse(c.api)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(u.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	s := &smoke{c: c, key: key, conn: conn}
	s.alice = s.user("smoke-alice", "Alice Smoke", []string{c.requesterGroup})
	s.bob = s.user("smoke-bob", "Bob Smoke", []string{c.requesterGroup, c.approverGroup})
	return s.all(context.Background())
}

type user struct {
	name, token string
	creds       grpc.CallOption
}

type bearer string

func (b bearer) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}
func (bearer) RequireTransportSecurity() bool { return false }

type smoke struct {
	c          config
	key        jwk.Key
	conn       *grpc.ClientConn
	alice, bob user
	passed     int
}

func (s *smoke) user(sub, name string, groups []string) user {
	tok := jwt.New()
	now := time.Now()
	for k, v := range map[string]any{
		jwt.IssuerKey: s.c.issuer, jwt.SubjectKey: sub, jwt.AudienceKey: s.c.audience,
		jwt.IssuedAtKey: now, jwt.ExpirationKey: now.Add(time.Hour),
		"email": sub + "@smoke.workbench.test", "name": name, "groups": groups,
	} {
		_ = tok.Set(k, v)
	}
	b, err := jwt.Sign(tok, jwa.RS256, s.key)
	if err != nil {
		panic(err)
	}
	return user{name: sub, token: string(b), creds: grpc.PerRPCCredentials(bearer(b))}
}

func (s *smoke) pass(format string, a ...any) {
	s.passed++
	fmt.Printf("PASS %d. %s\n", s.passed, fmt.Sprintf(format, a...))
}

// eventually polls f until it returns true or the timeout passes.
func (s *smoke) eventually(ctx context.Context, what string, f func() (bool, error)) error {
	deadline := time.Now().Add(s.c.timeout)
	var last error
	for time.Now().Before(deadline) {
		ok, err := f()
		if ok {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if last != nil {
		return fmt.Errorf("timed out waiting for %s: last error: %w", what, last)
	}
	return fmt.Errorf("timed out waiting for %s", what)
}

func (s *smoke) all(ctx context.Context) error {
	identity := wb.NewIdentityServiceClient(s.conn)
	approvals := wb.NewApprovalServiceClient(s.conn)
	audit := wb.NewAuditServiceClient(s.conn)
	notes := wb.NewNotificationServiceClient(s.conn)

	// 1. Sign-in and roles.
	for _, u := range []struct {
		user
		want wb.Role
	}{{s.alice, wb.Role_ROLE_REQUESTER}, {s.bob, wb.Role_ROLE_APPROVER}} {
		me, err := identity.GetMe(ctx, &wb.GetMeRequest{}, u.creds)
		if err != nil {
			return fmt.Errorf("%s sign-in: %w", u.name, err)
		}
		if !slices.Contains(me.GetRoles(), u.want) {
			return fmt.Errorf("%s has roles %v, want %v", u.name, me.GetRoles(), u.want)
		}
	}
	s.pass("alice and bob sign in; alice is a requester, bob an approver")

	// 2. Alice starts the conversation; the agent pauses for approval.
	a2c, err := s.a2a(ctx)
	if err != nil {
		return err
	}
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("workbench smoke: promote the smoke app to production"))
	res, err := a2c.SendMessage(withToken(ctx, s.alice.token), &a2a.SendMessageRequest{Message: msg, Config: &a2a.SendMessageConfig{ReturnImmediately: true}})
	if err != nil {
		return fmt.Errorf("start conversation: %w", err)
	}
	task, ok := res.(*a2a.Task)
	if !ok {
		return fmt.Errorf("expected a task from SendMessage, got %T", res)
	}
	sessionID, taskID := task.ContextID, string(task.ID)
	if err := s.eventually(ctx, "the task to pause", func() (bool, error) {
		t, err := a2c.GetTask(withToken(ctx, s.alice.token), &a2a.GetTaskRequest{ID: a2a.TaskID(taskID)})
		if err != nil {
			return false, err
		}
		if hitl.Terminal(string(t.Status.State)) {
			return false, fmt.Errorf("task ended as %s before pausing", t.Status.State)
		}
		return t.Status.State == a2a.TaskStateInputRequired, nil
	}); err != nil {
		return err
	}
	s.pass("alice's conversation %s paused at task %s", sessionID, taskID)

	// 3. The pause is a production-deploy approval.
	var approval *wb.Approval
	if err := s.eventually(ctx, "the approval to appear", func() (bool, error) {
		r, err := approvals.ListSessionApprovals(ctx, &wb.ListSessionApprovalsRequest{SessionId: sessionID}, s.alice.creds)
		if err != nil {
			return false, err
		}
		for _, a := range r.GetApprovals() {
			if a.GetTaskId() == taskID && a.GetState() == wb.ApprovalState_APPROVAL_STATE_PENDING {
				approval = a
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		return err
	}
	if approval.GetKind() != wb.ActionKind_ACTION_KIND_PRODUCTION_DEPLOY || approval.GetRequesterMayDecide() || approval.GetCallerMayDecide() {
		return fmt.Errorf("approval %s: kind %s, requester may decide %v, alice may decide %v; want a production deploy only others may decide",
			approval.GetId(), approval.GetKind(), approval.GetRequesterMayDecide(), approval.GetCallerMayDecide())
	}
	s.pass("the pause is approval %s, a production deploy alice may not decide", approval.GetId())

	// 4. Alice is refused.
	_, err = approvals.Decide(ctx, &wb.DecideRequest{ApprovalId: approval.GetId(), Approved: true}, s.alice.creds)
	if status.Code(err) != codes.PermissionDenied {
		return fmt.Errorf("alice approving her own production deploy: got %v, want PermissionDenied", err)
	}
	s.pass("the server refuses alice's attempt: %s", status.Convert(err).Message())

	// 5. Bob finds it and approves.
	inbox, err := approvals.ListInbox(ctx, &wb.ListInboxRequest{View: wb.InboxView_INBOX_VIEW_TO_DECIDE}, s.bob.creds)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(inbox.GetApprovals(), func(a *wb.Approval) bool { return a.GetId() == approval.GetId() && a.GetCallerMayDecide() }) {
		return errors.New("the approval is not in bob's inbox as his to decide")
	}
	decided, err := approvals.Decide(ctx, &wb.DecideRequest{ApprovalId: approval.GetId(), Approved: true, Reason: "smoke test"}, s.bob.creds)
	if err != nil {
		return fmt.Errorf("bob approving: %w", err)
	}
	if decided.GetApproval().GetDecidedBy().GetId() == "" {
		return errors.New("decided approval names no decider")
	}
	s.pass("bob finds it in his inbox and approves it")

	// 6. The task resumes and completes.
	if err := s.eventually(ctx, "the task to complete", func() (bool, error) {
		t, err := a2c.GetTask(withToken(ctx, s.alice.token), &a2a.GetTaskRequest{ID: a2a.TaskID(taskID)})
		if err != nil {
			return false, err
		}
		switch t.Status.State {
		case a2a.TaskStateCompleted:
			return true, nil
		case a2a.TaskStateFailed, a2a.TaskStateCanceled, a2a.TaskStateRejected:
			return false, fmt.Errorf("task ended as %s", t.Status.State)
		}
		return false, nil
	}); err != nil {
		return err
	}
	s.pass("the decision reached the task, which resumed and completed")

	// 7. The audit trail.
	want := []string{"approval.requested", "approval.decided", "approval.delivered"}
	if err := s.eventually(ctx, "the audit trail", func() (bool, error) {
		r, err := audit.ListAuditEvents(ctx, &wb.ListAuditEventsRequest{SessionId: sessionID}, s.alice.creds)
		if err != nil {
			return false, err
		}
		seen := map[string]*wb.AuditEvent{}
		for _, e := range r.GetEvents() {
			seen[e.GetType()] = e
		}
		for _, w := range want {
			if seen[w] == nil {
				return false, fmt.Errorf("no %s event yet", w)
			}
		}
		d := seen["approval.decided"]
		if d.GetActor().GetId() == d.GetOnBehalfOf().GetId() || d.GetOnBehalfOf().GetId() == "" {
			return false, fmt.Errorf("decision event must name bob acting for alice, got actor %q for %q", d.GetActor().GetId(), d.GetOnBehalfOf().GetId())
		}
		return true, nil
	}); err != nil {
		return err
	}
	s.pass("the audit trail records the request, bob's decision for alice, and delivery")

	// 8. Push notification → alice is told it finished.
	if err := s.eventually(ctx, "alice's finished notification (push path)", func() (bool, error) {
		r, err := notes.ListNotifications(ctx, &wb.ListNotificationsRequest{}, s.alice.creds)
		if err != nil {
			return false, err
		}
		return slices.ContainsFunc(r.GetNotifications(), func(n *wb.Notification) bool {
			return n.GetSessionId() == sessionID && n.GetKind() == wb.NotificationKind_NOTIFICATION_KIND_TASK_FINISHED
		}), nil
	}); err != nil {
		return err
	}
	s.pass("kagent's push notification arrived and alice was told the task finished")

	fmt.Printf("\nAll %d checks passed.\n", s.passed)
	return nil
}

type tokenKey struct{}

func withToken(ctx context.Context, tok string) context.Context {
	return context.WithValue(ctx, tokenKey{}, tok)
}

type authInterceptor struct{ a2aclient.PassthroughInterceptor }

func (authInterceptor) Before(ctx context.Context, req *a2aclient.Request) (context.Context, any, error) {
	if tok, ok := ctx.Value(tokenKey{}).(string); ok {
		req.ServiceParams.Append("Authorization", "Bearer "+tok)
	}
	req.ServiceParams.Append(hitl.ExtensionHeader, hitl.ExtensionURI)
	return ctx, nil, nil
}

func (s *smoke) a2a(ctx context.Context) (*a2aclient.Client, error) {
	ns, name, ok := strings.Cut(s.c.agent, "/")
	if !ok {
		return nil, fmt.Errorf("-agent must be namespace/name")
	}
	ep := a2a.NewAgentInterface(strings.TrimRight(s.c.api, "/")+"/agents/"+ns+"/"+name, a2a.TransportProtocolJSONRPC)
	ep.Tenant = s.c.agent
	return a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{ep}, a2aclient.WithCallInterceptors(authInterceptor{}))
}
