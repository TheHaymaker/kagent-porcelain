/**
 * Mock mode: every service the app calls, served in-process by
 * `createRouterTransport`. It is substituted *under* the app's transport
 * (`setApiTransport`), so mock and live share every line above the wire —
 * the generated clients, the A2A chat client, the interceptors.
 */
import { create, type JsonObject } from "@bufbuild/protobuf";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, createRouterTransport, type Transport } from "@connectrpc/connect";
import { A2AService, MessageSchema, Role, StreamResponseSchema, TaskState } from "@/gen/a2a_pb";
import { SessionService } from "@/gen/kagent/api/v1/sessions_pb";
import { CheckpointService, CheckpointState } from "@/gen/kagent/api/v1/checkpoints_pb";
import { ScheduledRunService, ScheduledRunExecutionState } from "@/gen/kagent/api/v1/scheduled_runs_pb";
import { IdentityService } from "@/gen/workbench/v1/identity_pb";
import { ApprovalService, ApprovalState, InboxView, RememberScope, ActionKind } from "@/gen/workbench/v1/approvals_pb";
import { AppService, AppStatus, EnvironmentName } from "@/gen/workbench/v1/apps_pb";
import { AuditService } from "@/gen/workbench/v1/audit_pb";
import { NotificationService, NotificationKind } from "@/gen/workbench/v1/notifications_pb";
import { UsageGroupBy, UsageService } from "@/gen/workbench/v1/usage_pb";
import { ComponentService } from "@/gen/workbench/v1/components_pb";
import { HITL_EXTENSION_URI, readAskUserResponse } from "@/api/chat/hitl";
import { A2A_METADATA } from "@/api/chat/a2aMetadata";
import { MockTask, nextPosition, uuid } from "./runtime";
import { buildScript, followUpScript, ScriptCtx, supersedeFor } from "./script";
import { makeApproval, ME, mockWorld, now, ROLES_ME, type World } from "./world";
import catalog from "../../../catalogue/catalog.json";

function notFound(what: string): ConnectError {
  return new ConnectError(`${what} not found`, Code.NotFound);
}

function textOf(message: { parts: { content: { case?: string; value?: unknown } }[] } | undefined): string {
  return (message?.parts ?? [])
    .map((p) => (p.content.case === "text" ? String(p.content.value) : ""))
    .join("");
}

