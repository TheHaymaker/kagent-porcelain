/**
 * NOTICE: Adapted from kagent-dev/kagent ui/src/api/chat/types.ts, Apache-2.0.
 *
 * The chat port in the app's own vocabulary. Screens render these; only the
 * A2A client knows the wire.
 */
import type { AskUserRecord, PendingRequest, ToolApprovalRecord } from "./hitl";

export type ChatRole = "user" | "agent";

export interface ChatTextPart {
  kind: "text";
  text: string;
  /** Model reasoning rather than its answer. */
  thought?: boolean;
}

export interface ChatDataPart {
  kind: "data";
  dataKind: "tool_call" | "tool_result" | "tool_not_run" | "unknown";
  data: Record<string, unknown>;
}

/** A2UI messages that arrived as an `application/a2ui+json` DataPart. */
export interface ChatA2uiPart {
  kind: "a2ui";
  messages: unknown[];
}

export interface ChatToolApprovalPart {
  kind: "tool_approval";
  approval: ToolApprovalRecord;
}

export interface ChatAskUserPart {
  kind: "ask_user";
  interaction: AskUserRecord;
}

export interface ChatFilePart {
  kind: "file";
  name: string;
  mediaType: string;
  url?: string;
}

export type ChatPart =
  | ChatTextPart
  | ChatDataPart
  | ChatA2uiPart
  | ChatToolApprovalPart
  | ChatAskUserPart
  | ChatFilePart;

export interface ChatMessage {
  id: string;
  role: ChatRole;
  parts: ChatPart[];
  createdAt: string;
  taskId?: string;
}

export type ChatTurnState =
  | "submitted"
  | "working"
  | "input_required"
  | "completed"
  | "failed"
  | "canceled";

export type ChatEvent =
  | { type: "message"; message: ChatMessage }
  | { type: "delta"; messageId: string; text: string; taskId?: string }
  | { type: "status"; state: ChatTurnState; taskId?: string; awaiting?: PendingRequest; text?: string };

export interface ChatConversationRef {
  /** Session UUID, also the A2A contextId. */
  id: string;
  /** `namespace/name`, sent as the A2A `tenant`. */
  agent: string;
}

export interface SendMessageInput {
  conversation: ChatConversationRef;
  text: string;
  messageId?: string;
  /** The parked turn this answers. */
  taskId?: string;
  /** Structured HITL answer metadata (ask_user_response). */
  hitl?: Record<string, unknown>;
  signal?: AbortSignal;
}

export interface ChatHistory {
  messages: ChatMessage[];
  /** Task states, oldest first, for "try again from here" and resuming. */
  turns: { taskId: string; state: ChatTurnState }[];
  awaitingReply?: PendingRequest;
}

export function isTerminal(state: ChatTurnState): boolean {
  return state === "completed" || state === "failed" || state === "canceled";
}
