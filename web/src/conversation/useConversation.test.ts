import { afterEach, describe, expect, it } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { setApiTransport } from "@/api/transport";
import { api } from "@/api/clients";
import { ApprovalState } from "@/gen/workbench/v1/approvals_pb";
import { createMockTransport } from "@/mock/transport";
import { resetMockWorld } from "@/mock/world";
import { useConversation } from "./useConversation";

afterEach(() => setApiTransport(undefined));

describe("useConversation", () => {
  it("follows a paused task when its approval is decided elsewhere", async () => {
    setApiTransport(createMockTransport(resetMockWorld(0)));
    const { session } = await api.sessions.createSession({ agent: { namespace: "workbench", name: "builder" }, requestId: "r1" });
    const { result } = renderHook(() => useConversation(session!.id, "workbench/builder"));
    await waitFor(() => expect(result.current.loading).toBe(false));

    await act(() => result.current.send("An expense tracker"));
    await waitFor(() => expect(result.current.state.awaiting?.kind).toBe("ask_user"));
    const ask = result.current.state.awaiting as Extract<typeof result.current.state.awaiting, { kind: "ask_user" }>;
    await act(() => result.current.answer(ask!, [["Just my team"]]));
    await waitFor(() => expect(result.current.state.awaiting?.kind).toBe("tool_approval"));

    // Decided without this page (as a remembered answer or another tab
    // would): no card calls onApprovalSettled.
    const pending = (await api.approvals.listSessionApprovals({ sessionId: session!.id })).approvals.find(
      (a) => a.state === ApprovalState.PENDING,
    )!;
    await api.approvals.decide({ approvalId: pending.id, approved: true });

    // The conversation picks the task back up: the decided call stops
    // "waiting" and finishes, and the turn moves on to its next pause.
    const callId = pending.calls[0]!.callId;
    expect(result.current.state.tools[callId]?.status).toBe("waiting");
    await waitFor(() => expect(result.current.state.tools[callId]?.status).toBe("done"), { timeout: 12000, interval: 250 });
  }, 20000);
});
