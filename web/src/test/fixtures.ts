import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { ActionKind, ApprovalSchema, ApprovalState, type Approval } from "@/gen/workbench/v1/approvals_pb";
import { PersonSchema } from "@/gen/workbench/v1/common_pb";

export const ME = create(PersonSchema, { id: "u-me", displayName: "Me Person", email: "me@example.com" });
export const OTHER = create(PersonSchema, { id: "u-other", displayName: "Olive Other", email: "olive@example.com" });

export function approval(init: Omit<MessageInitShape<typeof ApprovalSchema>, "$typeName"> = {}): Approval {
  return create(ApprovalSchema, {
    id: "apr_1",
    state: ApprovalState.PENDING,
    sessionId: "s1",
    taskId: "t1",
    requester: ME,
    kind: ActionKind.ORDINARY,
    hint: "Create a bucket?",
    calls: [{ id: "tool-1", callId: "c1", name: "create_bucket", args: { name: "b" }, kind: ActionKind.ORDINARY }],
    callerMayDecide: true,
    requesterMayDecide: true,
    ...init,
  });
}
