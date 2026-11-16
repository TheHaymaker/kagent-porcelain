/**
 * The scripted builder agent mock mode runs. The first message in a new
 * conversation gets the full build: a question, tool calls, a Progress +
 * DeployPreview surface, an ordinary approval the requester can decide, and a
 * production-deploy approval they cannot. Later messages get a short reply.
 */
import type { JsonObject, JsonValue } from "@bufbuild/protobuf";
import { TaskState } from "@/gen/a2a_pb";
import { ActionKind, ApprovalState } from "@/gen/workbench/v1/approvals_pb";
import { AppStatus, EnvironmentName } from "@/gen/workbench/v1/apps_pb";
import { NotificationKind } from "@/gen/workbench/v1/notifications_pb";
import {
  a2uiPart,
  agentMessage,
  hitlMessage,
  mockId,
  textPart,
  toolCallPart,
  toolResultPart,
  type MockTask,
} from "./runtime";
import { makeApp, makeApproval, ME, now, type World } from "./world";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

const CATALOG = "https://workbench.dev/catalogs/workbench/v1/catalog.json";

export class ScriptCtx {
  constructor(
    readonly world: World,
    readonly task: MockTask,
  ) {}

  get sessionId(): string {
    return this.task.contextId;
  }

  sleep(ms: number): Promise<void> {
    const scaled = ms * this.world.pace;
    return scaled <= 0 ? Promise.resolve() : new Promise((r) => setTimeout(r, scaled));
  }

  working(parts = [] as ReturnType<typeof textPart>[]): void {
    this.task.setStatus(TaskState.WORKING, parts.length ? agentMessage(this.task.id, this.task.contextId, parts) : undefined);
  }

  /** Streams prose the way the runtime does: appended artifact chunks, then the whole text. */
  async say(text: string): Promise<void> {
    const artifactId = mockId("a");
    const words = text.split(/(?<=\s)/);
    let first = true;
    for (let i = 0; i < words.length; i += 3) {
      const chunk = words.slice(i, i + 3).join("");
      this.task.emit({
        case: "artifactUpdate",
        value: {
          $typeName: "lf.a2a.v1.TaskArtifactUpdateEvent",
          taskId: this.task.id,
          contextId: this.task.contextId,
          artifact: { $typeName: "lf.a2a.v1.Artifact", artifactId, name: "", description: "", parts: [textPart(chunk)], extensions: [] },
          append: !first,
          lastChunk: false,
        },
      });
      first = false;
      await this.sleep(45);
    }
    this.task.emit({
      case: "artifactUpdate",
      value: {
        $typeName: "lf.a2a.v1.TaskArtifactUpdateEvent",
        taskId: this.task.id,
        contextId: this.task.contextId,
        artifact: { $typeName: "lf.a2a.v1.Artifact", artifactId, name: "", description: "", parts: [textPart(text)], extensions: [] },
        append: false,
        lastChunk: true,
      },
    });
    await this.sleep(150);
  }

  async tool(name: string, args: JsonObject, response: JsonValue, ms = 500): Promise<void> {
    const id = mockId("call");
    this.working([toolCallPart(id, name, args)]);
    await this.sleep(ms);
    this.working([toolResultPart(id, name, response)]);
    await this.sleep(120);
  }

  /** Route b: the workbench_show MCP tool, whose structured result carries the A2UI messages. */
  async show(surfaceId: string, messages: JsonObject[]): Promise<void> {
    this.world.storeSurface(this.sessionId, this.task.id, messages);
    await this.tool(
      "workbench_show",
      { surfaceId, messages: messages as JsonValue },
      { content: [{ type: "text", text: `Shown ${surfaceId}.` }], structuredContent: { a2ui: { surfaceId, messages: messages as JsonValue } }, isError: false },
      250,
    );
  }

  /** Route a: an A2UI DataPart in the stream. */
  a2ui(messages: JsonObject[]): void {
    this.world.storeSurface(this.sessionId, this.task.id, messages);
    this.working([a2uiPart(messages as JsonValue[])]);
  }

