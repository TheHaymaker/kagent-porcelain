/**
 * The whole scripted conversation, through the real A2A chat client and the
 * real reducer over the fixture transport — the same path the browser takes.
 */
import { afterEach, describe, expect, it } from "vitest";
import { setApiTransport } from "@/api/transport";
import { api } from "@/api/clients";
import { chatClient } from "@/api/chat/a2aChatClient";
import type { ChatEvent } from "@/api/chat/types";
import { conversationReducer, initialConversation, type ConversationState } from "@/conversation/state";
import { SurfaceStore } from "@/a2ui/store";
import { a2uiFromPart, a2uiFromSurface } from "@/a2ui/extract";
import { ApprovalState, InboxView } from "@/gen/workbench/v1/approvals_pb";
import { createMockTransport } from "./transport";
import { resetMockWorld } from "./world";

afterEach(() => setApiTransport(undefined));

async function drain(events: AsyncIterable<ChatEvent>, state: ConversationState, store: SurfaceStore) {
  for await (const event of events) {
    if (event.type === "message") for (const p of event.message.parts) store.apply(a2uiFromPart(p));
    state = conversationReducer(state, { type: "event", event });
  }
  return state;
}

describe("mock world", () => {
  it("runs the scripted build: question, ordinary approval, production approval the requester cannot decide", async () => {
    const world = resetMockWorld(0);
    setApiTransport(createMockTransport(world));
    const { session } = await api.sessions.createSession({ agent: { namespace: "workbench", name: "builder" }, requestId: "r1" });
    const conv = { id: session!.id, agent: "workbench/builder" };
    const store = new SurfaceStore();

    let state = await drain(chatClient().send({ conversation: conv, text: "An expense tracker" }), initialConversation, store);
    expect(state.awaiting?.kind).toBe("ask_user");
    const ask = state.awaiting as Extract<typeof state.awaiting, { kind: "ask_user" }>;

    state = await drain(
      chatClient().send({ conversation: conv, text: "Just my team", taskId: ask!.taskId, hitl: (await import("@/api/chat/hitl")).askUserAnswer(ask!.requestId, [["Just my team"]]) }),
      state,
      store,
    );
    expect(state.awaiting?.kind).toBe("tool_approval");
    expect(store.get("deploy-1")).toBeDefined();

    const { approvals } = await api.approvals.listSessionApprovals({ sessionId: conv.id });
    const ordinary = approvals.find((a) => a.state === ApprovalState.PENDING)!;
    expect(ordinary.callerMayDecide).toBe(true);
    await api.approvals.decide({ approvalId: ordinary.id, approved: true });

    state = await drain(chatClient().follow(conv, ordinary.taskId), state, store);
    expect(state.awaiting?.kind).toBe("tool_approval");
    const after = (await api.approvals.listSessionApprovals({ sessionId: conv.id })).approvals;
    const prod = after.find((a) => a.state === ApprovalState.PENDING)!;
    expect(prod.callerMayDecide).toBe(false);
    expect(prod.requesterMayDecide).toBe(false);
    expect(prod.approverGroups).toEqual(["platform-approvers"]);
    await expect(api.approvals.decide({ approvalId: prod.id, approved: true })).rejects.toThrow();
    expect([...store.approvalIds()]).toEqual([prod.id]);

    // Progress reached 100% and the preview is ready.
    const deploy = store.get("deploy-1")!;
    expect((deploy.dataModel as { build: { percent: number } }).build.percent).toBe(100);
    expect(deploy.components.preview?.status).toBe("ready");

    // Route c replays the same surfaces: a no-op for the store.
    const before = store.getVersion();
    const { surfaces } = await api.components.listSurfaces({ sessionId: conv.id });
    for (const s of surfaces) store.apply(a2uiFromSurface(s));
    expect(store.getVersion()).toBe(before);

    // History replays the same conversation.
    const history = await chatClient().history(conv);
    expect(history.awaitingReply?.kind).toBe("tool_approval");
    const replay = conversationReducer(initialConversation, { type: "history", history });
    expect(Object.keys(replay.tools).length).toBe(Object.keys(state.tools).length);

    const mine = await api.approvals.listInbox({ view: InboxView.MY_REQUESTS });
    expect(mine.approvals.map((a) => a.id)).toContain(prod.id);
  });
});
