/**
 * What the client may draw for an approval. Every answer is read off the
 * Approval the ApprovalService returned; nothing here decides policy.
 */
import {
  ActionKind,
  ApprovalState,
  RememberScope,
  type Approval,
} from "@/gen/workbench/v1/approvals_pb";
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
export function whyNotDecider(
  approval: Approval,
  myId: string | undefined,
): string | undefined {
  if (approval.state !== ApprovalState.PENDING || approval.callerMayDecide)
    return undefined;
  const isRequester = !!myId && approval.requester?.id === myId;
  if (isRequester && !approval.requesterMayDecide) {
    return approval.kind === ActionKind.PRODUCTION_DEPLOY
      ? "Production deploys need a second pair of eyes, so someone other than you approves this."
      : "Steps like this need someone other than the requester to approve.";
  }
  return undefined;
}

/** What a tool call does, in words a requester recognises. */
const ACTIONS: Record<string, string> = {
  cloudrun_deploy_preview: "Deploy the test version",
  cloudrun_delete_preview_service: "Delete the test version",
  cloudrun_set_iam_invoker: "Change who can open the test version",
  cloudrun_get_service: "Check the test version",
  cloudrun_list_revisions: "List the test version's releases",
  cloudrun_read_logs: "Read the test version's logs",
  workbench_request_promotion: "Put the app live",
  bigquery_delete_table: "Delete a data table",
  bigquery_create_dataset: "Create a dataset",
};

function describeCall(name: string, args: unknown): string {
  const action =
    ACTIONS[name] ??
    name
      .replace(/^[a-z]+_/, "")
      .replace(/_/g, " ")
      .replace(/^./, (c) => c.toUpperCase());
  const a = (args && typeof args === "object" ? args : {}) as Record<
    string,
    unknown
  >;
  const target = [a.service, a.appId, a.app, a.table].find(
    (v) => typeof v === "string" && v,
  );
  return target ? `${action}: ${String(target)}` : action;
}

export function approvalTitle(approval: Approval): string {
  if (approval.hint) return approval.hint;
  const calls = approval.calls.filter((c) => c.name);
  if (!calls.length) return "Approval needed";
  const parts = calls.map((c) => describeCall(c.name, c.args));
  return `${parts.join("; ")}?`;
}

export function isSettled(approval: Approval): boolean {
  return (
    approval.state !== ApprovalState.PENDING &&
    approval.state !== ApprovalState.UNSPECIFIED
  );
}
