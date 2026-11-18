// Package kagent is how Workbench's own services talk to the kagent core
// running in the same process: over loopback, through kagent's public API,
// with short-lived delegation tokens. Going through the API rather than
// kagent's internals means every call passes kagent's own authentication,
// authorization and ownership checks, exactly as a client's would.
package kagent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	kagentv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/thehaymaker/kagent-porcelain/server/internal/hitl"
)

// Minter issues delegation tokens. identity.Authenticator implements it.
type Minter interface {
	Mint(principalID, actorID, purpose string) string
}

// SessionInfo is what Workbench needs to know about a kagent session.
type SessionInfo struct {
	ID      string
	Agent   string // namespace/name
	Creator string
	Name    string
}

// Task is the part of an A2A task Workbench reads.
type Task struct {
	ID        string
	ContextID string
	State     string
	Message   *hitl.Message
}

// Client is the subset of kagent Workbench uses. Domain packages depend on
// this interface, not on the transport.
type Client interface {
	// Session reads a session as its creator would.
	Session(ctx context.Context, asPrincipal, sessionID string) (SessionInfo, error)
	// Task reads an A2A task as asPrincipal.
	Task(ctx context.Context, asPrincipal, agent, taskID string) (Task, error)
	// Send sends a user message into an existing task as asPrincipal, on
	// behalf of actorID (who is recorded, not authenticated, by kagent).
	Send(ctx context.Context, asPrincipal, actorID, agent string, msg hitl.Message) error
	// PausedTasks lists a session's tasks that are waiting for input.
	PausedTasks(ctx context.Context, asPrincipal, agent, sessionID string) ([]Task, error)
	// Watch registers a push notification callback for a task.
	Watch(ctx context.Context, asPrincipal, agent, taskID, callbackURL string) error
}

// Loopback implements Client against the kagent API served by this process.
type Loopback struct {
	base   string // http://127.0.0.1:8083
	minter Minter
	conn   *grpc.ClientConn

	mu      sync.Mutex
	clients map[string]*a2aclient.Client
}

// Dial prepares a loopback client. addr is host:port of kagent's API server.
func Dial(addr string, minter Minter) (*Loopback, error) {
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Loopback{base: "http://" + addr, minter: minter, conn: conn, clients: map[string]*a2aclient.Client{}}, nil
}

// Close releases connections.
func (l *Loopback) Close() error { return l.conn.Close() }

type bearer struct{ token string }

func (b bearer) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + b.token}, nil
}
func (bearer) RequireTransportSecurity() bool { return false }

// Session implements Client. kagent scopes GetSession to the caller's own
// sessions, so when asPrincipal is not the creator the lookup falls back to
// listing every creator's sessions, which kagent allows only for principals
// authorised for SessionAllCreators (Workbench's system principal is).
func (l *Loopback) Session(ctx context.Context, as, sessionID string) (SessionInfo, error) {
	creds := grpc.PerRPCCredentials(bearer{l.minter.Mint(as, "", "read-session")})
	svc := kagentv1.NewSessionServiceClient(l.conn)
	resp, err := svc.GetSession(ctx, &kagentv1.GetSessionRequest{SessionId: sessionID}, creds)
	if err == nil {
		return sessionInfo(resp.GetSession()), nil
	}
	if c := status.Code(err); c != codes.NotFound && c != codes.PermissionDenied {
		return SessionInfo{}, err
	}
	token := ""
	for range maxSessionPages {
		list, lerr := svc.ListSessions(ctx, &kagentv1.ListSessionsRequest{AllCreators: true,
			Page: &kagentv1.PageRequest{Limit: sessionPageSize, PageToken: token}}, creds)
		if lerr != nil {
			if status.Code(lerr) == codes.PermissionDenied {
				return SessionInfo{}, err
			}
			return SessionInfo{}, fmt.Errorf("list sessions: %w", lerr)
		}
		for _, s := range list.GetSessions() {
			if s.GetId() == sessionID {
				return sessionInfo(s), nil
			}
		}
		token = list.GetPage().GetNextPageToken()
		if token == "" {
			break
		}
	}
	return SessionInfo{}, err
}

const (
	sessionPageSize = 100
	maxSessionPages = 50
)

func sessionInfo(s *kagentv1.Session) SessionInfo {
	ref := s.GetAgent().GetRef()
	return SessionInfo{ID: s.GetId(), Agent: ref.GetNamespace() + "/" + ref.GetName(), Creator: s.GetCreator(), Name: s.GetName()}
}

// interceptor attaches the delegation token and activates HITL.
type interceptor struct {
	a2aclient.PassthroughInterceptor
}

