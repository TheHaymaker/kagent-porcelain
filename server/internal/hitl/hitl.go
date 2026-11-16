// Package hitl reads and writes kagent's human-in-the-loop A2A extension.
//
// The wire format is kagent's (docs/architecture/human-in-the-loop.md): a
// paused task's status message carries the request in its metadata under the
// extension URI, and the answer is an ordinary user message on the same task
// and context carrying the response under the same key.
package hitl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// ExtensionURI identifies kagent's HITL extension.
const ExtensionURI = "https://kagent.dev/extensions/hitl/v1"

// ExtensionHeader activates an extension on an A2A request.
const ExtensionHeader = "A2A-Extensions"

// Kind distinguishes the two request forms.
type Kind string

const (
	KindToolApproval Kind = "tool_approval"
	KindAskUser      Kind = "ask_user"
)

// Tool is one pending tool call in a tool approval request.
type Tool struct {
	ID     string         `json:"id"`
	CallID string         `json:"call_id,omitempty"`
	Name   string         `json:"name"`
	Args   map[string]any `json:"args,omitempty"`
}

// Request is a pending HITL request read from a task's status message.
type Request struct {
	Kind      Kind
	TaskID    string
	ContextID string
	Hint      string
	Tools     []Tool
	// AskUserID is set for ask_user requests.
	AskUserID string
	// Subagent names the child agent that asked, for nested requests.
	Subagent string
}

