/**
 * A conversation as the screen draws it: an ordered timeline of messages, tool
 * activity, A2UI surfaces, approvals and questions, plus each turn's state.
 *
 * Pure, so the rules (dedupe by id, where an approval sits, what counts as a
 * HITL pause) are unit-tested without a browser.
 */
import type { PendingRequest } from "@/api/chat/hitl";
import type { ChatEvent, ChatHistory, ChatMessage, ChatPart, ChatTurnState } from "@/api/chat/types";
import { a2uiFromPart, surfaceIdsIn } from "@/a2ui/extract";

export type Entry =
  | { kind: "message"; id: string; taskId?: string }
  | { kind: "tool"; id: string; taskId?: string }
  | { kind: "surface"; surfaceId: string; taskId?: string }
  | { kind: "approval"; approvalId: string; taskId?: string }
  | { kind: "question"; requestId: string; taskId: string };

export type ToolStatus = "running" | "waiting" | "done" | "failed" | "not_run";

export interface ToolActivity {
  id: string;
  name: string;
  args: unknown;
  result?: unknown;
  status: ToolStatus;
  taskId?: string;
  at: string;
}

export interface QuestionState {
  request: Extract<PendingRequest, { kind: "ask_user" }>;
  answers?: string[][];
}

export interface ConversationState {
  entries: Entry[];
  messages: Record<string, ChatMessage>;
  tools: Record<string, ToolActivity>;
  questions: Record<string, QuestionState>;
  turns: Record<string, ChatTurnState>;
  turnOrder: string[];
  /** What the conversation is parked on, if anything. */
  awaiting?: PendingRequest;
  /** The latest prose a HITL pause carried, for an `unknown` request. */
  awaitingText?: string;
  /** Messages already drawn another way (an answered question card). */
  hidden: Record<string, true>;
}

export const initialConversation: ConversationState = {
  entries: [],
  messages: {},
  tools: {},
  questions: {},
  turns: {},
  turnOrder: [],
  hidden: {},
};

export type ConversationAction =
  | { type: "history"; history: ChatHistory }
  | { type: "event"; event: ChatEvent }
  | { type: "local-user"; message: ChatMessage }
  | { type: "approvals"; approvals: { id: string; taskId: string }[] }
  | { type: "surfaces"; surfaces: { surfaceId: string; taskId?: string }[] }
  | { type: "answered"; requestId: string; answers: string[][]; messageId?: string }
  | { type: "turn"; taskId: string; state: ChatTurnState };

export function conversationReducer(state: ConversationState, action: ConversationAction): ConversationState {
  switch (action.type) {
    case "history": {
      let next = state;
      for (const message of action.history.messages) next = addMessage(next, message);
      for (const turn of action.history.turns) next = setTurn(next, turn.taskId, turn.state);
      if (action.history.awaitingReply) next = setAwaiting(next, action.history.awaitingReply);
      return next;
    }
    case "local-user":
      return addMessage(state, action.message);
    case "event":
      return applyEvent(state, action.event);
    case "approvals": {
      let next = state;
      for (const approval of action.approvals) {
        if (next.entries.some((e) => e.kind === "approval" && e.approvalId === approval.id)) continue;
        next = insertAfterTask(next, { kind: "approval", approvalId: approval.id, taskId: approval.taskId || undefined });
      }
      return next;
    }
    case "surfaces": {
      let next = state;
      for (const s of action.surfaces) {
        if (next.entries.some((e) => e.kind === "surface" && e.surfaceId === s.surfaceId)) continue;
        next = insertAfterTask(next, { kind: "surface", surfaceId: s.surfaceId, taskId: s.taskId });
      }
      return next;
    }
    case "answered": {
      const q = state.questions[action.requestId];
      if (!q) return state;
      const awaiting =
        state.awaiting?.kind === "ask_user" && state.awaiting.requestId === action.requestId ? undefined : state.awaiting;
      return {
        ...state,
        awaiting,
        hidden: action.messageId ? { ...state.hidden, [action.messageId]: true } : state.hidden,
        questions: { ...state.questions, [action.requestId]: { ...q, answers: action.answers } },
      };
    }
    case "turn":
      return setTurn(state, action.taskId, action.state);
  }
}

