/**
 * The fixture world mock mode serves: people, conversations, apps, approvals,
 * audit, notifications, usage. Mutable, so actions in the app have effects.
 */
import { create, type JsonObject } from "@bufbuild/protobuf";
import { timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Role, TaskState, MessageSchema } from "@/gen/a2a_pb";
import { RuntimeState } from "@/gen/kagent/api/v1/runtime_pb";
import { CheckpointState, type Checkpoint } from "@/gen/kagent/api/v1/checkpoints_pb";
import type { Session, SessionShare } from "@/gen/kagent/api/v1/sessions_pb";
import type { ScheduledRun } from "@/gen/kagent/api/v1/scheduled_runs_pb";
import { ActionKind, ApprovalState, RememberScope, type Approval, type RememberedAnswer } from "@/gen/workbench/v1/approvals_pb";
import { AppStatus, EnvironmentName, type App, type Deployment } from "@/gen/workbench/v1/apps_pb";
import type { AuditEvent } from "@/gen/workbench/v1/audit_pb";
import type { Person } from "@/gen/workbench/v1/common_pb";
import { Channel, NotificationKind, type Notification, type Preference } from "@/gen/workbench/v1/notifications_pb";
import type { LinkedIdentity } from "@/gen/workbench/v1/identity_pb";
import { Role as WbRole } from "@/gen/workbench/v1/identity_pb";
import type { Limit } from "@/gen/workbench/v1/usage_pb";
import { LimitSubject } from "@/gen/workbench/v1/usage_pb";
import { A2A_METADATA } from "@/api/chat/a2aMetadata";
import { a2uiPart, agentMessage, MockTask, nextPosition, textPart, uuid } from "./runtime";

export const AGENT = "workbench/builder";

const person = (id: string, displayName: string, email: string): Person =>
  ({ $typeName: "workbench.v1.Person", id, displayName, email });

export const ME = person("u-ava", "Ava Chen", "ava.chen@example.com");
export const SAM = person("u-sam", "Sam Ortiz", "sam.ortiz@example.com");
export const DANA = person("u-dana", "Dana Lee", "dana.lee@example.com");

const ago = (minutes: number): Timestamp => timestampFromDate(new Date(Date.now() - minutes * 60_000));
export const now = (): Timestamp => timestampFromDate(new Date());

export interface StoredSurface {
  surfaceId: string;
  sessionId: string;
  taskId: string;
  messages: JsonObject[];
  createdAt: Timestamp;
  updatedAt: Timestamp;
}

export class World {
  /** Multiplies every scripted delay; 0 in unit tests. */
  pace = 1;
  me = ME;
  groups = ["finance-team", "finance-leads"];
  sessions = new Map<string, Session>();
  tasks = new Map<string, MockTask>();
  /** Task ids per session, in order. */
  sessionTasks = new Map<string, string[]>();
  surfaces = new Map<string, Map<string, StoredSurface>>();
  shares: (SessionShare & { token: string })[] = [];
  checkpoints: Checkpoint[] = [];
  schedules: ScheduledRun[] = [];
  approvals = new Map<string, Approval>();
  remembered: RememberedAnswer[] = [];
  apps = new Map<string, App>();
  deployments: Deployment[] = [];
  audit: AuditEvent[] = [];
  notifications: Notification[] = [];
  preferences: Preference[] = [];
  identities: LinkedIdentity[] = [];
  limits: Limit[] = [];

  constructor() {
    seed(this);
  }

  addSession(name: string, minutesAgo = 0, creator = ME.id): Session {
    const id = uuid();
    const session: Session = {
      $typeName: "kagent.api.v1.Session",
      id,
      contextId: id,
      creator,
      name,
      agent: { $typeName: "kagent.api.v1.AgentProvenance", ref: { $typeName: "kagent.api.v1.ResourceReference", namespace: "workbench", name: "builder" }, uid: "agent-uid" },
      preparedRevision: "",
      a2aAuthority: "",
      state: RuntimeState.READY,
      operation: 0,
      createdAt: ago(minutesAgo),
      updatedAt: ago(minutesAgo),
    };
    this.sessions.set(id, session);
    this.sessionTasks.set(id, []);
    return session;
  }

  touch(sessionId: string): void {
    const s = this.sessions.get(sessionId);
    if (s) s.updatedAt = now();
  }

  newTask(sessionId: string, agent = AGENT): MockTask {
    const task = new MockTask(uuid(), sessionId, agent);
    this.tasks.set(task.id, task);
    this.sessionTasks.get(sessionId)?.push(task.id);
    this.touch(sessionId);
    return task;
  }

