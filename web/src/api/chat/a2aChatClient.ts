/**
 * NOTICE: Adapted from kagent-dev/kagent ui/src/api/chat/a2aGrpcChatClient.ts, Apache-2.0.
 *
 * The chat transport: A2A 1.0 over gRPC-Web (`lf.a2a.v1.A2AService`), with the
 * kagent human-in-the-loop extension activated on every call.
 *
 * Carried over from kagent's client because each was a bug fixed against a live
 * controller: an artifact repeating text already shown as status text is
 * dropped; an artifact's `append` flag is honoured (that is how the runtime
 * streams tokens); a status message carrying a HITL request does not render its
 * fallback prose, because the structured request is drawn instead.
 *
 * Added for Workbench: DataParts whose media type is `application/a2ui+json`
 * become `a2ui` parts, and `follow()` re-attaches to a task with
 * `SubscribeToTask` — which is how a turn that was resumed server-side (by an
 * ApprovalService decision) is watched to the end.
 */
import { create, toJson, type JsonObject } from "@bufbuild/protobuf";
import { ValueSchema } from "@bufbuild/protobuf/wkt";
import {
  MessageSchema,
  Role,
  TaskState,
  type Message as A2AMessage,
  type Part as A2APart,
  type StreamResponse,
  type Task as A2ATask,
  type TaskStatus,
} from "@/gen/a2a_pb";
import { api } from "../clients";
import { A2A_METADATA, A2UI_MIME_TYPE, metadataString } from "./a2aMetadata";
import {
  HITL_EXTENSION_HEADER,
  HITL_EXTENSION_URI,
  readAskUserResponse,
  readHitlRequest,
  readToolApprovalResponse,
  type PendingRequest,
} from "./hitl";
import type {
  ChatConversationRef,
  ChatEvent,
  ChatHistory,
  ChatMessage,
  ChatPart,
  ChatTurnState,
  SendMessageInput,
} from "./types";

const HISTORY_PAGE_LIMIT = 50;
const SHARE_HEADER = "X-Share-Token";

let counter = 0;
function nextId(prefix: string): string {
  counter += 1;
  return `${prefix}-${Date.now().toString(36)}-${counter}`;
}

/** A share token the page was opened with, per conversation (see ShareDialog). */
export function shareTokenFor(sessionId: string): string | undefined {
  try {
    return sessionStorage.getItem(`workbench.share.${sessionId}`) ?? undefined;
  } catch {
    return undefined;
  }
}

function isAwaitingReply(state: TaskState | undefined): boolean {
  return state === TaskState.INPUT_REQUIRED || state === TaskState.AUTH_REQUIRED;
}

export function pendingRequestFromStatus(
  taskId: string,
  status: TaskStatus | undefined,
): PendingRequest | undefined {
  if (!taskId || !isAwaitingReply(status?.state)) return undefined;
  return (
    readHitlRequest(taskId, status?.message?.metadata, status?.message?.extensions) ?? {
      kind: "unknown",
      taskId,
    }
  );
}

const TURN_STATE: Partial<Record<TaskState, ChatTurnState>> = {
  [TaskState.SUBMITTED]: "submitted",
  [TaskState.WORKING]: "working",
  [TaskState.COMPLETED]: "completed",
  [TaskState.FAILED]: "failed",
  [TaskState.CANCELED]: "canceled",
  [TaskState.INPUT_REQUIRED]: "input_required",
  [TaskState.REJECTED]: "failed",
  [TaskState.AUTH_REQUIRED]: "input_required",
};

