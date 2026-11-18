import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { ActionKind, ApprovalService, ApprovalState, RememberScope, type DecideRequest } from "@/gen/workbench/v1/approvals_pb";
import { setApiTransport } from "@/api/transport";
import { approval, ME, OTHER } from "@/test/fixtures";
import { renderWithRouter, useServices } from "@/test/render";
import { ApprovalPanel } from "./ApprovalPanel";
import { canDecide, rememberOptions, waitingOn, whyNotDecider } from "./logic";

afterEach(() => setApiTransport(undefined));

describe("approval permission logic", () => {
  it("offers buttons only when pending and the server says the caller may decide", () => {
    expect(canDecide(approval())).toBe(true);
    expect(canDecide(approval({ callerMayDecide: false }))).toBe(false);
    expect(canDecide(approval({ state: ApprovalState.APPROVED }))).toBe(false);
    // Even a requester the server lets decide gets buttons only from caller_may_decide.
    expect(canDecide(approval({ requesterMayDecide: true, callerMayDecide: false }))).toBe(false);
  });

  it("offers remember scopes only for ordinary calls", () => {
    expect(rememberOptions(approval())).toEqual([RememberScope.ONCE, RememberScope.CONVERSATION, RememberScope.ALWAYS]);
    for (const kind of [ActionKind.PRODUCTION_DEPLOY, ActionKind.IAM_CHANGE, ActionKind.DATA_DELETION, ActionKind.NEW_SPEND]) {
      expect(rememberOptions(approval({ kind }))).toEqual([RememberScope.ONCE]);
    }
  });

  it("names who it is waiting on", () => {
    expect(waitingOn(approval({ approverGroups: ["platform-approvers"] }))).toBe("Platform approvers");
    expect(waitingOn(approval({ approvers: [OTHER], approverGroups: ["sec"] }))).toBe("Olive Other or Sec");
    expect(waitingOn(approval())).toBe("an approver");
  });

  it("explains why a requester cannot approve their own production deploy", () => {
    const prod = approval({ kind: ActionKind.PRODUCTION_DEPLOY, requester: ME, requesterMayDecide: false, callerMayDecide: false });
    expect(whyNotDecider(prod, ME.id)).toMatch(/someone other than you/);
    expect(whyNotDecider(prod, OTHER.id)).toBeUndefined();
    expect(whyNotDecider(approval(), ME.id)).toBeUndefined();
  });
});

describe("ApprovalPanel", () => {
  it("decides through ApprovalService.Decide with reason and remember scope", async () => {
    const decide = vi.fn(async (req: DecideRequest) => ({
      approval: approval({ state: req.approved ? ApprovalState.APPROVED : ApprovalState.REJECTED, decidedBy: ME, reason: req.reason, callerMayDecide: false }),
    }));
    useServices((r) => r.service(ApprovalService, { getApproval: async () => ({ approval: approval() }), decide }));
    const onSettled = vi.fn();
    renderWithRouter(<ApprovalPanel approvalId="apr_1" onSettled={onSettled} />);
    fireEvent.change(await screen.findByLabelText(/Reason/), { target: { value: "fine" } });
    fireEvent.click(screen.getByLabelText("For the rest of this conversation"));
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(decide).toHaveBeenCalled());
    expect(decide.mock.calls[0]![0]).toMatchObject({ approvalId: "apr_1", approved: true, reason: "fine", remember: RememberScope.CONVERSATION });
    expect(await screen.findByText(/Approved by Me Person/)).toBeInTheDocument();
    await waitFor(() => expect(onSettled).toHaveBeenCalledTimes(1));
  });

  it("shows only 'once' (no choice) for an always-ask kind, and refuses with a reason", async () => {
    const decide = vi.fn(async () => ({ approval: approval({ state: ApprovalState.REJECTED }) }));
    useServices((r) =>
      r.service(ApprovalService, { getApproval: async () => ({ approval: approval({ kind: ActionKind.DATA_DELETION }) }), decide }),
    );
    renderWithRouter(<ApprovalPanel approvalId="apr_1" />);
    await screen.findByRole("button", { name: "Refuse" });
    expect(screen.queryByText("Remember this answer")).toBeNull();
    expect(screen.getByText("Data deletion")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Refuse" }));
    await waitFor(() => expect(decide).toHaveBeenCalled());
    expect((decide.mock.calls[0] as unknown as [DecideRequest])[0]).toMatchObject({ approved: false, remember: RememberScope.ONCE });
  });

  it("shows who it is waiting on instead of buttons when the caller may not decide", async () => {
    useServices((r) =>
      r.service(ApprovalService, {
        getApproval: async () => ({
          approval: approval({ kind: ActionKind.PRODUCTION_DEPLOY, callerMayDecide: false, requesterMayDecide: false, approverGroups: ["platform-approvers"] }),
        }),
      }),
    );
    renderWithRouter(<ApprovalPanel approvalId="apr_1" />);
    expect(await screen.findByText(/Waiting on Platform approvers/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Refuse" })).toBeNull();
    expect(screen.getByText("Production deploy")).toBeInTheDocument();
  });
});