  storeSurface(sessionId: string, taskId: string, messages: JsonObject[]): void {
    let bySession = this.surfaces.get(sessionId);
    if (!bySession) {
      bySession = new Map();
      this.surfaces.set(sessionId, bySession);
    }
    for (const m of messages) {
      const body = Object.values(m).find((v) => v && typeof v === "object" && !Array.isArray(v)) as { surfaceId?: unknown } | undefined;
      const surfaceId = typeof body?.surfaceId === "string" ? body.surfaceId : undefined;
      if (!surfaceId) continue;
      const existing = bySession.get(surfaceId);
      if (existing) {
        existing.messages.push(m);
        existing.updatedAt = now();
      } else {
        bySession.set(surfaceId, { surfaceId, sessionId, taskId, messages: [m], createdAt: now(), updatedAt: now() });
      }
    }
  }

  recordAudit(type: string, summary: string, extra: Partial<AuditEvent> = {}): void {
    this.audit.unshift({
      $typeName: "workbench.v1.AuditEvent",
      id: uuid(),
      time: now(),
      type,
      actor: ME,
      sessionId: "",
      taskId: "",
      appId: "",
      approvalId: "",
      summary,
      surface: "web",
      ...extra,
    });
  }

  notify(kind: NotificationKind, title: string, body: string, link: string, extra: Partial<Notification> = {}): void {
    this.notifications.unshift({
      $typeName: "workbench.v1.Notification",
      id: uuid(),
      kind,
      title,
      body,
      link,
      sessionId: "",
      approvalId: "",
      appId: "",
      read: false,
      createdAt: now(),
      ...extra,
    });
  }

  addCheckpoint(sessionId: string, taskId: string): void {
    this.checkpoints.push({
      $typeName: "kagent.api.v1.Checkpoint",
      id: uuid(),
      sessionId,
      headTaskId: taskId,
      historySequence: BigInt(this.checkpoints.length + 1),
      state: CheckpointState.READY,
      createdAt: now(),
      name: "",
    });
  }
}

export function makeApproval(init: Partial<Approval> & { id: string }): Approval {
  return {
    $typeName: "workbench.v1.Approval",
    state: ApprovalState.PENDING,
    sessionId: "",
    taskId: "",
    agent: AGENT,
    requester: ME,
    calls: [],
    kind: ActionKind.ORDINARY,
    hint: "",
    approverGroups: [],
    approvers: [],
    requesterMayDecide: true,
    reason: "",
    callerMayDecide: false,
    appId: "",
    createdAt: now(),
    ...init,
  };
}

function env(name: EnvironmentName, url: string, revision: string, minutesAgo: number) {
  return { $typeName: "workbench.v1.Environment" as const, name, url, revision, image: `europe-docker.pkg.dev/workbench/apps/${revision}`, updatedAt: ago(minutesAgo) };
}

export function makeApp(init: Partial<App> & { id: string; name: string }): App {
  return {
    $typeName: "workbench.v1.App",
    description: "",
    owner: ME,
    status: AppStatus.PREVIEW,
    scaffold: "python-cloudrun",
    repositoryUrl: `https://github.example.com/workbench-apps/${init.name}`,
    sessionId: "",
    agent: AGENT,
    environments: [],
    costCentsMtd: 0n,
    createdAt: ago(60 * 24 * 10),
    updatedAt: ago(60),
    pendingApprovalId: "",
    ...init,
  };
}

/** A finished turn: the user's words, the agent's reply, and optionally a surface. */
function seedTurn(world: World, sessionId: string, user: string, reply: string, a2ui?: JsonObject[]): MockTask {
  const task = world.newTask(sessionId);
  task.addHistory(
    create(MessageSchema, {
      messageId: uuid(),
      role: Role.USER,
      parts: [textPart(user)],
      contextId: sessionId,
      taskId: task.id,
      metadata: { [A2A_METADATA.timelinePosition]: nextPosition(Date.now() - 3_600_000) },
    }),
  );
  task.addHistory(agentMessage(task.id, sessionId, [textPart(reply)]));
  if (a2ui) {
    task.addHistory(agentMessage(task.id, sessionId, [a2uiPart(a2ui)]));
    world.storeSurface(sessionId, task.id, a2ui);
  }
  task.status = { $typeName: "lf.a2a.v1.TaskStatus", state: TaskState.COMPLETED, timestamp: ago(60) };
  world.addCheckpoint(sessionId, task.id);
  return task;
}

