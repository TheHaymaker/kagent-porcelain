/**
 * An in-process stand-in for kagent's A2A gateway and one builder agent, used
 * by the fixture transport. It produces the same frames the real gateway does
 * (status updates carrying tool-call DataParts, appended text artifacts, HITL
 * pauses with the extension metadata) so the app's real chat client reads them.
 */
import { create, fromJson, type JsonObject, type JsonValue } from "@bufbuild/protobuf";
import { timestampFromDate, ValueSchema } from "@bufbuild/protobuf/wkt";
import {
  ArtifactSchema,
  MessageSchema,
  PartSchema,
  Role,
  StreamResponseSchema,
  TaskSchema,
  TaskState,
  type Artifact,
  type Message,
  type Part,
  type StreamResponse,
  type Task,
  type TaskStatus,
} from "@/gen/a2a_pb";
import { HITL_EXTENSION_URI } from "@/api/chat/hitl";
import { A2A_METADATA, A2UI_MIME_TYPE } from "@/api/chat/a2aMetadata";

let lastPosition = 0;
/** Strictly increasing RFC 3339 timeline positions. */
export function nextPosition(at = Date.now()): string {
  lastPosition = Math.max(lastPosition + 1, at);
  return new Date(lastPosition).toISOString();
}

let idCounter = 0;
export function mockId(prefix: string): string {
  idCounter += 1;
  return `${prefix}-${idCounter.toString(36)}-${Math.random().toString(36).slice(2, 7)}`;
}

export function uuid(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return "xxxxxxxx-xxxx-4xxx-8xxx-xxxxxxxxxxxx".replace(/x/g, () => Math.floor(Math.random() * 16).toString(16));
}

export function textPart(text: string): Part {
  return create(PartSchema, { content: { case: "text", value: text } });
}

export function dataPart(data: JsonValue, metadata?: JsonObject, mediaType = ""): Part {
  return create(PartSchema, { content: { case: "data", value: fromJson(ValueSchema, data) }, metadata, mediaType });
}

export function toolCallPart(id: string, name: string, args: JsonObject): Part {
  return dataPart({ id, name, args }, { [A2A_METADATA.partType]: "function_call" });
}

export function toolResultPart(id: string, name: string, response: JsonValue): Part {
  return dataPart({ id, name, response }, { [A2A_METADATA.partType]: "function_response" });
}

export function a2uiPart(messages: JsonValue[]): Part {
  return dataPart(messages, { mimeType: A2UI_MIME_TYPE }, A2UI_MIME_TYPE);
}

export function agentMessage(taskId: string, contextId: string, parts: Part[], extra: { metadata?: JsonObject; extensions?: string[] } = {}): Message {
  return create(MessageSchema, {
    messageId: mockId("m"),
    role: Role.AGENT,
    parts,
    taskId,
    contextId,
    metadata: { [A2A_METADATA.timelinePosition]: nextPosition() },
    ...extra,
  });
}

type Listener = () => void;

export class MockTask {
  readonly frames: StreamResponse[] = [];
  readonly history: Message[] = [];
  readonly artifacts = new Map<string, Artifact>();
  status: TaskStatus;
  private listeners = new Set<Listener>();
  private gates = new Map<string, { promise: Promise<unknown>; resolve: (v: unknown) => void }>();
  pendingAskId?: string;
  pendingApprovalId?: string;

  constructor(
    readonly id: string,
    readonly contextId: string,
    readonly agent: string,
  ) {
    this.status = { $typeName: "lf.a2a.v1.TaskStatus", state: TaskState.SUBMITTED, timestamp: timestampFromDate(new Date()) };
  }

  get state(): TaskState {
    return this.status.state;
  }

  snapshot(): Task {
    return create(TaskSchema, {
      id: this.id,
      contextId: this.contextId,
      status: this.status,
      history: [...this.history],
      artifacts: [...this.artifacts.values()],
    });
  }