  async askUser(questions: { question: string; choices: string[]; multiple?: boolean }[]): Promise<string[][]> {
    const requestId = mockId("ask");
    const fallback = questions.map((q) => q.question).join("; ");
    this.task.pendingAskId = requestId;
    this.task.setStatus(
      TaskState.INPUT_REQUIRED,
      hitlMessage(this.task, fallback, {
        type: "ask_user_request",
        id: requestId,
        questions: questions.map((q) => ({ question: q.question, choices: q.choices, multiple: q.multiple ?? false })),
      }),
    );
    this.world.notify(NotificationKind.INPUT_NEEDED, "The builder has a question", questions[0]?.question ?? "", `/c/${this.sessionId}`, { sessionId: this.sessionId });
    const answers = await this.task.gate<string[][]>(`ask:${requestId}`);
    this.task.pendingAskId = undefined;
    return answers;
  }

  /**
   * Pauses for a tool approval. The pause is HITL tool_approval_request metadata
   * (what kagent emits); the Approval is what the Workbench server would create.
   */
  async approval(def: {
    id: string;
    tool: string;
    args: JsonObject;
    kind: ActionKind;
    hint: string;
    approverGroups: string[];
    requesterMayDecide: boolean;
    appId?: string;
    /** Runs once the approval exists, before the pause. */
    announce?: () => void;
  }): Promise<boolean> {
    const toolId = "tool-1";
    const callId = mockId("call");
    this.working([toolCallPart(callId, def.tool, def.args)]);
    await this.sleep(200);
    this.world.approvals.set(
      def.id,
      makeApproval({
        id: def.id,
        sessionId: this.sessionId,
        taskId: this.task.id,
        kind: def.kind,
        hint: def.hint,
        calls: [{ $typeName: "workbench.v1.ToolCall", id: toolId, callId, name: def.tool, args: def.args, kind: def.kind }],
        approverGroups: def.approverGroups,
        requesterMayDecide: def.requesterMayDecide,
        callerMayDecide: def.requesterMayDecide,
        appId: def.appId ?? "",
      }),
    );
    this.world.recordAudit("approval.requested", def.hint, { approvalId: def.id, sessionId: this.sessionId, taskId: this.task.id });
    this.task.pendingApprovalId = def.id;
    def.announce?.();
    this.task.setStatus(
      TaskState.INPUT_REQUIRED,
      hitlMessage(this.task, `${def.hint} (${def.tool})`, {
        type: "tool_approval_request",
        hint: def.hint,
        tools: [{ id: toolId, call_id: callId, name: def.tool, args: def.args }],
      }),
    );
    const approved = await this.task.gate<boolean>(`approval:${def.id}`);
    this.task.pendingApprovalId = undefined;
    this.working([
      toolResultPart(
        callId,
        def.tool,
        approved ? { content: [{ type: "text", text: "Done." }], isError: false } : { error: "Tool call was rejected by user." },
      ),
    ]);
    await this.sleep(150);
    return approved;
  }

  complete(): void {
    this.task.setStatus(TaskState.COMPLETED);
    this.world.addCheckpoint(this.sessionId, this.task.id);
    this.world.touch(this.sessionId);
  }
}

function steps(states: string[]): JsonObject[] {
  const labels = ["Scaffold from python-cloudrun", "Write the app", "Run the tests", "Deploy a preview"];
  return labels.map((label, i) => ({ label, state: states[i] ?? "pending" }));
}

