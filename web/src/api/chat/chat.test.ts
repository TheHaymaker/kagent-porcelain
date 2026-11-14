import { describe, expect, it } from "vitest";
import { create, fromJson } from "@bufbuild/protobuf";
import { ValueSchema } from "@bufbuild/protobuf/wkt";
import { PartSchema, StreamResponseSchema, TaskState, type StreamResponse } from "@/gen/a2a_pb";
import { A2UI_MIME_TYPE } from "./a2aMetadata";
import { FrameReader, toPart } from "./a2aChatClient";
import { askUserAnswer, HITL_EXTENSION_URI, readHitlRequest } from "./hitl";

function status(state: TaskState, metadata?: Record<string, unknown>, text = "fallback"): StreamResponse {
  return create(StreamResponseSchema, {
    payload: {
      case: "statusUpdate",
      value: {
        taskId: "t1",
        contextId: "c1",
        status: {
          state,
          message: {
            messageId: `m-${Math.random()}`,
            role: 2,
            parts: [{ content: { case: "text", value: text } }],
            metadata: metadata as never,
            extensions: metadata ? [HITL_EXTENSION_URI] : [],
          },
        },
      },
    },
  });
}

describe("HITL detection", () => {
  it("reads a tool approval request only when the extension is declared", () => {
    const meta = { [HITL_EXTENSION_URI]: { type: "tool_approval_request", hint: "Deploy?", tools: [{ id: "tool-1", name: "deploy", args: { a: 1 } }] } };
    expect(readHitlRequest("t1", meta, [HITL_EXTENSION_URI])).toMatchObject({ kind: "tool_approval", hint: "Deploy?", tools: [{ id: "tool-1", name: "deploy" }] });
    expect(readHitlRequest("t1", meta, [])).toBeUndefined();
  });

  it("reads ask_user questions and builds the positional answer", () => {
    const meta = { [HITL_EXTENSION_URI]: { type: "ask_user_request", id: "q1", questions: [{ question: "Who?", choices: ["A", "B"] }] } };
    expect(readHitlRequest("t1", meta, [HITL_EXTENSION_URI])).toMatchObject({ kind: "ask_user", requestId: "q1", questions: [{ multiple: false }] });
    expect(askUserAnswer("q1", [["A"]])).toEqual({ [HITL_EXTENSION_URI]: { type: "ask_user_response", id: "q1", answers: [{ answer: ["A"] }] } });
  });

  it("treats an unknown pause as unknown, not as nothing", () => {
    const meta = { [HITL_EXTENSION_URI]: { type: "something_new" } };
    expect(readHitlRequest("t1", meta, [HITL_EXTENSION_URI])).toEqual({ kind: "unknown", taskId: "t1" });
  });

  it("an input-required frame carries the request and hides its fallback prose", () => {
    const reader = new FrameReader();
    const meta = { [HITL_EXTENSION_URI]: { type: "tool_approval_request", tools: [{ id: "tool-1", name: "deploy", args: {} }] } };
    const events = [...reader.read(status(TaskState.INPUT_REQUIRED, meta, "Approval is required for tool(s): deploy"))];
    expect(events.map((e) => e.type)).toEqual(["status"]);
    expect(events[0]).toMatchObject({ state: "input_required", awaiting: { kind: "tool_approval" }, text: "Approval is required for tool(s): deploy" });
  });

  it("an input-required frame without the extension is 'unknown' and shows its prose", () => {
    const reader = new FrameReader();
    const events = [...reader.read(status(TaskState.INPUT_REQUIRED, undefined, "Please approve"))];
    expect(events.map((e) => e.type)).toEqual(["message", "status"]);
    expect(events[1]).toMatchObject({ awaiting: { kind: "unknown" } });
  });
});

describe("parts", () => {
  it("turns an a2ui DataPart (array) into an a2ui part", () => {
    const part = create(PartSchema, {
      content: { case: "data", value: fromJson(ValueSchema, [{ createSurface: { surfaceId: "x" } }]) },
      metadata: { mimeType: A2UI_MIME_TYPE },
    });
    expect(toPart(part)).toEqual({ kind: "a2ui", messages: [{ createSurface: { surfaceId: "x" } }] });
  });

  it("classifies tool calls and results by kagent.dev/a2a/part-type", () => {
    const call = create(PartSchema, {
      content: { case: "data", value: fromJson(ValueSchema, { id: "1", name: "write_file", args: { path: "a" } }) },
      metadata: { "kagent.dev/a2a/part-type": "function_call" },
    });
    expect(toPart(call)).toMatchObject({ kind: "data", dataKind: "tool_call" });
  });

  it("appends streamed artifact chunks as deltas", () => {
    const reader = new FrameReader();
    const chunk = (text: string, append: boolean) =>
      create(StreamResponseSchema, {
        payload: { case: "artifactUpdate", value: { taskId: "t1", artifact: { artifactId: "a1", parts: [{ content: { case: "text", value: text } }] }, append } },
      });
    const events = [...reader.read(chunk("Hel", false)), ...reader.read(chunk("lo", true))];
    expect(events).toMatchObject([{ type: "message", message: { id: "a1" } }, { type: "delta", messageId: "a1", text: "lo" }]);
  });
});