const CATALOG = "https://workbench.dev/catalogs/workbench/v1/catalog.json";

function seed(world: World): void {
  // Apps
  world.apps.set(
    "app_lunch",
    makeApp({
      id: "app_lunch",
      name: "lunch-poll",
      description: "Lets the team vote on Friday lunch and sends the order to the office manager.",
      status: AppStatus.LIVE,
      costCentsMtd: 1840n,
      environments: [
        env(EnvironmentName.PREVIEW, "https://lunch-poll-preview.run.example.com", "lunch-poll-00012", 60 * 30),
        env(EnvironmentName.PRODUCTION, "https://lunch-poll.apps.example.com", "lunch-poll-00011", 60 * 48),
      ],
    }),
  );
  world.apps.set(
    "app_invoice",
    makeApp({
      id: "app_invoice",
      name: "invoice-reminders",
      description: "Emails customers a friendly reminder when an invoice is 7 days overdue.",
      status: AppStatus.PREVIEW,
      costCentsMtd: 512n,
      environments: [env(EnvironmentName.PREVIEW, "https://invoice-reminders-preview.run.example.com", "invoice-reminders-00004", 60 * 5)],
    }),
  );
  world.apps.set(
    "app_inventory",
    makeApp({
      id: "app_inventory",
      name: "inventory-dashboard",
      description: "Stock levels across the three warehouses, refreshed hourly.",
      owner: DANA,
      status: AppStatus.BUILDING,
      costCentsMtd: 233n,
    }),
  );

  const deployment = (appId: string, environment: EnvironmentName, revision: string, url: string, minutesAgo: number, by: Person, approvalId = ""): Deployment => ({
    $typeName: "workbench.v1.Deployment",
    id: uuid(),
    appId,
    environment,
    revision,
    image: `europe-docker.pkg.dev/workbench/apps/${revision}`,
    url,
    deployedBy: by,
    approvalId,
    createdAt: ago(minutesAgo),
  });
  world.deployments.push(
    deployment("app_lunch", EnvironmentName.PREVIEW, "lunch-poll-00012", "https://lunch-poll-preview.run.example.com", 60 * 30, ME),
    deployment("app_lunch", EnvironmentName.PRODUCTION, "lunch-poll-00011", "https://lunch-poll.apps.example.com", 60 * 48, SAM, "apr_lunch_prod"),
    deployment("app_lunch", EnvironmentName.PREVIEW, "lunch-poll-00011", "https://lunch-poll-preview.run.example.com", 60 * 50, ME),
    deployment("app_invoice", EnvironmentName.PREVIEW, "invoice-reminders-00004", "https://invoice-reminders-preview.run.example.com", 60 * 5, ME),
  );

  // Conversations
  const lunch = world.addSession("Team lunch poll", 60 * 49);
  const lunchTask = seedTurn(
    world,
    lunch.id,
    "Can you make a little app where the team votes on Friday lunch?",
    "Done! **lunch-poll** is live. Everyone on the finance team can vote until Thursday 3pm, and the winning order is emailed to the office manager.",
    [
      { version: "v0.9.1", createSurface: { surfaceId: "lunch-done", catalogId: CATALOG } },
      {
        version: "v0.9.1",
        updateComponents: {
          surfaceId: "lunch-done",
          components: [
            { id: "root", component: "Column", children: ["h", "app"] },
            { id: "h", component: "Text", text: "Your app is live", variant: "h2" },
            { id: "app", component: "AppCard", appId: "app_lunch", name: "lunch-poll" },
          ],
        },
      },
    ],
  );
  world.apps.get("app_lunch")!.sessionId = lunch.id;
  seedTurn(world, lunch.id, "Could it also show last week's winner?", "Sure — the poll page now shows last week's winner at the top. The change is in preview; production is unchanged until it's approved.");

  const invoice = world.addSession("Invoice reminder emails", 60 * 6);
  seedTurn(
    world,
    invoice.id,
    "I need something that reminds customers about overdue invoices.",
    "I've built **invoice-reminders** and deployed a preview. It checks the billing export every morning and emails a reminder after 7 days.",
    [
      { version: "v0.9.1", createSurface: { surfaceId: "inv-cost", catalogId: CATALOG } },
      {
        version: "v0.9.1",
        updateComponents: {
          surfaceId: "inv-cost",
          components: [
            { id: "root", component: "Column", children: ["app", "cost"] },
            { id: "app", component: "AppCard", appId: "app_invoice", name: "invoice-reminders" },
            { id: "cost", component: "CostSummary", title: "This build", period: "this conversation", totalCents: 318, lines: [{ label: "Model", cents: 296 }, { label: "Cloud Run preview", cents: 22 }] },
          ],
        },
      },
    ],
  );
  world.apps.get("app_invoice")!.sessionId = invoice.id;

  const danaSession = world.addSession("Inventory dashboard", 90, DANA.id);
  world.apps.get("app_inventory")!.sessionId = danaSession.id;

  void lunchTask;

  // Approvals
  world.approvals.set(
    "apr_dana_spend",
    makeApproval({
      id: "apr_dana_spend",
      sessionId: danaSession.id,
      requester: DANA,
      kind: ActionKind.NEW_SPEND,
      hint: "Create a managed database for inventory-dashboard (about $38 a month)?",
      calls: [
        {
          $typeName: "workbench.v1.ToolCall",
          id: "tool-1",
          callId: "call-db-1",
          name: "create_cloudsql_instance",
          args: { name: "inventory-dashboard-db", tier: "db-g1-small", region: "europe-west2", estimated_monthly_usd: 38 },
          kind: ActionKind.NEW_SPEND,
        },
      ],
      approverGroups: ["finance-leads"],
      requesterMayDecide: false,
      callerMayDecide: true,
      appId: "app_inventory",
      createdAt: ago(25),
    }),
  );
  world.approvals.set(
    "apr_lunch_prod",
    makeApproval({
      id: "apr_lunch_prod",
      sessionId: lunch.id,
      taskId: lunchTask.id,
      kind: ActionKind.PRODUCTION_DEPLOY,
      state: ApprovalState.APPROVED,
      hint: "Put lunch-poll into production",
      calls: [
        { $typeName: "workbench.v1.ToolCall", id: "tool-1", callId: "c1", name: "promote_to_production", args: { app: "lunch-poll", revision: "lunch-poll-00011" }, kind: ActionKind.PRODUCTION_DEPLOY },
      ],
      approverGroups: ["platform-approvers"],
      requesterMayDecide: false,
      decidedBy: SAM,
      reason: "Checked the preview — looks good.",
      createdAt: ago(60 * 48 + 20),
      decidedAt: ago(60 * 48),
      appId: "app_lunch",
    }),
  );
  world.approvals.set(
    "apr_old_delete",
    makeApproval({
      id: "apr_old_delete",
      sessionId: danaSession.id,
      requester: DANA,
      kind: ActionKind.DATA_DELETION,
      state: ApprovalState.APPROVED,
      hint: "Delete 1,204 test rows from the inventory sheet",
      calls: [
        { $typeName: "workbench.v1.ToolCall", id: "tool-1", callId: "c2", name: "delete_rows", args: { sheet: "inventory-test", rows: 1204 }, kind: ActionKind.DATA_DELETION },
      ],
      approverGroups: ["finance-leads"],
      requesterMayDecide: false,
      decidedBy: ME,
      reason: "These are only test rows.",
      createdAt: ago(80),
      decidedAt: ago(70),
    }),
  );

  world.remembered.push(
    { $typeName: "workbench.v1.RememberedAnswer", id: uuid(), scope: RememberScope.ALWAYS, toolName: "read_file", agent: AGENT, sessionId: "", approved: true, createdAt: ago(60 * 24 * 3) },
    { $typeName: "workbench.v1.RememberedAnswer", id: uuid(), scope: RememberScope.CONVERSATION, toolName: "write_file", agent: AGENT, sessionId: invoice.id, approved: true, createdAt: ago(60 * 6) },
  );

  // Audit
  const audit = (minutes: number, type: string, summary: string, actor: Person, extra: Partial<AuditEvent> = {}): AuditEvent => ({
    $typeName: "workbench.v1.AuditEvent",
    id: uuid(),
    time: ago(minutes),
    type,
    actor,
    sessionId: "",
    taskId: "",
    appId: "",
    approvalId: "",
    summary,
    surface: "web",
    ...extra,
  });
  world.audit.push(
    audit(70, "approval.decided", "Approved deleting 1,204 test rows", ME, { approvalId: "apr_old_delete", sessionId: danaSession.id }),
    audit(80, "approval.requested", "Asked to delete 1,204 test rows", DANA, { approvalId: "apr_old_delete", sessionId: danaSession.id }),
    audit(60 * 5, "app.deployed", "Deployed invoice-reminders-00004 to preview", ME, { appId: "app_invoice", sessionId: invoice.id }),
    audit(60 * 6, "app.registered", "Registered invoice-reminders", ME, { appId: "app_invoice", sessionId: invoice.id }),
    audit(60 * 6, "conversation.started", "Started “Invoice reminder emails”", ME, { sessionId: invoice.id }),
    audit(60 * 48, "app.promoted", "Promoted lunch-poll-00011 to production", SAM, { appId: "app_lunch", approvalId: "apr_lunch_prod", surface: "teams" }),
    audit(60 * 48, "approval.decided", "Approved production deploy of lunch-poll", SAM, { approvalId: "apr_lunch_prod", sessionId: lunch.id, surface: "teams" }),
    audit(60 * 48 + 20, "approval.requested", "Asked to put lunch-poll into production", ME, { approvalId: "apr_lunch_prod", sessionId: lunch.id }),
  );

  // Notifications
  const note = (minutes: number, kind: NotificationKind, title: string, body: string, link: string, read: boolean): Notification => ({
    $typeName: "workbench.v1.Notification",
    id: uuid(),
    kind,
    title,
    body,
    link,
    sessionId: "",
    approvalId: "",
    appId: "",
    read,
    createdAt: ago(minutes),
  });
  world.notifications.push(
    note(25, NotificationKind.APPROVAL_NEEDED, "Dana needs your approval", "New spend: a managed database for inventory-dashboard.", "/approvals/apr_dana_spend", false),
    note(60 * 5, NotificationKind.TASK_FINISHED, "invoice-reminders is ready to try", "The preview is deployed.", `/c/${invoice.id}`, false),
    note(60 * 48, NotificationKind.APP_PROMOTED, "lunch-poll is live", "Sam approved the production deploy.", "/apps/app_lunch", true),
  );

  const allKinds = [
    NotificationKind.APPROVAL_NEEDED,
    NotificationKind.APPROVAL_DECIDED,
    NotificationKind.INPUT_NEEDED,
    NotificationKind.TASK_FINISHED,
    NotificationKind.TASK_FAILED,
    NotificationKind.APP_PROMOTED,
    NotificationKind.LIMIT_REACHED,
  ];
  world.preferences = allKinds.map((kind) => ({
    $typeName: "workbench.v1.Preference",
    kind,
    channels:
      kind === NotificationKind.APPROVAL_NEEDED || kind === NotificationKind.INPUT_NEEDED
        ? [Channel.IN_APP, Channel.EMAIL, Channel.TEAMS]
        : [Channel.IN_APP],
  }));

  world.identities.push({
    $typeName: "workbench.v1.LinkedIdentity",
    issuer: "login.microsoftonline.com/7c1e-example/v2.0",
    subject: "a1b2c3",
    displayHint: "Microsoft Teams · ava.chen@example.com",
    linkedAt: ago(60 * 24 * 20),
  });

  world.limits.push(
    { $typeName: "workbench.v1.Limit", subject: LimitSubject.PERSON, subjectId: ME.id, monthlyCents: 5000n, spentCentsMtd: 2312n },
    { $typeName: "workbench.v1.Limit", subject: LimitSubject.TEAM, subjectId: "finance-team", monthlyCents: 50000n, spentCentsMtd: 18744n },
  );

  world.schedules.push({
    $typeName: "kagent.api.v1.ScheduledRun",
    id: uuid(),
    creator: ME.id,
    agent: { $typeName: "kagent.api.v1.ResourceReference", namespace: "workbench", name: "builder" },
    config: {
      $typeName: "kagent.api.v1.ScheduledRunConfig",
      name: "Weekly overdue invoice summary",
      schedule: "0 9 * * 1",
      timeZone: "Europe/London",
      prompt: "Every Monday, summarise overdue invoices from invoice-reminders and email the list to me.",
      paused: false,
    },
    etag: uuid(),
    nextExecutionTime: timestampFromDate(nextMonday9()),
    createdAt: ago(60 * 24 * 7),
    updatedAt: ago(60 * 24 * 7),
  });
}

function nextMonday9(): Date {
  const d = new Date();
  d.setHours(9, 0, 0, 0);
  const day = d.getDay();
  const add = (8 - day) % 7 || 7;
  d.setDate(d.getDate() + add);
  return d;
}

export const ROLES_ME = [WbRole.REQUESTER, WbRole.APPROVER];

let world: World | undefined;
export function mockWorld(): World {
  world ??= new World();
  return world;
}
export function resetMockWorld(pace = 1): World {
  world = new World();
  world.pace = pace;
  return world;
}