export async function buildScript(s: ScriptCtx, prompt: string): Promise<void> {
  const world = s.world;
  s.working();
  await s.sleep(300);
  await s.say("Happy to help. Before I start, one quick question so I set it up for the right people.");
  const answers = await s.askUser([
    {
      question: "Who should be able to use it?",
      choices: ["Just my team", "The whole finance department", "Everyone at the company"],
    },
  ]);
  const audience = answers[0]?.[0] ?? "your team";
  s.working();
  await s.say(
    `Got it — **${audience.toLowerCase()}**. Here's the plan:\n\n1. Start from our standard web app template\n2. Write the app and its tests\n3. Put a preview online for you to try`,
  );

  const appId = "app_expense";
  world.apps.set(
    appId,
    makeApp({
      id: appId,
      name: "expense-tracker",
      description: prompt.length > 140 ? `${prompt.slice(0, 137)}…` : prompt,
      status: AppStatus.BUILDING,
      sessionId: s.sessionId,
      costCentsMtd: 0n,
      createdAt: now(),
      updatedAt: now(),
    }),
  );
  world.recordAudit("app.registered", "Registered expense-tracker", { appId, sessionId: s.sessionId });

  await s.tool("scaffold_app", { scaffold: "python-cloudrun", name: "expense-tracker" }, { content: [{ type: "text", text: "Created 14 files from python-cloudrun." }], isError: false }, 700);

  await s.show("deploy-1", [
    { version: "v0.9.1", createSurface: { surfaceId: "deploy-1", catalogId: CATALOG } },
    {
      version: "v0.9.1",
      updateComponents: {
        surfaceId: "deploy-1",
        components: [
          { id: "root", component: "Column", children: ["progress", "preview"] },
          { id: "progress", component: "Progress", title: "Building expense-tracker", steps: steps(["done", "running"]), percent: { path: "/build/percent" } },
          { id: "preview", component: "DeployPreview", appId, appName: "expense-tracker", environment: "preview", status: "deploying" },
        ],
      },
    },
    { version: "v0.9.1", updateDataModel: { surfaceId: "deploy-1", path: "/build", value: { percent: 25 } } },
  ]);

  await s.tool("read_file", { path: "templates/base.html" }, { content: [{ type: "text", text: "<!doctype html>…" }], isError: false }, 350);
  await s.tool("write_file", { path: "app/main.py", bytes: 2841 }, { content: [{ type: "text", text: "Saved app/main.py" }], isError: false }, 600);
  await s.tool("write_file", { path: "app/templates/expenses.html", bytes: 3920 }, { content: [{ type: "text", text: "Saved app/templates/expenses.html" }], isError: false }, 500);
  s.a2ui([{ version: "v0.9.1", updateDataModel: { surfaceId: "deploy-1", path: "/build/percent", value: 50 } }]);
  await s.tool("write_file", { path: "tests/test_expenses.py", bytes: 1710 }, { content: [{ type: "text", text: "Saved tests/test_expenses.py" }], isError: false }, 400);
  await s.tool("run_tests", { command: "pytest -q" }, { content: [{ type: "text", text: "12 passed in 1.84s" }], isError: false }, 900);
  // An agent polling a build: the conversation shows it as one row with a count.
  await s.tool("build_start_image", { project: "expense-tracker" }, { content: [{ type: "text", text: '{"build_id":"b-1"}' }], isError: false }, 300);
  for (let i = 0; i < 5; i++) {
    await s.tool("build_get_status", { build_id: "b-1" }, { content: [{ type: "text", text: '{"status":"WORKING"}' }], isError: false }, 250);
  }
  s.a2ui([
    {
      version: "v0.9.1",
      updateComponents: {
        surfaceId: "deploy-1",
        components: [{ id: "progress", component: "Progress", title: "Building expense-tracker", steps: steps(["done", "done", "done", "pending"]), percent: { path: "/build/percent" } }],
      },
    },
    { version: "v0.9.1", updateDataModel: { surfaceId: "deploy-1", path: "/build/percent", value: 70 } },
  ]);

  await s.say("The app needs somewhere to keep receipts. I'd like to create a small storage bucket for the preview.");
  const bucketOk = await s.approval({
    id: `apr_${s.task.id.slice(0, 8)}_bucket`,
    tool: "create_storage_bucket",
    args: { name: "expense-tracker-preview-receipts", location: "europe-west2", environment: "preview" },
    kind: ActionKind.ORDINARY,
    hint: "Create a storage bucket for receipts in the preview?",
    approverGroups: [],
    requesterMayDecide: true,
  });
  await s.say(bucketOk ? "Thanks — bucket created. Deploying the preview now." : "No problem — I'll keep receipts in the app's own database instead. Deploying the preview now.");

  s.a2ui([
    {
      version: "v0.9.1",
      updateComponents: {
        surfaceId: "deploy-1",
        components: [{ id: "progress", component: "Progress", title: "Building expense-tracker", steps: steps(["done", "done", "done", "running"]), percent: { path: "/build/percent" } }],
      },
    },
    { version: "v0.9.1", updateDataModel: { surfaceId: "deploy-1", path: "/build/percent", value: 85 } },
  ]);
  await s.tool("deploy_preview", { app: "expense-tracker", region: "europe-west2" }, { content: [{ type: "text", text: "Revision expense-tracker-00007 is serving." }], isError: false }, 1200);

  const previewUrl = "https://expense-tracker-preview.run.example.com";
  const app = world.apps.get(appId)!;
  app.status = AppStatus.PREVIEW;
  app.costCentsMtd = 412n;
  app.updatedAt = now();
  app.environments = [
    { $typeName: "workbench.v1.Environment", name: EnvironmentName.PREVIEW, url: previewUrl, revision: "expense-tracker-00007", image: "europe-docker.pkg.dev/workbench/apps/expense-tracker-00007", updatedAt: now() },
  ];
  world.deployments.unshift({
    $typeName: "workbench.v1.Deployment",
    id: mockId("dep"),
    appId,
    environment: EnvironmentName.PREVIEW,
    revision: "expense-tracker-00007",
    image: "",
    url: previewUrl,
    deployedBy: ME,
    approvalId: "",
    createdAt: timestampFromDate(new Date()),
  });

  s.a2ui([
    {
      version: "v0.9.1",
      updateComponents: {
        surfaceId: "deploy-1",
        components: [
          { id: "progress", component: "Progress", title: "Built expense-tracker", steps: steps(["done", "done", "done", "done"]), percent: { path: "/build/percent" } },
          { id: "preview", component: "DeployPreview", appId, appName: "expense-tracker", environment: "preview", status: "ready", url: previewUrl, revision: "expense-tracker-00007", logsUrl: "https://console.example.com/run/expense-tracker/logs" },
        ],
      },
    },
    { version: "v0.9.1", updateDataModel: { surfaceId: "deploy-1", path: "/build/percent", value: 100 } },
  ]);
  await s.say("Your preview is ready to try. Have a play — add an expense and attach a receipt.");

  await s.show("done-1", [
    { version: "v0.9.1", createSurface: { surfaceId: "done-1", catalogId: CATALOG } },
    {
      version: "v0.9.1",
      updateComponents: {
        surfaceId: "done-1",
        components: [
          { id: "root", component: "Row", children: ["app", "cost"], align: "start" },
          { id: "app", component: "AppCard", appId, name: "expense-tracker" },
          { id: "cost", component: "CostSummary", title: "This build", period: "this conversation", totalCents: 412, limitCents: 5000, lines: [{ label: "Model", cents: 388 }, { label: "Cloud Run preview", cents: 24 }] },
        ],
      },
    },
  ]);

  await s.say("Next, I'll ask to put it into production so the rest of the team can use it.");
  const prodId = `apr_${s.task.id.slice(0, 8)}_prod`;
  app.pendingApprovalId = prodId;
  const prodOk = await s.approval({
    id: prodId,
    tool: "promote_to_production",
    args: { app: "expense-tracker", revision: "expense-tracker-00007" },
    kind: ActionKind.PRODUCTION_DEPLOY,
    hint: "Promote expense-tracker to production",
    approverGroups: ["platform-approvers"],
    requesterMayDecide: false,
    appId,
    // The approval is announced with an ApprovalCard surface too (route a). The
    // card draws its state from the ApprovalService, never from these props.
    announce: () =>
      s.a2ui([
        { version: "v0.9.1", createSurface: { surfaceId: "approval-1", catalogId: CATALOG } },
        {
          version: "v0.9.1",
          updateComponents: {
            surfaceId: "approval-1",
            components: [
              {
                id: "root",
                component: "ApprovalCard",
                approvalId: prodId,
                title: "Promote expense-tracker to production",
                summary: "Revision expense-tracker-00007 has passed its checks in preview.",
              },
            ],
          },
        },
      ]),
  });
  app.pendingApprovalId = "";
  if (prodOk) {
    app.status = AppStatus.LIVE;
    await s.say("It's live in production. 🎉");
  } else {
    await s.say("Production was declined, so the app stays in preview. Let me know what you'd like to change.");
  }
  s.complete();
}

export async function followUpScript(s: ScriptCtx, prompt: string): Promise<void> {
  s.working();
  await s.sleep(300);
  await s.tool("read_file", { path: "app/main.py" }, { content: [{ type: "text", text: "…" }], isError: false }, 400);
  await s.say(`Noted: “${prompt.slice(0, 120)}”. I've made that change in the preview — refresh the preview link to see it.`);
  s.complete();
}

/** Marks approvals superseded when the task they paused is cancelled. */
export function supersedeFor(world: World, taskId: string): void {
  for (const approval of world.approvals.values()) {
    if (approval.taskId === taskId && approval.state === ApprovalState.PENDING) {
      approval.state = ApprovalState.SUPERSEDED;
      approval.callerMayDecide = false;
    }
  }
}
