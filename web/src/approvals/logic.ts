/**
 * What the client may draw for an approval. Every answer is read off the
 * Approval the ApprovalService returned; nothing here decides policy.
 */
import { ActionKind, ApprovalState, RememberScope, type Approval } from "@/gen/workbench/v1/approvals_pb";
import { personName } from "@/lib/format";

/** Buttons appear only for a pending approval the server says the caller may decide. */
export function canDecide(approval: Approval): boolean {
  return approval.state === ApprovalState.PENDING && approval.callerMayDecide;
}

/** Always-ask kinds accept only ONCE; ordinary calls may be remembered. */
export function rememberOptions(approval: Approval): RememberScope[] {
  return approval.kind === ActionKind.ORDINARY
    ? [RememberScope.ONCE, RememberScope.CONVERSATION, RememberScope.ALWAYS]
    : [RememberScope.ONCE];
}

export const REMEMBER_LABEL: Record<RememberScope, string> = {
  [RememberScope.UNSPECIFIED]: "Just this once",
  [RememberScope.ONCE]: "Just this once",
  [RememberScope.CONVERSATION]: "For the rest of this conversation",
  [RememberScope.ALWAYS]: "Always, for this kind of step",
};

/** "Platform approvers", "Dana Lee or Sam Ortiz", or a generic sentence. */
export function waitingOn(approval: Approval): string {
  const people = approval.approvers.map((p) => personName(p));
  const groups = approval.approverGroups.map(humanGroup);
  const names = [...people, ...groups];
  if (names.length === 0) return "an approver";
  if (names.length === 1) return names[0]!;
  return `${names.slice(0, -1).join(", ")} or ${names.at(-1)}`;
}

export function humanGroup(group: string): string {
  const words = group.replace(/[-_]+/g, " ").trim();
  return words ? words[0]!.toUpperCase() + words.slice(1) : group;
}

/**
 * Why the caller sees no buttons, in plain words. `myId` is the signed-in
 * principal; the server's flags decide, this only explains them.
 */
export function whyNotDecider(approval: Approval, myId: string | undefined): string | undefined {
  if (approval.state !== ApprovalState.PENDING || approval.callerMayDecide) return undefined;
  const isRequester = !!myId && approval.requester?.id === myId;
  if (isRequester && !approval.requesterMayDecide) {
    return approval.kind === ActionKind.PRODUCTION_DEPLOY
      ? "Production deploys need a second pair of eyes, so someone other than you approves this."
      : "Steps like this need someone other than the requester to approve.";
  }
  return undefined;
}

export function approvalTitle(approval: Approval): string {
  if (approval.hint) return approval.hint;
  const names = approval.calls.map((c) => c.name).filter(Boolean);
  return names.length ? `Allow ${names.join(", ")}?` : "Approval needed";
}

export function isSettled(approval: Approval): boolean {
  return approval.state !== ApprovalState.PENDING && approval.state !== ApprovalState.UNSPECIFIED;
}