type tokenKey struct{}

func (interceptor) Before(ctx context.Context, req *a2aclient.Request) (context.Context, any, error) {
	if tok, ok := ctx.Value(tokenKey{}).(string); ok {
		req.ServiceParams.Append("Authorization", "Bearer "+tok)
	}
	req.ServiceParams.Append(hitl.ExtensionHeader, hitl.ExtensionURI)
	return ctx, nil, nil
}

func (l *Loopback) a2a(ctx context.Context, agent string) (*a2aclient.Client, error) {
	ns, name, ok := strings.Cut(agent, "/")
	if !ok || ns == "" || name == "" {
		return nil, fmt.Errorf("agent %q is not namespace/name", agent)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if c, ok := l.clients[agent]; ok {
		return c, nil
	}
	endpoint := a2a.NewAgentInterface(l.base+"/agents/"+ns+"/"+name, a2a.TransportProtocolJSONRPC)
	endpoint.Tenant = agent
	c, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{endpoint}, a2aclient.WithCallInterceptors(interceptor{}))
	if err != nil {
		return nil, err
	}
	l.clients[agent] = c
	return c, nil
}

// Task implements Client.
func (l *Loopback) Task(ctx context.Context, as, agent, taskID string) (Task, error) {
	c, err := l.a2a(ctx, agent)
	if err != nil {
		return Task{}, err
	}
	ctx = context.WithValue(ctx, tokenKey{}, l.minter.Mint(as, "", "read-task"))
	zero := 0
	t, err := c.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(taskID), HistoryLength: &zero})
	if err != nil {
		return Task{}, err
	}
	return FromA2ATask(t), nil
}

// PausedTasks implements Client.
func (l *Loopback) PausedTasks(ctx context.Context, as, agent, sessionID string) ([]Task, error) {
	c, err := l.a2a(ctx, agent)
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, tokenKey{}, l.minter.Mint(as, "", "read-task"))
	zero := 0
	resp, err := c.ListTasks(ctx, &a2a.ListTasksRequest{ContextID: sessionID, Status: a2a.TaskStateInputRequired, PageSize: 20, HistoryLength: &zero})
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		out = append(out, FromA2ATask(t))
	}
	return out, nil
}

// Send implements Client.
func (l *Loopback) Send(ctx context.Context, as, actor, agent string, msg hitl.Message) error {
	c, err := l.a2a(ctx, agent)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, tokenKey{}, l.minter.Mint(as, actor, "deliver-decision"))
	_, err = c.SendMessage(ctx, &a2a.SendMessageRequest{Message: ToA2AMessage(msg), Config: &a2a.SendMessageConfig{ReturnImmediately: true}})
	return err
}

// Watch implements Client.
func (l *Loopback) Watch(ctx context.Context, as, agent, taskID, callbackURL string) error {
	c, err := l.a2a(ctx, agent)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, tokenKey{}, l.minter.Mint(as, "", "watch-task"))
	_, err = c.CreateTaskPushConfig(ctx, &a2a.PushConfig{TaskID: a2a.TaskID(taskID), ID: "workbench", URL: callbackURL})
	return err
}

// ToA2AMessage converts Workbench's message form to a2a-go's.
func ToA2AMessage(m hitl.Message) *a2a.Message {
	parts := make(a2a.ContentParts, 0, len(m.Parts))
	for _, p := range m.Parts {
		var part *a2a.Part
		if p.Data != nil {
			part = a2a.NewDataPart(p.Data)
		} else {
			part = a2a.NewTextPart(p.Text)
		}
		part.Metadata = p.Metadata
		parts = append(parts, part)
	}
	return &a2a.Message{ID: m.MessageID, ContextID: m.ContextID, TaskID: a2a.TaskID(m.TaskID), Role: a2a.MessageRoleUser,
		Parts: parts, Metadata: m.Metadata, Extensions: m.Extensions}
}

// FromA2ATask converts a2a-go's task to Workbench's form.
func FromA2ATask(t *a2a.Task) Task {
	out := Task{ID: string(t.ID), ContextID: t.ContextID, State: string(t.Status.State)}
	if m := t.Status.Message; m != nil {
		hm := &hitl.Message{MessageID: m.ID, ContextID: m.ContextID, TaskID: string(m.TaskID), Role: string(m.Role),
			Metadata: m.Metadata, Extensions: m.Extensions}
		for _, p := range m.Parts {
			hm.Parts = append(hm.Parts, hitl.Part{Text: p.Text(), Data: p.Data(), Metadata: p.Metadata})
		}
		out.Message = hm
	}
	return out
}