function applyEvent(state: ConversationState, event: ChatEvent): ConversationState {
  if (event.type === "message") return addMessage(state, event.message);
  if (event.type === "delta") {
    const existing = state.messages[event.messageId];
    if (!existing) {
      return addMessage(state, {
        id: event.messageId,
        role: "agent",
        parts: [{ kind: "text", text: event.text }],
        createdAt: new Date().toISOString(),
        taskId: event.taskId,
      });
    }
    const parts = [...existing.parts];
    const last = parts.at(-1);
    if (last?.kind === "text" && !last.thought) parts[parts.length - 1] = { ...last, text: last.text + event.text };
    else parts.push({ kind: "text", text: event.text });
    return { ...state, messages: { ...state.messages, [existing.id]: { ...existing, parts } } };
  }
  // status
  let next = state;
  if (event.taskId) next = setTurn(next, event.taskId, event.state);
  if (event.state === "input_required" && event.awaiting) {
    next = setAwaiting(next, event.awaiting);
    if (event.text) next = { ...next, awaitingText: event.text };
  } else if (event.taskId && next.awaiting?.taskId === event.taskId && event.state !== "input_required") {
    next = { ...next, awaiting: undefined, awaitingText: undefined };
  }
  if (event.state !== "input_required" && event.taskId) next = settleRunningTools(next, event.taskId, event.state);
  return next;
}

function setTurn(state: ConversationState, taskId: string, turn: ChatTurnState): ConversationState {
  if (!taskId) return state;
  const turnOrder = state.turnOrder.includes(taskId) ? state.turnOrder : [...state.turnOrder, taskId];
  let next: ConversationState = { ...state, turns: { ...state.turns, [taskId]: turn }, turnOrder };
  if (state.awaiting?.taskId === taskId && turn !== "input_required") next = { ...next, awaiting: undefined, awaitingText: undefined };
  return next;
}

/** A finished turn leaves nothing "running" behind. */
function settleRunningTools(state: ConversationState, taskId: string, turn: ChatTurnState): ConversationState {
  if (turn === "working" || turn === "submitted") return state;
  let tools = state.tools;
  for (const tool of Object.values(state.tools)) {
    if (tool.taskId === taskId && (tool.status === "running" || tool.status === "waiting")) {
      tools = { ...tools, [tool.id]: { ...tool, status: turn === "completed" ? "done" : "not_run" } };
    }
  }
  return tools === state.tools ? state : { ...state, tools };
}

function setAwaiting(state: ConversationState, awaiting: PendingRequest): ConversationState {
  let next: ConversationState = { ...state, awaiting };
  if (awaiting.kind === "tool_approval") {
    // The calls the pause is about are waiting for a person, not running.
    const names = new Set(awaiting.tools.map((t) => t.name));
    let tools = next.tools;
    for (const tool of Object.values(next.tools)) {
      if (tool.taskId === awaiting.taskId && tool.status === "running" && names.has(tool.name)) {
        tools = { ...tools, [tool.id]: { ...tool, status: "waiting" } };
      }
    }
    next = { ...next, tools };
  }
  if (awaiting.kind === "ask_user" && !state.questions[awaiting.requestId]) {
    next = { ...next, questions: { ...next.questions, [awaiting.requestId]: { request: awaiting } } };
    next = insertAfterTask(next, { kind: "question", requestId: awaiting.requestId, taskId: awaiting.taskId });
  }
  return next;
}

/** True when a part is drawn in a message bubble. A tool-approval record is not: the Approval from the ApprovalService is the record. */
function isBubblePart(part: ChatPart): boolean {
  return part.kind === "text" || part.kind === "file" || part.kind === "ask_user";
}

/**
 * Splits one message into the timeline: its prose (a bubble, upserted by id),
 * its tool calls and results (activity), and any A2UI surfaces it carries.
 */
