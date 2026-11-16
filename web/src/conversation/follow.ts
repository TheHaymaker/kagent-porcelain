import { ApprovalState, type Approval } from "@/gen/workbench/v1/approvals_pb";
import { activeTurn, type ConversationState } from "./state";

/**
 * The task to follow because its tool approval was decided without this
 * page (a remembered answer, an approver elsewhere, the Approvals page), or
 * undefined.
 *
 * Only a turn paused on tool calls qualifies, and only when a decided
 * approval covers the very calls it is paused on. A turn paused on a
 * question, or on calls whose approval is still open, is left alone: the
 * page polls every few seconds and would otherwise re-follow it each time.
 */
export function decidedPauseTask(state: ConversationState, approvals: readonly Approval[]): string | undefined {
  const turn = activeTurn(state);
  if (turn?.state !== "input_required") return undefined;
  const awaiting = state.awaiting;
  if (awaiting?.kind === "ask_user") return undefined;
  // The paused calls: from the pause itself, or (when the stream did not
  // report it) from the turn's tool rows still marked waiting.
  const paused = new Set(
    awaiting?.kind === "tool_approval" && awaiting.taskId === turn.taskId
      ? awaiting.tools.map((t) => t.id)
      : Object.values(state.tools)
          .filter((t) => t.status === "waiting" && (!t.taskId || t.taskId === turn.taskId))
          .map((t) => t.id),
  );
  if (paused.size === 0) return undefined;
  const decided = approvals.some(
    (a) =>
      a.taskId === turn.taskId &&
      (a.state === ApprovalState.APPROVED || a.state === ApprovalState.REJECTED) &&
      // The pause names a call by its HITL id, a tool row by the model's
      // function-call id; an approval call carries both.
      a.calls.some((c) => paused.has(c.id) || paused.has(c.callId)),
  );
  return decided ? turn.taskId : undefined;
}