// Fingerprint identifies this exact pause. Two observations of the same pause
// (a push notification and a read of the task, say) have the same fingerprint;
// a later pause on the same task has a different one.
func (r Request) Fingerprint() string {
	ids := make([]string, 0, len(r.Tools)+1)
	for _, t := range r.Tools {
		ids = append(ids, t.ID+"\x00"+t.CallID+"\x00"+t.Name)
	}
	sort.Strings(ids)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", r.TaskID, r.ContextID, r.Kind, r.AskUserID)
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Message is the subset of an A2A message HITL needs, in A2A's JSON form.
type Message struct {
	MessageID  string         `json:"messageId,omitempty"`
	ContextID  string         `json:"contextId,omitempty"`
	TaskID     string         `json:"taskId,omitempty"`
	Role       string         `json:"role,omitempty"`
	Parts      []Part         `json:"parts,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Extensions []string       `json:"extensions,omitempty"`
}

// Part is an A2A message part. Only text and data parts are used here.
type Part struct {
	Text     string         `json:"text,omitempty"`
	Data     any            `json:"data,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TaskStatus is an A2A task status in JSON form.
type TaskStatus struct {
	State     string   `json:"state"`
	Message   *Message `json:"message,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
}

// StatusUpdate is the body kagent posts to a push notification callback.
type StatusUpdate struct {
	StatusUpdate *struct {
		TaskID    string     `json:"taskId"`
		ContextID string     `json:"contextId"`
		Status    TaskStatus `json:"status"`
	} `json:"statusUpdate"`
}

// A2A task states as kagent serialises them.
const (
	StateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	StateAuthRequired  = "TASK_STATE_AUTH_REQUIRED"
	StateCompleted     = "TASK_STATE_COMPLETED"
	StateFailed        = "TASK_STATE_FAILED"
	StateCanceled      = "TASK_STATE_CANCELED"
	StateRejected      = "TASK_STATE_REJECTED"
	StateWorking       = "TASK_STATE_WORKING"
)

// Terminal reports whether a task in this state will never pause again.
func Terminal(state string) bool {
	switch state {
	case StateCompleted, StateFailed, StateCanceled, StateRejected:
		return true
	}
	return false
}

// ReadRequest extracts a HITL request from a paused task's status message.
// It returns false when the message carries no HITL request it understands.
func ReadRequest(taskID, contextID string, msg *Message) (Request, bool) {
	if msg == nil || taskID == "" {
		return Request{}, false
	}
	payload, ok := msg.Metadata[ExtensionURI].(map[string]any)
	if !ok {
		return Request{}, false
	}
	// kagent always lists the extension on a message that uses it; a payload
	// without that is not one we were asked to read.
	if !slices.Contains(msg.Extensions, ExtensionURI) {
		return Request{}, false
	}
	req := Request{TaskID: taskID, ContextID: contextID}
	if h, ok := payload["hint"].(string); ok && !harnessBoilerplate(h) {
		req.Hint = h
	}
	nested, _ := payload["nested"].(map[string]any)
	if nested != nil {
		req.Subagent, _ = nested["subagent_name"].(string)
	}
	switch payload["type"] {
	case "ask_user_request":
		id, _ := payload["id"].(string)
		if id == "" {
			return Request{}, false
		}
		req.Kind = KindAskUser
		req.AskUserID = id
		return req, true
	case "tool_approval_request":
		raw := payload["tools"]
		if nested != nil && nested["tools"] != nil {
			raw = nested["tools"]
		}
		tools, err := readTools(raw)
		if err != nil || len(tools) == 0 {
			return Request{}, false
		}
		req.Kind = KindToolApproval
		req.Tools = tools
		return req, true
	}
	return Request{}, false
}

func readTools(raw any) ([]Tool, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var tools []Tool
	if err := json.Unmarshal(b, &tools); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, t := range tools {
		if t.ID == "" || t.Name == "" || seen[t.ID] {
			return nil, fmt.Errorf("tool approval request has a tool without a unique id and name")
		}
		seen[t.ID] = true
	}
	return tools, nil
}

// Decision answers one pending tool call.
type Decision struct {
	ID       string `json:"id"`
	Approved bool   `json:"approved"`
	Reason   string `json:"rejection_reason,omitempty"`
}

// ToolApprovalResponse builds the user message that answers a tool approval
// request on its own task. Every pending call must have exactly one decision.
func ToolApprovalResponse(req Request, decisions []Decision, messageID string) (Message, error) {
	if req.Kind != KindToolApproval {
		return Message{}, fmt.Errorf("request is %s, not a tool approval", req.Kind)
	}
	byID := map[string]Decision{}
	for _, d := range decisions {
		if _, dup := byID[d.ID]; dup {
			return Message{}, fmt.Errorf("duplicate decision for %s", d.ID)
		}
		byID[d.ID] = d
	}
	approvals := make([]map[string]any, 0, len(req.Tools))
	for _, t := range req.Tools {
		d, ok := byID[t.ID]
		if !ok {
			return Message{}, fmt.Errorf("no decision for pending call %s (%s)", t.ID, t.Name)
		}
		delete(byID, t.ID)
		entry := map[string]any{"id": d.ID, "approved": d.Approved}
		if !d.Approved && d.Reason != "" {
			entry["rejection_reason"] = d.Reason
		}
		approvals = append(approvals, entry)
	}
	if len(byID) > 0 {
		return Message{}, fmt.Errorf("decision for a call that is not pending")
	}
	text := "Approved."
	for _, d := range decisions {
		if !d.Approved {
			text = "Some calls were refused."
			break
		}
	}
	return Message{
		MessageID:  messageID,
		ContextID:  req.ContextID,
		TaskID:     req.TaskID,
		Role:       "ROLE_USER",
		Parts:      []Part{{Text: text}},
		Metadata:   map[string]any{ExtensionURI: map[string]any{"type": "tool_approval_response", "approvals": approvals}},
		Extensions: []string{ExtensionURI},
	}, nil
}

// harnessBoilerplate reports a hint that is the harness's own instruction to
// its client (ADK's "Please approve or reject the tool call ... with a
// FunctionResponse ..."), which means nothing to a person. Workbench then
// describes the call itself.
func harnessBoilerplate(h string) bool {
	h = strings.TrimSpace(h)
	return strings.HasPrefix(h, "Please approve or reject the tool call") || strings.Contains(h, "FunctionResponse")
}
