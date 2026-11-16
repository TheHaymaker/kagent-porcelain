import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ApprovalSchema, ApprovalState, ToolCallSchema } from "@/gen/workbench/v1/approvals_pb";
import { decidedPauseTask } from "./follow";
import { initialConversation, type ConversationState } from "./state";

// The HITL id is the call's id; the model's function-call id is fn-<id>.
const approval = (id: string, state: ApprovalState) =>
  create(ApprovalSchema, { id: `a-${id}`, taskId: "t1", state, calls: [create(ToolCallSchema, { id, callId: `fn-${id}`, name: "cloudrun_deploy_preview" })] });

const paused = (awaiting: ConversationState["awaiting"], tools: ConversationState["tools"] = {}): ConversationState => ({
  ...initialConversation,
  turns: { t1: "input_required" },
  turnOrder: ["t1"],
  awaiting,
  tools,
});

const onTools = paused({ kind: "tool_approval", taskId: "t1", tools: [{ id: "c2", name: "cloudrun_deploy_preview", args: {} }] });

describe("decidedPauseTask", () => {
  it("follows when the paused calls' approval was decided elsewhere", () => {
    expect(decidedPauseTask(onTools, [approval("c2", ApprovalState.APPROVED)])).toBe("t1");
    expect(decidedPauseTask(onTools, [approval("c2", ApprovalState.REJECTED)])).toBe("t1");
  });

  it("does not follow while the paused calls' approval is open", () => {
    expect(decidedPauseTask(onTools, [approval("c1", ApprovalState.APPROVED), approval("c2", ApprovalState.PENDING)])).toBeUndefined();
  });

  it("does not follow a turn paused on a question, whatever was decided before", () => {
    const onQuestion = paused({ kind: "ask_user", taskId: "t1", requestId: "q1", questions: [] });
    expect(decidedPauseTask(onQuestion, [approval("c1", ApprovalState.APPROVED)])).toBeUndefined();
  });

  it("uses the waiting tool rows when the stream did not report the pause", () => {
    const fromRows = paused(undefined, { "fn-c3": { id: "fn-c3", name: "cloudrun_deploy_preview", args: {}, status: "waiting", taskId: "t1", at: "" } });
    expect(decidedPauseTask(fromRows, [approval("c3", ApprovalState.APPROVED)])).toBe("t1");
    expect(decidedPauseTask(fromRows, [approval("c1", ApprovalState.APPROVED)])).toBeUndefined();
  });

  it("does nothing unless the turn is paused", () => {
    expect(decidedPauseTask({ ...onTools, turns: { t1: "working" } }, [approval("c2", ApprovalState.APPROVED)])).toBeUndefined();
  });
});