export function createMockTransport(world: World = mockWorld()): Transport {
  return createRouterTransport(({ service }) => {
    service(A2AService, {
      async *sendStreamingMessage(request, context) {
        const message = request.message;
        if (!message) throw new ConnectError("message is required", Code.InvalidArgument);
        const sessionId = message.contextId;
        if (!world.sessions.has(sessionId)) throw notFound("conversation");

        // An answer to a parked question resumes the same task.
        if (message.taskId) {
          const task = world.tasks.get(message.taskId);
          if (!task) throw notFound("task");
          const answer = readAskUserResponse(message.metadata, message.extensions);
          if (task.state !== TaskState.INPUT_REQUIRED || !answer || answer.requestId !== task.pendingAskId) {
            throw new ConnectError("That task is not waiting for this answer.", Code.FailedPrecondition);
          }
          task.addHistory(create(MessageSchema, { ...message, metadata: { ...(message.metadata ?? {}), [A2A_METADATA.timelinePosition]: nextPosition() } }));
          const from = task.frames.length;
          task.setStatus(TaskState.WORKING);
          task.open(`ask:${answer.requestId}`, answer.answers);
          yield* task.follow(from, context.signal);
          return;
        }

        const ids = world.sessionTasks.get(sessionId) ?? [];
        const parked = ids.map((id) => world.tasks.get(id)).find((t) => t && t.state === TaskState.INPUT_REQUIRED);
        if (parked) {
          throw new ConnectError(
            parked.pendingApprovalId
              ? "The agent is waiting for an approval before it can continue."
              : "The agent is waiting for a reply to its last question.",
            Code.FailedPrecondition,
          );
        }
        const first = ids.length === 0;
        const task = world.newTask(sessionId, request.tenant);
        task.addHistory(
          create(MessageSchema, {
            ...message,
            taskId: task.id,
            metadata: { ...(message.metadata ?? {}), [A2A_METADATA.timelinePosition]: nextPosition() },
          }),
        );
        const session = world.sessions.get(sessionId)!;
        const prompt = textOf(message);
        if (!session.name) session.name = prompt.length > 48 ? `${prompt.slice(0, 45)}…` : prompt;
        world.recordAudit("conversation.message", `Asked: ${prompt.slice(0, 80)}`, { sessionId, taskId: task.id });
        task.setStatus(TaskState.SUBMITTED);
        const ctx = new ScriptCtx(world, task);
        void (first ? buildScript(ctx, prompt) : followUpScript(ctx, prompt)).catch(() => task.setStatus(TaskState.FAILED));
        yield* task.follow(0, context.signal);
      },
      async *subscribeToTask(request, context) {
        const task = world.tasks.get(request.id);
        if (!task) throw notFound("task");
        const from = task.frames.length;
        yield create(StreamResponseSchema, { payload: { case: "task", value: task.snapshot() } });
        if (task.state === TaskState.INPUT_REQUIRED || task.state === TaskState.COMPLETED || task.state === TaskState.FAILED || task.state === TaskState.CANCELED) return;
        yield* task.follow(from, context.signal);
      },
      async listTasks(request) {
        const ids = world.sessionTasks.get(request.contextId) ?? [];
        const tasks = ids.map((id) => world.tasks.get(id)!).filter(Boolean).map((t) => t.snapshot());
        return { tasks, nextPageToken: "", pageSize: tasks.length, totalSize: tasks.length };
      },
      async getTask(request) {
        const task = world.tasks.get(request.id);
        if (!task) throw notFound("task");
        return task.snapshot();
      },
      async cancelTask(request) {
        const task = world.tasks.get(request.id);
        if (!task) throw notFound("task");
        supersedeFor(world, task.id);
        task.setStatus(TaskState.CANCELED);
        return task.snapshot();
      },
    });

    service(SessionService, {
      async createSession(request) {
        const session = world.addSession(request.name);
        world.recordAudit("conversation.started", `Started a conversation`, { sessionId: session.id });
        return { session };
      },
      async getSession(request) {
        const session = world.sessions.get(request.sessionId);
        if (!session) throw notFound("conversation");
        return { session };
      },
      async listSessions(request) {
        const sessions = [...world.sessions.values()]
          .filter((s) => request.allCreators || s.creator === ME.id)
          .sort((a, b) => Number(b.updatedAt?.seconds ?? 0n) - Number(a.updatedAt?.seconds ?? 0n));
        const limit = request.page?.limit || 50;
        return { sessions: sessions.slice(0, limit), page: { nextPageToken: "" } };
      },
      async updateSessionName(request) {
        const session = world.sessions.get(request.sessionId);
        if (!session) throw notFound("conversation");
        session.name = request.name;
        return { session };
      },
      async createSessionShare(request) {
        const ttl = Number(request.ttl?.seconds ?? 7n * 86400n);
        const share = {
          $typeName: "kagent.api.v1.SessionShare" as const,
          id: uuid(),
          sessionId: request.sessionId,
          createdAt: now(),
          expiresAt: timestampFromDate(new Date(Date.now() + ttl * 1000)),
          token: uuid().replace(/-/g, ""),
        };
        world.shares.push(share);
        world.recordAudit("conversation.shared", "Shared a conversation link", { sessionId: request.sessionId });
        const { token, ...rest } = share;
        return { share: rest, token };
      },
      async listSessionShares(request) {
        return { shares: world.shares.filter((s) => s.sessionId === request.sessionId).map(({ token: _t, ...rest }) => rest) };
      },
      async revokeSessionShare(request) {
        world.shares = world.shares.filter((s) => s.id !== request.shareId);
        return {};
      },
    });

    service(CheckpointService, {
      async listCheckpoints(request) {
        return { checkpoints: world.checkpoints.filter((c) => c.sessionId === request.sessionId) };
      },
      async getCheckpoint(request) {
        const checkpoint = world.checkpoints.find((c) => c.id === request.checkpointId);
        if (!checkpoint) throw notFound("checkpoint");
        return { checkpoint };
      },
      async createCheckpoint(request) {
        const ids = world.sessionTasks.get(request.sessionId) ?? [];
        if (ids.at(-1) !== request.expectedHeadTaskId) throw new ConnectError("The conversation has moved on.", Code.FailedPrecondition);
        world.addCheckpoint(request.sessionId, request.expectedHeadTaskId);
        return { checkpoint: world.checkpoints.at(-1) };
      },
      async forkSession(request) {
        const checkpoint = world.checkpoints.find((c) => c.id === request.checkpointId);
        if (!checkpoint || checkpoint.state !== CheckpointState.READY) throw notFound("checkpoint");
        const source = world.sessions.get(checkpoint.sessionId);
        const fork = world.addSession(`${source?.name || "Conversation"} (try again)`);
        const ids = world.sessionTasks.get(checkpoint.sessionId) ?? [];
        const cut = ids.indexOf(checkpoint.headTaskId);
        for (const id of ids.slice(0, cut + 1)) {
          const original = world.tasks.get(id);
          if (!original) continue;
          const copy = new MockTask(uuid(), fork.id, original.agent);
          for (const m of original.history) copy.addHistory(create(MessageSchema, { ...m, contextId: fork.id, taskId: copy.id }));
          for (const [aid, artifact] of original.artifacts) copy.artifacts.set(aid, artifact);
          copy.status = original.status;
          world.tasks.set(copy.id, copy);
          world.sessionTasks.get(fork.id)!.push(copy.id);
          world.addCheckpoint(fork.id, copy.id);
          for (const surface of world.surfaces.get(checkpoint.sessionId)?.values() ?? []) {
            if (surface.taskId === id) world.storeSurface(fork.id, copy.id, surface.messages);
          }
        }
        world.recordAudit("conversation.forked", `Tried again from an earlier point of “${source?.name ?? ""}”`, { sessionId: fork.id });
        return { session: fork };
      },
      async deleteCheckpoint() {
        return {};
      },
      async updateCheckpointName(request) {
        const checkpoint = world.checkpoints.find((c) => c.id === request.checkpointId);
        if (!checkpoint) throw notFound("checkpoint");
        checkpoint.name = request.name;
        return { checkpoint };
      },
    });

    service(ScheduledRunService, {
      async listScheduledRuns() {
        return { scheduledRuns: world.schedules.filter((s) => !s.deletedAt) };
      },
      async getScheduledRun(request) {
        const run = world.schedules.find((s) => s.id === request.scheduledRunId);
        if (!run) throw notFound("schedule");
        return { scheduledRun: run };
      },
      async createScheduledRun(request) {
        if (!request.config?.prompt.trim()) throw new ConnectError("A schedule needs instructions.", Code.InvalidArgument);
        const run = {
          $typeName: "kagent.api.v1.ScheduledRun" as const,
          id: uuid(),
          creator: ME.id,
          agent: request.agent,
          config: request.config,
          etag: uuid(),
          createdAt: now(),
          updatedAt: now(),
          nextExecutionTime: timestampFromDate(new Date(Date.now() + 86400_000)),
        };
        world.schedules.unshift(run);
        world.recordAudit("schedule.created", `Scheduled “${request.config.name}”`);
        return { scheduledRun: run };
      },
      async updateScheduledRun(request) {
        const run = world.schedules.find((s) => s.id === request.scheduledRunId);
        if (!run) throw notFound("schedule");
        if (run.etag !== request.etag) throw new ConnectError("This schedule changed elsewhere. Reload and try again.", Code.Aborted);
        run.config = request.config;
        run.etag = uuid();
        run.updatedAt = now();
        return { scheduledRun: run };
      },
      async deleteScheduledRun(request) {
        const run = world.schedules.find((s) => s.id === request.scheduledRunId);
        if (!run) throw notFound("schedule");
        run.deletedAt = now();
        return { scheduledRun: run };
      },
      async triggerScheduledRun(request) {
        const run = world.schedules.find((s) => s.id === request.scheduledRunId);
        if (!run) throw notFound("schedule");
        const session = world.addSession(run.config?.name ?? "Scheduled run");
        return {
          execution: {
            id: uuid(),
            creator: ME.id,
            scheduledRunId: run.id,
            trigger: { case: "manualRequestId", value: request.requestId },
            prompt: run.config?.prompt ?? "",
            createdAt: now(),
            sessionId: session.id,
            state: ScheduledRunExecutionState.PENDING,
          },
        };
      },
      async listScheduledRunExecutions() {
        return { executions: [] };
      },
      async getScheduledRunExecution() {
        throw notFound("execution");
      },
    });

    service(IdentityService, {
      async getMe() {
        return { person: world.me, groups: world.groups, roles: ROLES_ME, surface: "web" };
      },
      async listLinkedIdentities() {
        return { identities: world.identities };
      },
      async unlinkIdentity(request) {
        world.identities = world.identities.filter((i) => !(i.issuer === request.issuer && i.subject === request.subject));
        return {};
      },
      async linkIdentity() {
        throw new ConnectError("Linking needs a sign-in from the other app.", Code.FailedPrecondition);
      },
    });

    service(ApprovalService, {
      async listInbox(request) {
        const all = [...world.approvals.values()].sort((a, b) => Number(b.createdAt?.seconds ?? 0n) - Number(a.createdAt?.seconds ?? 0n));
        const approvals = all.filter((a) => {
          switch (request.view) {
            case InboxView.TO_DECIDE:
              return a.state === ApprovalState.PENDING && a.callerMayDecide;
            case InboxView.MY_REQUESTS:
              return a.requester?.id === ME.id;
            case InboxView.DECIDED_BY_ME:
              return a.decidedBy?.id === ME.id;
            default:
              return a.callerMayDecide || a.requester?.id === ME.id;
          }
        });
        return { approvals };
      },
      async getApproval(request) {
        const approval = world.approvals.get(request.id);
        if (!approval) throw notFound("approval");
        return { approval };
      },
      async listSessionApprovals(request) {
        return { approvals: [...world.approvals.values()].filter((a) => a.sessionId === request.sessionId) };
      },
      async decide(request) {
        const approval = world.approvals.get(request.approvalId);
        if (!approval) throw notFound("approval");
        if (approval.state !== ApprovalState.PENDING) throw new ConnectError("This approval has already been decided.", Code.FailedPrecondition);
        if (!approval.callerMayDecide) throw new ConnectError("You can't decide this approval.", Code.PermissionDenied);
        if (approval.kind !== ActionKind.ORDINARY && request.remember > RememberScope.ONCE) {
          throw new ConnectError("This kind of step is always asked about.", Code.InvalidArgument);
        }
        approval.state = request.approved ? ApprovalState.APPROVED : ApprovalState.REJECTED;
        approval.decidedBy = ME;
        approval.decidedAt = now();
        approval.reason = request.reason;
        approval.callerMayDecide = false;
        if (request.remember === RememberScope.CONVERSATION || request.remember === RememberScope.ALWAYS) {
          for (const call of approval.calls) {
            world.remembered.unshift({
              $typeName: "workbench.v1.RememberedAnswer",
              id: uuid(),
              scope: request.remember,
              toolName: call.name,
              agent: approval.agent,
              sessionId: request.remember === RememberScope.CONVERSATION ? approval.sessionId : "",
              approved: request.approved,
              createdAt: now(),
            });
          }
        }
        world.recordAudit("approval.decided", `${request.approved ? "Approved" : "Refused"}: ${approval.hint}`, {
          approvalId: approval.id,
          sessionId: approval.sessionId,
          taskId: approval.taskId,
        });
        // Deciding sends the HITL response into the paused task; the agent resumes.
        const task = world.tasks.get(approval.taskId);
        if (task && task.pendingApprovalId === approval.id && task.state === TaskState.INPUT_REQUIRED) {
          task.addHistory(
            create(MessageSchema, {
              messageId: uuid(),
              role: Role.USER,
              contextId: task.contextId,
              taskId: task.id,
              parts: [{ content: { case: "text", value: `${request.approved ? "Approved" : "Rejected"}: ${approval.calls[0]?.name ?? "tool"}` } }],
              extensions: [HITL_EXTENSION_URI],
              metadata: {
                [HITL_EXTENSION_URI]: {
                  type: "tool_approval_response",
                  approvals: approval.calls.map((c) => ({ id: c.id, approved: request.approved, ...(request.reason && !request.approved ? { rejection_reason: request.reason } : {}) })),
                },
                [A2A_METADATA.timelinePosition]: nextPosition(),
              },
            }),
          );
          task.setStatus(TaskState.WORKING);
          task.open(`approval:${approval.id}`, request.approved);
        }
        return { approval };
      },
      async reassign(request) {
        const approval = world.approvals.get(request.approvalId);
        if (!approval) throw notFound("approval");
        approval.approvers = [{ $typeName: "workbench.v1.Person", id: request.assignee, email: request.assignee, displayName: request.assignee }];
        return { approval };
      },
      async listRememberedAnswers() {
        return { answers: world.remembered };
      },
      async forgetRememberedAnswer(request) {
        world.remembered = world.remembered.filter((a) => a.id !== request.id);
        return {};
      },
      async getPolicy() {
        return {
          kinds: [
            { kind: ActionKind.PRODUCTION_DEPLOY, description: "Putting an app into production", approverGroups: ["platform-approvers"], requesterMayDecide: false },
            { kind: ActionKind.IAM_CHANGE, description: "Changing who can access something", approverGroups: ["security"], requesterMayDecide: false },
            { kind: ActionKind.DATA_DELETION, description: "Deleting data", approverGroups: ["data-owners"], requesterMayDecide: false },
            { kind: ActionKind.NEW_SPEND, description: "Starting something that costs money every month", approverGroups: ["finance-leads"], requesterMayDecide: false },
          ],
        };
      },
    });

    service(AppService, {
      async listApps(request) {
        let apps = [...world.apps.values()];
        if (request.mine) apps = apps.filter((a) => a.owner?.id === ME.id);
        if (request.status) apps = apps.filter((a) => a.status === request.status);
        if (request.query) {
          const q = request.query.toLowerCase();
          apps = apps.filter((a) => a.name.toLowerCase().includes(q) || a.description.toLowerCase().includes(q));
        }
        apps.sort((a, b) => Number(b.updatedAt?.seconds ?? 0n) - Number(a.updatedAt?.seconds ?? 0n));
        return { apps };
      },
      async getApp(request) {
        const app = world.apps.get(request.id);
        if (!app) throw notFound("app");
        return { app };
      },
      async updateApp(request) {
        const app = world.apps.get(request.id);
        if (!app) throw notFound("app");
        if (request.name) app.name = request.name;
        if (request.description) app.description = request.description;
        return { app };
      },
      async archiveApp(request) {
        const app = world.apps.get(request.id);
        if (!app) throw notFound("app");
        app.status = AppStatus.ARCHIVED;
        return { app };
      },
      async listDeployments(request) {
        return { deployments: world.deployments.filter((d) => d.appId === request.appId) };
      },
      async requestPromotion(request) {
        const app = world.apps.get(request.appId);
        if (!app) throw notFound("app");
        if (app.pendingApprovalId) return { approvalId: app.pendingApprovalId };
        const preview = app.environments.find((e) => e.name === EnvironmentName.PREVIEW);
        if (!preview) throw new ConnectError("There's no preview to promote yet.", Code.FailedPrecondition);
        const id = `apr_${uuid().slice(0, 8)}`;
        const revision = request.revision || preview.revision;
        world.approvals.set(
          id,
          makeApproval({
            id,
            sessionId: app.sessionId,
            kind: ActionKind.PRODUCTION_DEPLOY,
            hint: `Promote ${app.name} to production`,
            calls: [{ $typeName: "workbench.v1.ToolCall", id: "tool-1", callId: uuid(), name: "promote_to_production", args: { app: app.name, revision, note: request.note } as JsonObject, kind: ActionKind.PRODUCTION_DEPLOY }],
            approverGroups: ["platform-approvers"],
            requesterMayDecide: false,
            callerMayDecide: false,
            appId: app.id,
          }),
        );
        app.pendingApprovalId = id;
        world.recordAudit("approval.requested", `Asked to put ${app.name} into production`, { approvalId: id, appId: app.id });
        return { approvalId: id };
      },
    });

    service(AuditService, {
      async listAuditEvents(request) {
        const since = request.since ? timestampDate(request.since).getTime() : -Infinity;
        const until = request.until ? timestampDate(request.until).getTime() : Infinity;
        const events = world.audit.filter((e) => {
          const t = e.time ? timestampDate(e.time).getTime() : 0;
          return (
            (!request.sessionId || e.sessionId === request.sessionId) &&
            (!request.appId || e.appId === request.appId) &&
            (!request.actorId || e.actor?.id === request.actorId) &&
            (!request.typePrefix || e.type.startsWith(request.typePrefix)) &&
            t >= since &&
            t <= until
          );
        });
        return { events };
      },
    });

    service(NotificationService, {
      async listNotifications(request) {
        const list = request.unreadOnly ? world.notifications.filter((n) => !n.read) : world.notifications;
        return { notifications: list.slice(0, request.page?.pageSize || 30), unreadCount: world.notifications.filter((n) => !n.read).length };
      },
      async markRead(request) {
        for (const n of world.notifications) if (request.all || request.ids.includes(n.id)) n.read = true;
        return {};
      },
      async getPreferences() {
        return { preferences: world.preferences };
      },
      async setPreferences(request) {
        world.preferences = request.preferences;
        return { preferences: world.preferences };
      },
      async registerDevice() {
        return {};
      },
    });

    service(UsageService, {
      async getUsageSummary(request) {
        if (request.groupBy === UsageGroupBy.DAY) {
          const lines = Array.from({ length: 14 }, (_, i) => {
            const d = new Date(Date.now() - (13 - i) * 86400_000);
            const cents = [42, 0, 0, 118, 260, 95, 31, 12, 0, 0, 388, 512, 174, 236][i] ?? 0;
            return { key: d.toISOString().slice(0, 10), inputTokens: BigInt(cents * 900), outputTokens: BigInt(cents * 140), costCents: BigInt(cents) };
          });
          return { lines, totalCostCents: lines.reduce((s, l) => s + l.costCents, 0n) };
        }
        if (request.groupBy === UsageGroupBy.MODEL) {
          const lines = [
            { key: "claude-sonnet", inputTokens: 1_540_000n, outputTokens: 210_000n, costCents: 1720n },
            { key: "claude-haiku", inputTokens: 980_000n, outputTokens: 88_000n, costCents: 168n },
            { key: "Cloud Run (apps)", inputTokens: 0n, outputTokens: 0n, costCents: 424n },
          ];
          return { lines, totalCostCents: 2312n };
        }
        const lines = [...world.apps.values()]
          .filter((a) => a.owner?.id === ME.id)
          .map((a) => ({ key: a.name, inputTokens: 0n, outputTokens: 0n, costCents: a.costCentsMtd }));
        return { lines, totalCostCents: lines.reduce((s, l) => s + l.costCents, 0n) };
      },
      async listLimits() {
        return { limits: world.limits };
      },
      async setLimit() {
        throw new ConnectError("Only platform engineers can change limits.", Code.PermissionDenied);
      },
    });

    service(ComponentService, {
      async listSurfaces(request) {
        const surfaces = [...(world.surfaces.get(request.sessionId)?.values() ?? [])].map((s) => ({
          surfaceId: s.surfaceId,
          sessionId: s.sessionId,
          taskId: s.taskId,
          catalogId: "https://workbench.dev/catalogs/workbench/v1/catalog.json",
          messages: s.messages,
          createdAt: s.createdAt,
          updatedAt: s.updatedAt,
        }));
        return { surfaces };
      },
      async getCatalog() {
        return { catalogId: catalog.catalogId, catalog: catalog as unknown as JsonObject };
      },
      async sendAction(request) {
        world.recordAudit("component.action", `Pressed “${request.name}”`, {});
        world.notify(NotificationKind.TASK_FINISHED, "Action sent", `“${request.name}” was sent to the agent.`, "/");
        return {};
      },
    });
  });
}