  addHistory(message: Message): void {
    this.history.push(message);
  }

  /** Records a frame and wakes anyone streaming this task. */
  emit(payload: StreamResponse["payload"]): void {
    const frame = create(StreamResponseSchema, { payload });
    if (payload.case === "statusUpdate" && payload.value.status) {
      this.status = payload.value.status;
      if (payload.value.status.message) this.history.push(payload.value.status.message);
    }
    if (payload.case === "artifactUpdate" && payload.value.artifact) {
      const incoming = payload.value.artifact;
      const existing = this.artifacts.get(incoming.artifactId);
      if (existing && payload.value.append) {
        const parts = [...existing.parts];
        const last = parts.at(-1);
        const add = incoming.parts[0];
        if (last?.content.case === "text" && add?.content.case === "text") {
          parts[parts.length - 1] = textPart(last.content.value + add.content.value);
        } else parts.push(...incoming.parts);
        this.artifacts.set(incoming.artifactId, create(ArtifactSchema, { ...existing, parts }));
      } else {
        this.artifacts.set(
          incoming.artifactId,
          create(ArtifactSchema, {
            ...incoming,
            metadata: existing?.metadata ?? incoming.metadata ?? { [A2A_METADATA.timelinePosition]: nextPosition() },
          }),
        );
      }
    }
    this.frames.push(frame);
    for (const l of [...this.listeners]) l();
  }

  setStatus(state: TaskState, message?: Message): void {
    this.emit({
      case: "statusUpdate",
      value: {
        $typeName: "lf.a2a.v1.TaskStatusUpdateEvent",
        taskId: this.id,
        contextId: this.contextId,
        status: { $typeName: "lf.a2a.v1.TaskStatus", state, message, timestamp: timestampFromDate(new Date()) },
      },
    });
  }

  /** Streams frames from `from` until the task stops (input required or terminal). */
  async *follow(from: number, signal?: AbortSignal): AsyncGenerator<StreamResponse> {
    let index = from;
    for (;;) {
      while (index < this.frames.length) {
        const frame = this.frames[index++]!;
        yield frame;
        if (frame.payload.case === "statusUpdate" && isStop(frame.payload.value.status?.state)) {
          if (index >= this.frames.length) return;
        }
      }
      if (isStop(this.state) || signal?.aborted) return;
      await new Promise<void>((resolve) => {
        const done = () => {
          this.listeners.delete(done);
          signal?.removeEventListener("abort", done);
          resolve();
        };
        this.listeners.add(done);
        signal?.addEventListener("abort", done, { once: true });
      });
    }
  }

  gate<T>(key: string): Promise<T> {
    let entry = this.gates.get(key);
    if (!entry) {
      let resolve!: (v: unknown) => void;
      const promise = new Promise<unknown>((r) => (resolve = r));
      entry = { promise, resolve };
      this.gates.set(key, entry);
    }
    return entry.promise as Promise<T>;
  }

  open(key: string, value: unknown): void {
    const entry = this.gates.get(key);
    if (entry) entry.resolve(value);
    else {
      this.gates.set(key, { promise: Promise.resolve(value), resolve: () => undefined });
    }
  }
}

export function isStop(state: TaskState | undefined): boolean {
  return (
    state === TaskState.INPUT_REQUIRED ||
    state === TaskState.AUTH_REQUIRED ||
    state === TaskState.COMPLETED ||
    state === TaskState.FAILED ||
    state === TaskState.CANCELED ||
    state === TaskState.REJECTED
  );
}

export function hitlMessage(task: MockTask, text: string, payload: JsonObject): Message {
  return agentMessage(task.id, task.contextId, [textPart(text)], {
    metadata: { [HITL_EXTENSION_URI]: payload, [A2A_METADATA.timelinePosition]: nextPosition() },
    extensions: [HITL_EXTENSION_URI],
  });
}
