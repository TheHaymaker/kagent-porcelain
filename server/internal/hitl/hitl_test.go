package hitl

import (
	"encoding/json"
	"testing"
)

const pushBody = `{
  "statusUpdate": {
    "taskId": "task-1",
    "contextId": "ctx-1",
    "status": {
      "state": "TASK_STATE_INPUT_REQUIRED",
      "message": {
        "messageId": "m1",
        "role": "ROLE_AGENT",
        "parts": [{"text": "Approval is required for tool(s): workbench_request_promotion"}],
        "extensions": ["https://kagent.dev/extensions/hitl/v1"],
        "metadata": {
          "https://kagent.dev/extensions/hitl/v1": {
            "type": "tool_approval_request",
            "hint": "Approval is required for tool(s): workbench_request_promotion",
            "tools": [{"id": "tool-1", "call_id": "call-1", "name": "workbench_request_promotion", "args": {"appId": "app_1"}}]
          }
        }
      }
    }
  }
}`

func decode(t *testing.T) StatusUpdate {
	t.Helper()
	var u StatusUpdate
	if err := json.Unmarshal([]byte(pushBody), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestReadToolApproval(t *testing.T) {
	u := decode(t)
	req, ok := ReadRequest(u.StatusUpdate.TaskID, u.StatusUpdate.ContextID, u.StatusUpdate.Status.Message)
	if !ok {
		t.Fatal("expected a request")
	}
	if req.Kind != KindToolApproval || len(req.Tools) != 1 || req.Tools[0].Name != "workbench_request_promotion" {
		t.Fatalf("unexpected request %+v", req)
	}
	if req.Tools[0].Args["appId"] != "app_1" {
		t.Fatalf("args not read: %+v", req.Tools[0].Args)
	}
}

func TestReadRequiresExtensionListed(t *testing.T) {
	u := decode(t)
	u.StatusUpdate.Status.Message.Extensions = nil
	if _, ok := ReadRequest("task-1", "ctx-1", u.StatusUpdate.Status.Message); ok {
		t.Fatal("payload without the extension listed must be ignored")
	}
}

func TestReadNestedPrefersChildTools(t *testing.T) {
	msg := &Message{
		Extensions: []string{ExtensionURI},
		Metadata: map[string]any{ExtensionURI: map[string]any{
			"type":   "tool_approval_request",
			"tools":  []any{map[string]any{"id": "outer", "name": "call_subagent"}},
			"nested": map[string]any{"subagent_name": "deployer", "tools": []any{map[string]any{"id": "inner", "name": "iam_grant"}}},
		}},
	}
	req, ok := ReadRequest("t", "c", msg)
	if !ok || req.Tools[0].ID != "inner" || req.Subagent != "deployer" {
		t.Fatalf("nested request not read: %+v", req)
	}
}

func TestRejectsDuplicateToolIDs(t *testing.T) {
	msg := &Message{
		Extensions: []string{ExtensionURI},
		Metadata: map[string]any{ExtensionURI: map[string]any{
			"type":  "tool_approval_request",
			"tools": []any{map[string]any{"id": "a", "name": "x"}, map[string]any{"id": "a", "name": "y"}},
		}},
	}
	if _, ok := ReadRequest("t", "c", msg); ok {
		t.Fatal("duplicate ids must not parse")
	}
}

func TestAskUser(t *testing.T) {
	msg := &Message{
		Extensions: []string{ExtensionURI},
		Metadata:   map[string]any{ExtensionURI: map[string]any{"type": "ask_user_request", "id": "q1"}},
	}
	req, ok := ReadRequest("t", "c", msg)
	if !ok || req.Kind != KindAskUser || req.AskUserID != "q1" {
		t.Fatalf("ask_user not read: %+v", req)
	}
}

func TestFingerprintStable(t *testing.T) {
	a := Request{Kind: KindToolApproval, TaskID: "t", ContextID: "c", Tools: []Tool{{ID: "1", Name: "a"}, {ID: "2", Name: "b"}}}
	b := Request{Kind: KindToolApproval, TaskID: "t", ContextID: "c", Tools: []Tool{{ID: "2", Name: "b"}, {ID: "1", Name: "a"}}}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("order of tools must not change the fingerprint")
	}
	b.Tools[0].ID = "3"
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("different pause must differ")
	}
}

func TestToolApprovalResponse(t *testing.T) {
	req := Request{Kind: KindToolApproval, TaskID: "t", ContextID: "c", Tools: []Tool{{ID: "1", Name: "a"}, {ID: "2", Name: "b"}}}
	msg, err := ToolApprovalResponse(req, []Decision{{ID: "2", Approved: false, Reason: "not now"}, {ID: "1", Approved: true, Reason: "ignored"}}, "m")
	if err != nil {
		t.Fatal(err)
	}
	payload := msg.Metadata[ExtensionURI].(map[string]any)
	approvals := payload["approvals"].([]map[string]any)
	if payload["type"] != "tool_approval_response" || len(approvals) != 2 {
		t.Fatalf("bad payload %+v", payload)
	}
	if approvals[0]["id"] != "1" || approvals[0]["rejection_reason"] != nil {
		t.Fatalf("approved call must not carry a reason: %+v", approvals[0])
	}
	if approvals[1]["rejection_reason"] != "not now" {
		t.Fatalf("reason lost: %+v", approvals[1])
	}
	if msg.TaskID != "t" || msg.ContextID != "c" || msg.Extensions[0] != ExtensionURI {
		t.Fatal("response must continue the same task and list the extension")
	}
	if _, err := ToolApprovalResponse(req, []Decision{{ID: "1", Approved: true}}, "m"); err == nil {
		t.Fatal("a missing decision must be an error")
	}
	if _, err := ToolApprovalResponse(req, []Decision{{ID: "1", Approved: true}, {ID: "2", Approved: true}, {ID: "9", Approved: true}}, "m"); err == nil {
		t.Fatal("an extra decision must be an error")
	}
}

func TestHarnessBoilerplateHintIsDropped(t *testing.T) {
	adk := "Please approve or reject the tool call cloudrun_deploy_preview() by responding with a FunctionResponse with an expected ToolConfirmation payload."
	u := decode(t)
	u.StatusUpdate.Status.Message.Metadata[ExtensionURI].(map[string]any)["hint"] = adk
	req, ok := ReadRequest(u.StatusUpdate.TaskID, u.StatusUpdate.ContextID, u.StatusUpdate.Status.Message)
	if !ok || req.Hint != "" {
		t.Fatalf("boilerplate kept: %q", req.Hint)
	}
	if !harnessBoilerplate(adk) || harnessBoilerplate("Approval is required for tool(s): x") {
		t.Fatal("boilerplate detection is wrong")
	}
}