export function addMessage(state: ConversationState, message: ChatMessage): ConversationState {
  let next = state;
  const bubble = message.parts.filter(isBubblePart);
  if (bubble.length > 0 && !next.hidden[message.id]) {
    const stored: ChatMessage = { ...message, parts: bubble };
    const exists = !!next.messages[message.id];
    next = {
      ...next,
      messages: { ...next.messages, [message.id]: stored },
      entries: exists ? next.entries : [...next.entries, { kind: "message", id: message.id, taskId: message.taskId }],
    };
  }
  for (const part of message.parts) {
    if (part.kind === "data" && part.dataKind === "tool_call") next = addToolCall(next, part.data, message);
    if (part.kind === "data" && (part.dataKind === "tool_result" || part.dataKind === "tool_not_run")) {
      next = addToolResult(next, part.data, part.dataKind === "tool_not_run", message);
    }
    const surfaceMessages = a2uiFromPart(part);
    for (const surfaceId of surfaceIdsIn(surfaceMessages)) {
      if (next.entries.some((e) => e.kind === "surface" && e.surfaceId === surfaceId)) continue;
      next = { ...next, entries: [...next.entries, { kind: "surface", surfaceId, taskId: message.taskId }] };
    }
  }
  return next;
}

function toolId(data: Record<string, unknown>, fallback: string): string {
  return typeof data.id === "string" && data.id !== "" ? data.id : fallback;
}

function addToolCall(state: ConversationState, data: Record<string, unknown>, message: ChatMessage): ConversationState {
  const name = typeof data.name === "string" ? data.name : "tool";
  const id = toolId(data, `${message.id}:${name}`);
  const existing = state.tools[id];
  const tool: ToolActivity = existing
    ? { ...existing, args: data.args ?? existing.args }
    : { id, name, args: data.args ?? {}, status: "running", taskId: message.taskId, at: message.createdAt };
  const entries = existing ? state.entries : [...state.entries, { kind: "tool" as const, id, taskId: message.taskId }];
  return { ...state, tools: { ...state.tools, [id]: tool }, entries };
}

function addToolResult(
  state: ConversationState,
  data: Record<string, unknown>,
  notRun: boolean,
  message: ChatMessage,
): ConversationState {
  const name = typeof data.name === "string" ? data.name : "tool";
  const id = toolId(data, `${message.id}:${name}`);
  const response = data.response;
  const failed =
    !!response && typeof response === "object" && !Array.isArray(response) &&
    ((response as Record<string, unknown>).isError === true || typeof (response as Record<string, unknown>).error === "string");
  const status: ToolStatus = notRun ? "not_run" : failed ? "failed" : "done";
  const existing = state.tools[id];
  const tool: ToolActivity = existing
    ? { ...existing, result: response, status }
    : { id, name, args: {}, result: response, status, taskId: message.taskId, at: message.createdAt };
  const entries = existing ? state.entries : [...state.entries, { kind: "tool" as const, id, taskId: message.taskId }];
  return { ...state, tools: { ...state.tools, [id]: tool }, entries };
}

/** Places an entry after the last entry of its task, or at the end. */
function insertAfterTask(state: ConversationState, entry: Entry): ConversationState {
  const taskId = entry.taskId;
  let index = -1;
  if (taskId) {
    for (let i = state.entries.length - 1; i >= 0; i -= 1) {
      if (state.entries[i]!.taskId === taskId) {
        index = i;
        break;
      }
    }
  }
  if (index === -1) return { ...state, entries: [...state.entries, entry] };
  const entries = [...state.entries];
  entries.splice(index + 1, 0, entry);
  return { ...state, entries };
}

/** The turn currently running or parked, if any. */
export function activeTurn(state: ConversationState): { taskId: string; state: ChatTurnState } | undefined {
  for (let i = state.turnOrder.length - 1; i >= 0; i -= 1) {
    const taskId = state.turnOrder[i]!;
    const turn = state.turns[taskId];
    if (turn === "working" || turn === "submitted" || turn === "input_required") return { taskId, state: turn };
  }
  return undefined;
}

/** The last entry index of each completed turn, for "Try again from here". */
export function completedTurnEnds(state: ConversationState): Map<number, string> {
  const ends = new Map<number, string>();
  const seen = new Set<string>();
  for (let i = state.entries.length - 1; i >= 0; i -= 1) {
    const taskId = state.entries[i]!.taskId;
    if (!taskId || seen.has(taskId)) continue;
    seen.add(taskId);
    if (state.turns[taskId] === "completed") ends.set(i, taskId);
  }
  return ends;
}
