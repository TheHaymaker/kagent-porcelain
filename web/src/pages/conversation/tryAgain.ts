/**
 * "Try again from here": fork the conversation at the checkpoint whose head is
 * the chosen turn (CheckpointService), creating one first when the turn is the
 * conversation's latest, then open the fork.
 */
import { api } from "@/api/clients";
import { CheckpointState } from "@/gen/kagent/api/v1/checkpoints_pb";
import { newRequestId } from "@/lib/format";

const POLL_MS = 1000;
const POLL_LIMIT = 60;

export async function forkFromTurn(sessionId: string, taskId: string): Promise<string> {
  const { checkpoints } = await api.checkpoints.listCheckpoints({ sessionId });
  let checkpoint = checkpoints.find((c) => c.headTaskId === taskId && c.state === CheckpointState.READY);
  if (!checkpoint) {
    const created = await api.checkpoints.createCheckpoint({ sessionId, requestId: newRequestId(), expectedHeadTaskId: taskId });
    checkpoint = created.checkpoint;
    for (let i = 0; checkpoint && checkpoint.state === CheckpointState.CREATING && i < POLL_LIMIT; i += 1) {
      await new Promise((r) => setTimeout(r, POLL_MS));
      checkpoint = (await api.checkpoints.getCheckpoint({ checkpointId: checkpoint.id })).checkpoint;
    }
  }
  if (!checkpoint || checkpoint.state !== CheckpointState.READY) {
    throw new Error("We couldn't save this point of the conversation. Please try again in a moment.");
  }
  const fork = await api.checkpoints.forkSession({ checkpointId: checkpoint.id, requestId: newRequestId() });
  if (!fork.session) throw new Error("The new conversation couldn't be created.");
  return fork.session.id;
}
