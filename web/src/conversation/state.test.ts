import { describe, expect, it } from "vitest";
import type { ChatMessage } from "@/api/chat/types";
import {
  activeTurn,
  completedTurnEnds,
  conversationReducer,
  initialConversation,
  type ConversationState,
} from "./state";

const msg = (
  id: string,
  parts: ChatMessage["parts"],
  taskId = "t1",
  role: ChatMessage["role"] = "agent",
): ChatMessage => ({
  id,
  role,
  parts,
  createdAt: "2026-01-01T00:00:00Z",
  taskId,
});

function run(
  ...actions: Parameters<typeof conversationReducer>[1][]
): ConversationState {
  return actions.reduce(conversationReducer, initialConversation);
}

describe("conversation state", () => {
  it("upserts messages by id and appends deltas", () => {
    const s = run(
      {
        type: "event",
        event: {
          type: "message",
          message: msg("a1", [{ kind: "text", text: "Hel" }]),
        },
      },
      { type: "event", event: { type: "delta", messageId: "a1", text: "lo" } },
      {
        type: "event",
        event: {
          type: "message",
          message: msg("a1", [{ kind: "text", text: "Hello!" }]),
        },
      },
    );
    expect(s.entries).toHaveLength(1);
    expect(s.messages.a1!.parts).toEqual([{ kind: "text", text: "Hello!" }]);
  });

  it("tracks tool calls and results as activity", () => {
    const s = run(
      {
        type: "event",
        event: {
          type: "message",
          message: msg("m1", [
            {
              kind: "data",
              dataKind: "tool_call",
              data: { id: "c1", name: "write_file", args: { path: "a.py" } },
            },
          ]),
        },
      },
      {
        type: "event",
        event: {
          type: "message",
          message: msg("m2", [
            {
              kind: "data",
              dataKind: "tool_result",
              data: {
                id: "c1",
                name: "write_file",
                response: { isError: false },
              },
            },
          ]),
        },
      },
      {
        type: "event",
        event: {
          type: "message",
          message: msg("m3", [
            {
              kind: "data",
              dataKind: "tool_result",
              data: { id: "c2", name: "run", response: { isError: true } },
            },
          ]),
        },
      },
    );
    expect(s.tools.c1).toMatchObject({
      status: "done",
      args: { path: "a.py" },
    });
    expect(s.tools.c2!.status).toBe("failed");
    expect(s.entries.filter((e) => e.kind === "tool")).toHaveLength(2);
    expect(s.entries.some((e) => e.kind === "message")).toBe(false);
  });

  it("places a surface once, whichever route it arrives by", () => {
    const messages = [{ createSurface: { surfaceId: "d1" } }];
    const s = run(
      {
        type: "event",
        event: {
          type: "message",
          message: msg("m1", [{ kind: "a2ui", messages }]),
        },
      },
      {
        type: "event",
        event: {
          type: "message",
          message: msg("m2", [
            {
              kind: "data",
              dataKind: "tool_result",
              data: {
                id: "x",
                name: "workbench_show",
                response: {
                  structuredContent: { a2ui: { surfaceId: "d1", messages } },
                },
              },
            },
          ]),
        },
      },
      {
        type: "surfaces",
        surfaces: [
          { surfaceId: "d1", taskId: "t1" },
          { surfaceId: "d2", taskId: "t1" },
        ],
      },
    );
    expect(
      s.entries
        .filter((e) => e.kind === "surface")
        .map((e) => (e.kind === "surface" ? e.surfaceId : "")),
    ).toEqual(["d1", "d2"]);
  });

  it("an ask_user pause adds one question; answering it clears the wait and hides the echo", () => {
    const awaiting = {
      kind: "ask_user" as const,
      taskId: "t1",
      requestId: "q1",
      questions: [{ question: "Who?", choices: ["A"], multiple: false }],
    };
    let s = run(
      {
        type: "event",
        event: {
          type: "status",
          state: "input_required",
          taskId: "t1",
          awaiting,
        },
      },
      {
        type: "event",
        event: {
          type: "status",
          state: "input_required",
          taskId: "t1",
          awaiting,
        },
      },
    );
    expect(s.entries.filter((e) => e.kind === "question")).toHaveLength(1);
    expect(activeTurn(s)).toEqual({ taskId: "t1", state: "input_required" });
    s = conversationReducer(s, {
      type: "answered",
      requestId: "q1",
      answers: [["A"]],
      messageId: "u1",
    });
    expect(s.awaiting).toBeUndefined();
    s = conversationReducer(s, {
      type: "event",
      event: {
        type: "message",
        message: msg(
          "u1",
          [
            {
              kind: "ask_user",
              interaction: { questions: [], answers: [["A"]] },
            },
          ],
          "t1",
          "user",
        ),
      },
    });
    expect(s.entries.some((e) => e.kind === "message" && e.id === "u1")).toBe(
      false,
    );
  });

  it("a tool-approval pause marks the paused call as waiting, and approvals sit after their turn", () => {
    let s = run(
      {
        type: "event",
        event: {
          type: "message",
          message: msg("m1", [
            {
              kind: "data",
              dataKind: "tool_call",
              data: { id: "c1", name: "deploy", args: {} },
            },
          ]),
        },
      },
      {
        type: "event",
        event: {
          type: "message",
          message: msg("x", [{ kind: "text", text: "other turn" }], "t2"),
        },
      },
      {
        type: "event",
        event: {
          type: "status",
          state: "input_required",
          taskId: "t1",
          awaiting: {
            kind: "tool_approval",
            taskId: "t1",
            tools: [{ id: "tool-1", name: "deploy", args: {} }],
          },
        },
      },
    );
    expect(s.tools.c1!.status).toBe("waiting");
    s = conversationReducer(s, {
      type: "approvals",
      approvals: [
        { id: "apr_1", taskId: "t1" },
        { id: "apr_1", taskId: "t1" },
      ],
    });
    expect(s.entries.map((e) => e.kind)).toEqual([
      "tool",
      "approval",
      "message",
    ]);
    s = conversationReducer(s, {
      type: "event",
      event: { type: "status", state: "completed", taskId: "t1" },
    });
    expect(s.awaiting).toBeUndefined();
    expect(s.tools.c1!.status).toBe("done");
  });

  it("does not draw tool-approval records as chat bubbles", () => {
    const s = run({
      type: "event",
      event: {
        type: "message",
        message: msg(
          "r",
          [
            {
              kind: "tool_approval",
              approval: {
                tools: [],
                decisions: [{ id: "tool-1", approved: true }],
              },
            },
          ],
          "t1",
          "user",
        ),
      },
    });
    expect(s.entries).toHaveLength(0);
  });

  it("finds the last entry of each completed turn", () => {
    const s = run(
      {
        type: "event",
        event: {
          type: "message",
          message: msg("a", [{ kind: "text", text: "1" }], "t1"),
        },
      },
      {
        type: "event",
        event: {
          type: "message",
          message: msg("b", [{ kind: "text", text: "2" }], "t1"),
        },
      },
      {
        type: "event",
        event: { type: "status", state: "completed", taskId: "t1" },
      },
      {
        type: "event",
        event: {
          type: "message",
          message: msg("c", [{ kind: "text", text: "3" }], "t2"),
        },
      },
      {
        type: "event",
        event: { type: "status", state: "working", taskId: "t2" },
      },
    );
    expect([...completedTurnEnds(s).entries()]).toEqual([[1, "t1"]]);
  });
});

describe("approval placement", () => {
  it("puts an approval card where it happened, not after the task's last entry", () => {
    const msg = (id: string, createdAt: string, text: string) => ({
      id,
      role: "agent" as const,
      parts: [{ kind: "text" as const, text }],
      createdAt,
      taskId: "t1",
    });
    let s = conversationReducer(initialConversation, {
      type: "local-user",
      message: msg("m1", "2026-11-16T07:00:00Z", "deploy it"),
    });
    s = conversationReducer(s, {
      type: "local-user",
      message: msg("m2", "2026-11-16T07:10:00Z", "deployed! here is the link"),
    });
    s = conversationReducer(s, {
      type: "approvals",
      approvals: [
        { id: "a1", taskId: "t1", createdAt: "2026-11-16T07:05:00Z" },
      ],
    });
    expect(
      s.entries.map((e) =>
        e.kind === "approval"
          ? e.approvalId
          : e.kind === "message"
            ? e.id
            : e.kind,
      ),
    ).toEqual(["m1", "a1", "m2"]);
  });
});