/** Unnamed states are treated as still running: a stuck spinner beats a stranded composer. */
export function turnState(state: TaskState | undefined): ChatTurnState {
  if (state === undefined) return "working";
  return TURN_STATE[state] ?? "working";
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** One A2A part as something the transcript can render. */
export function toPart(part: A2APart): ChatPart | undefined {
  const content = part.content;
  const metadata = part.metadata as Record<string, unknown> | undefined;
  if (content.case === "text") {
    return { kind: "text", text: content.value, ...(metadata?.[A2A_METADATA.thought] === true ? { thought: true } : {}) };
  }
  if (content.case === "data") {
    // `Part.data` is a google.protobuf.Value: unwrap it to plain JSON first.
    const value = toJson(ValueSchema, content.value);
    const mime = (part.mediaType || metadataString(metadata, "mimeType") || "").split(";", 1)[0]?.trim().toLowerCase();
    if (mime === A2UI_MIME_TYPE) {
      return { kind: "a2ui", messages: Array.isArray(value) ? value : isObject(value) ? [value] : [] };
    }
    if (!isObject(value)) return undefined;
    const partType = metadataString(metadata, A2A_METADATA.partType);
    const dataKind =
      partType === "function_call" ? "tool_call" : partType === "function_response" ? "tool_result" : "unknown";
    return { kind: "data", dataKind, data: value };
  }
  if (content.case === "url") {
    if (!/^https?:\/\//i.test(content.value)) return undefined;
    return { kind: "file", name: part.filename || content.value.split("/").pop() || "file", mediaType: part.mediaType, url: content.value };
  }
  if (content.case === "raw") {
    return { kind: "file", name: part.filename || "file", mediaType: part.mediaType || "application/octet-stream" };
  }
  return undefined;
}

export function toParts(parts: readonly A2APart[] | undefined): ChatPart[] {
  const result: ChatPart[] = [];
  for (const source of parts ?? []) {
    const part = toPart(source);
    if (!part) continue;
    const previous = result.at(-1);
    if (part.kind === "text" && previous?.kind === "text" && !!previous.thought === !!part.thought) {
      previous.text += part.text;
      continue;
    }
    result.push(part);
  }
  return visibleParts(result);
}

const REJECTED_BY_USER = /^Tool call was rejected by user\.(?: Reason: [\s\S]+)?$/;

/**
 * Hides runtime control-plane traffic: the `ask_user` tool itself (its question
 * is drawn from the HITL request) and ADK's "requires confirmation" results.
 * A rejected call is kept but marked as not run.
 */
function visibleParts(parts: ChatPart[]): ChatPart[] {
  const visible: ChatPart[] = [];
  for (const part of parts) {
    if (part.kind !== "data") {
      visible.push(part);
      continue;
    }
    if (part.data.name === "ask_user" || part.data.name === "adk_request_confirmation") continue;
    if (part.dataKind === "tool_result" && isObject(part.data.response)) {
      const error = part.data.response.error;
      if (typeof error === "string") {
        if (/requires confirmation, please approve or reject$/.test(error)) continue;
        if (/call is rejected$/.test(error) || REJECTED_BY_USER.test(error)) {
          visible.push({ ...part, dataKind: "tool_not_run" });
          continue;
        }
      }
    }
    visible.push(part);
  }
  return visible;
}

function textOf(parts: readonly ChatPart[]): string {
  return parts
    .filter((p): p is ChatPart & { kind: "text" } => p.kind === "text" && !p.thought)
    .map((p) => p.text)
    .join("");
}

function statusTime(status: TaskStatus | undefined): string {
  const seconds = status?.timestamp?.seconds;
  if (seconds === undefined) return new Date().toISOString();
  return new Date(Number(seconds) * 1000).toISOString();
}

/**
 * Turns a stream of A2A frames into ChatEvents. One reader per stream; it keeps
 * the per-turn state (artifacts being appended, text already shown).
 */
export class FrameReader {
  private statusReply = "";
  private readonly delivered: Set<string>;
  private readonly artifacts = new Map<string, ChatPart[]>();

  constructor(alreadyShown: readonly string[] = []) {
    this.delivered = new Set(alreadyShown);
  }

  *read(frame: StreamResponse): Generator<ChatEvent> {
    const payload = frame.payload;
    if (payload.case === "statusUpdate") {
      const event = payload.value;
      const status = event.status;
      const message = status?.message;
      const awaiting = pendingRequestFromStatus(event.taskId, status);
      const parts = toParts(message?.parts);
      let fallbackText: string | undefined;
      if (message && parts.length > 0) {
        if (awaiting === undefined || awaiting.kind === "unknown") {
          const id = message.messageId || nextId("status");
          if (!this.delivered.has(id)) {
            this.delivered.add(id);
            const role = message.role === Role.USER ? "user" : "agent";
            if (role === "agent") this.statusReply = textOf(parts);
            yield { type: "message", message: { id, role, parts, createdAt: statusTime(status), taskId: event.taskId } };
          }
        } else {
          fallbackText = textOf(parts);
        }
      }
      yield { type: "status", state: turnState(status?.state), taskId: event.taskId, awaiting, text: fallbackText };
      return;
    }
    if (payload.case === "artifactUpdate") {
      const event = payload.value;
      const parts = toParts(event.artifact?.parts);
      if (parts.length === 0) return;
      const body = textOf(parts);
      const artifactId = event.artifact?.artifactId || "";
      if (artifactId && this.artifacts.has(artifactId)) {
        const previous = this.artifacts.get(artifactId) ?? [];
        const textOnly = previous.every((p) => p.kind === "text") && parts.every((p) => p.kind === "text");
        const combined = event.append ? [...previous, ...parts] : parts;
        const updated: ChatPart[] = textOnly ? [{ kind: "text", text: textOf(combined) }] : combined;
        this.artifacts.set(artifactId, updated);
        if (event.append && textOnly) {
          yield { type: "delta", messageId: artifactId, text: body, taskId: event.taskId };
        } else {
          yield {
            type: "message",
            message: { id: artifactId, role: "agent", parts: updated, createdAt: new Date().toISOString(), taskId: event.taskId },
          };
        }
        return;
      }
      if (body !== "" && this.statusReply === body && parts.every((p) => p.kind === "text")) {
        this.statusReply = "";
        return;
      }
      this.statusReply = "";
      const id = artifactId || nextId("artifact");
      this.artifacts.set(id, parts);
      yield { type: "message", message: { id, role: "agent", parts, createdAt: new Date().toISOString(), taskId: event.taskId } };
      return;
    }
    if (payload.case === "task") {
      const task = payload.value;
      const artifactIds = new Set(task.artifacts.map((a) => a.artifactId));
      for (const message of messagesFromTask(task)) {
        if (!artifactIds.has(message.id)) {
          if (this.delivered.has(message.id)) continue;
          this.delivered.add(message.id);
        }
        yield { type: "message", message };
      }
      for (const artifact of task.artifacts) {
        if (artifact.artifactId) this.artifacts.set(artifact.artifactId, toParts(artifact.parts));
      }
      yield {
        type: "status",
        state: turnState(task.status?.state),
        taskId: task.id,
        awaiting: pendingRequestFromStatus(task.id, task.status),
      };
      return;
    }
    if (payload.case === "message") {
      const message = payload.value;
      const parts = toParts(message.parts);
      if (parts.length === 0) return;
      const id = message.messageId || nextId("message");
      if (this.delivered.has(id)) return;
      this.delivered.add(id);
      yield {
        type: "message",
        message: {
          id,
          role: message.role === Role.USER ? "user" : "agent",
          parts,
          createdAt: new Date().toISOString(),
          taskId: message.taskId || undefined,
        },
      };
    }
  }
}

export class A2AChatClient {
  readonly protocolVersion = "A2A 1.0 over gRPC-Web";

  private callOptions(conversation: ChatConversationRef, signal?: AbortSignal) {
    // The HITL extension is activated on every call: a turn sent without it
    // parks with bare prose and no correlation id, and can never be answered.
    const headers: Record<string, string> = { [HITL_EXTENSION_HEADER]: HITL_EXTENSION_URI };
    const share = shareTokenFor(conversation.id);
    if (share) headers[SHARE_HEADER] = share;
    return { signal, headers };
  }

  async history(conversation: ChatConversationRef, options: { signal?: AbortSignal } = {}): Promise<ChatHistory> {
    const messages: ChatMessage[] = [];
    const turns: ChatHistory["turns"] = [];
    let awaitingReply: PendingRequest | undefined;
    let pageToken = "";
    for (let page = 0; page < HISTORY_PAGE_LIMIT; page += 1) {
      const response = await api.a2a.listTasks(
        { tenant: conversation.agent, contextId: conversation.id, pageToken, includeArtifacts: true },
        this.callOptions(conversation, options.signal),
      );
      for (const task of response.tasks) {
        messages.push(...messagesFromTask(task));
        turns.push({ taskId: task.id, state: turnState(task.status?.state) });
        if (isAwaitingReply(task.status?.state) && task.id) {
          awaitingReply = pendingRequestFromStatus(task.id, task.status);
        }
      }
      const next = response.nextPageToken;
      if (!next || next === pageToken) break;
      pageToken = next;
    }
    return { messages, turns, awaitingReply };
  }

  async *send(input: SendMessageInput): AsyncIterable<ChatEvent> {
    const { conversation, text, signal } = input;
    const request = {
      tenant: conversation.agent,
      message: create(MessageSchema, {
        messageId: input.messageId || nextId("msg"),
        role: Role.USER,
        parts: text ? [{ content: { case: "text" as const, value: text } }] : [],
        contextId: conversation.id,
        // An answer declares the extension on the message itself, or the
        // runtime forwards it as plain text and the structured answer is lost.
        extensions: input.hitl ? [HITL_EXTENSION_URI] : [],
        metadata: input.hitl as JsonObject | undefined,
        taskId: input.taskId || "",
      }),
    };
    const reader = new FrameReader(input.messageId ? [input.messageId] : []);
    const stream = api.a2a.sendStreamingMessage(request, this.callOptions(conversation, signal));
    for await (const frame of stream) yield* reader.read(frame);
  }

  /** Re-attaches to a task (e.g. one resumed by an approval) and streams it to its next stop. */
  async *follow(conversation: ChatConversationRef, taskId: string, signal?: AbortSignal): AsyncIterable<ChatEvent> {
    const reader = new FrameReader();
    const stream = api.a2a.subscribeToTask({ tenant: conversation.agent, id: taskId }, this.callOptions(conversation, signal));
    for await (const frame of stream) yield* reader.read(frame);
  }

  async cancel(conversation: ChatConversationRef, taskId: string): Promise<void> {
    await api.a2a.cancelTask({ tenant: conversation.agent, id: taskId }, this.callOptions(conversation));
  }
}

let instance: A2AChatClient | undefined;
export function chatClient(): A2AChatClient {
  instance ??= new A2AChatClient();
  return instance;
}

/**
 * The messages of a stored task, for replaying a conversation. Ordered by the
 * `kagent.dev/a2a/timeline-position` metadata when every entry has it, and
 * otherwise history first, then artifacts not already repeated in the history.
 */
export function messagesFromTask(task: A2ATask): ChatMessage[] {
  const positioned: { message: ChatMessage; position?: string }[] = [];
  const taken = new Set<string>();
  const createdAt = statusTime(task.status);
  let pendingTools: Extract<PendingRequest, { kind: "tool_approval" }> | undefined;
  const pendingQuestions = new Map<string, Extract<PendingRequest, { kind: "ask_user" }>>();
  let index = 0;

  const push = (message: A2AMessage) => {
    const request = readHitlRequest(task.id, message.metadata, message.extensions);
    if (request?.kind === "tool_approval") {
      pendingTools = request;
      return;
    }
    if (request?.kind === "ask_user") {
      pendingQuestions.set(request.requestId, request);
      return;
    }
    const decisions = readToolApprovalResponse(message.metadata, message.extensions);
    const answer = readAskUserResponse(message.metadata, message.extensions);
    const question = answer ? pendingQuestions.get(answer.requestId) : undefined;
    const parts: ChatPart[] = decisions
      ? [{ kind: "tool_approval", approval: { tools: pendingTools?.tools ?? [], decisions, askedBy: pendingTools?.askedBy } }]
      : answer
        ? [{ kind: "ask_user", interaction: { questions: question?.questions ?? [], answers: answer.answers, askedBy: question?.askedBy } }]
        : toParts(message.parts);
    if (decisions) pendingTools = undefined;
    if (answer) pendingQuestions.delete(answer.requestId);
    if (parts.length === 0) return;
    const identity = message.messageId || JSON.stringify(parts);
    if (taken.has(identity)) return;
    taken.add(identity);
    positioned.push({
      message: {
        id: message.messageId || `${task.id}-message-${index++}`,
        role: message.role === Role.USER ? "user" : "agent",
        parts,
        createdAt,
        taskId: task.id || undefined,
      },
      position: metadataString(message.metadata as Record<string, unknown> | undefined, A2A_METADATA.timelinePosition),
    });
  };

  for (const message of task.history) push(message);
  if (task.status?.message) push(task.status.message);

  const shown = new Set(positioned.map(({ message }) => textOf(message.parts)));
  for (const artifact of task.artifacts) {
    const parts = toParts(artifact.parts);
    const body = textOf(parts);
    const position = metadataString(artifact.metadata as Record<string, unknown> | undefined, A2A_METADATA.timelinePosition);
    if (parts.length === 0 || (position === undefined && body !== "" && shown.has(body))) continue;
    positioned.push({
      message: { id: artifact.artifactId || `${task.id}-artifact-${index++}`, role: "agent", parts, createdAt, taskId: task.id || undefined },
      position,
    });
  }

  if (positioned.length > 0 && positioned.every(({ position }) => position !== undefined)) {
    positioned.sort((a, b) => a.position!.localeCompare(b.position!));
  }
  return positioned.map(({ message }) => message);
}
