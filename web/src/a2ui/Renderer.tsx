/**
 * Our renderer for the Workbench A2UI catalogue (v0.9.1). It draws exactly the
 * catalogue's components; anything else becomes a quiet fallback. Every string
 * is rendered as text, links must be https, and domain components
 * (ApprovalCard, AppCard) read their truth from the server, never the agent.
 */
import { createContext, useContext, useState, useSyncExternalStore, type ReactNode } from "react";
import { Link as RouterLink } from "react-router";
import { AlertCircle, CheckCircle2, Circle, CircleDashed, ExternalLink, Loader2, MinusCircle, Rocket, XCircle } from "lucide-react";
import { api } from "@/api/clients";
import { friendlyError } from "@/api/errors";
import { useRpc } from "@/api/useRpc";
import { ApprovalPanel } from "@/approvals/ApprovalPanel";
import { EnvironmentName } from "@/gen/workbench/v1/apps_pb";
import { APP_STATUS_LABEL, APP_STATUS_TONE, money, personName, safeHttpsUrl } from "@/lib/format";
import { Badge } from "@/ui/Badge";
import { Button } from "@/ui/Button";
import { Meter } from "@/ui/Meter";
import { Markdown } from "./markdown";
import { getAt } from "./pointer";
import type { SurfaceStore } from "./store";
import type { A2uiComponent, SurfaceState } from "./types";

export const CATALOG_COMPONENTS = [
  "Text",
  "Column",
  "Row",
  "Card",
  "Divider",
  "Link",
  "Button",
  "ApprovalCard",
  "Progress",
  "DeployPreview",
  "AppCard",
  "CostSummary",
] as const;

export interface ActionEvent {
  surfaceId: string;
  name: string;
  sourceComponentId: string;
  context: Record<string, unknown>;
}

export interface RenderOptions {
  /** Where Button actions go. Defaults to ComponentService.SendAction. */
  sendAction?: (event: ActionEvent) => Promise<void>;
  /** Told when an ApprovalCard's approval is decided. */
  onApprovalSettled?: (approvalId: string) => void;
}

interface Ctx extends RenderOptions {
  surface: SurfaceState;
}

const SurfaceContext = createContext<Ctx | undefined>(undefined);

const MAX_DEPTH = 24;

export async function defaultSendAction(event: ActionEvent): Promise<void> {
  await api.components.sendAction({
    surfaceId: event.surfaceId,
    name: event.name,
    sourceComponentId: event.sourceComponentId,
    context: event.context as never,
  });
}

/** A surface from a store, re-rendered as messages arrive. */
export function A2uiSurface({ store, surfaceId, ...options }: { store: SurfaceStore; surfaceId: string } & RenderOptions) {
  useSyncExternalStore(store.subscribe, store.getVersion);
  const surface = store.get(surfaceId);
  if (!surface || surface.deleted) return null;
  return <SurfaceView surface={surface} {...options} />;
}

export function SurfaceView({ surface, ...options }: { surface: SurfaceState } & RenderOptions) {
  const root = surface.components.root;
  if (!root) return null;
  return (
    <SurfaceContext.Provider value={{ surface, ...options }}>
      <div className="a2ui" data-surface-id={surface.surfaceId}>
        <ComponentNode id="root" depth={0} trail={[]} />
      </div>
    </SurfaceContext.Provider>
  );
}

function useSurface(): Ctx {
  const ctx = useContext(SurfaceContext);
  if (!ctx) throw new Error("A2UI component outside a surface");
  return ctx;
}

/** Resolves a literal or a `{path}` binding against the surface's data model. */
export function resolveDynamic(value: unknown, dataModel: unknown): unknown {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const path = (value as Record<string, unknown>).path;
    if (typeof path === "string" && Object.keys(value).length === 1) return getAt(dataModel, path);
  }
  return value;
}

export function resolveString(value: unknown, dataModel: unknown): string {
  const resolved = resolveDynamic(value, dataModel);
  if (typeof resolved === "string") return resolved;
  if (typeof resolved === "number" || typeof resolved === "boolean") return String(resolved);
  return "";
}

export function resolveNumber(value: unknown, dataModel: unknown): number | undefined {
  const resolved = resolveDynamic(value, dataModel);
  if (typeof resolved === "number" && Number.isFinite(resolved)) return resolved;
  if (typeof resolved === "string" && resolved.trim() !== "" && Number.isFinite(Number(resolved))) return Number(resolved);
  return undefined;
}

/** Resolves bindings one level deep inside an action's context object. */
function resolveContext(context: unknown, dataModel: unknown): Record<string, unknown> {
  if (!context || typeof context !== "object" || Array.isArray(context)) return {};
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(context)) out[key] = resolveDynamic(value, dataModel) ?? null;
  return out;
}

function ComponentNode({ id, depth, trail }: { id: string; depth: number; trail: string[] }) {
  const { surface } = useSurface();
  const component = surface.components[id];
  if (!component) return null;
  if (depth > MAX_DEPTH || trail.includes(id)) return <Fallback reason="This part is nested too deeply to show." />;
  return <Dispatch component={component} depth={depth} trail={[...trail, id]} />;
}

function Children({ ids, depth, trail }: { ids: unknown; depth: number; trail: string[] }) {
  if (!Array.isArray(ids)) return null;
  return (
    <>
      {ids
        .filter((c): c is string => typeof c === "string")
        .slice(0, 50)
        .map((child) => (
          <ComponentNode key={child} id={child} depth={depth + 1} trail={trail} />
        ))}
    </>
  );
}

function Dispatch({ component, depth, trail }: { component: A2uiComponent; depth: number; trail: string[] }) {
  const { surface } = useSurface();
  const dm = surface.dataModel;
  const c = component;
  switch (c.component) {
    case "Text":
      return <TextView text={resolveString(c.text, dm)} variant={typeof c.variant === "string" ? c.variant : "body"} />;
    case "Column":
      return (
        <div className={`a2-col align-${oneOf(c.align, ["start", "center", "end", "stretch"], "stretch")}`}>
          <Children ids={c.children} depth={depth} trail={trail} />
        </div>
      );
    case "Row":
      return (
        <div
          className={`a2-row justify-${oneOf(c.justify, ["start", "center", "end", "spaceBetween"], "start")} align-${oneOf(c.align, ["start", "center", "end"], "center")}`}
        >
          <Children ids={c.children} depth={depth} trail={trail} />
        </div>
      );
    case "Card":
      return (
        <div className="a2-card">{typeof c.child === "string" && <ComponentNode id={c.child} depth={depth + 1} trail={trail} />}</div>
      );
    case "Divider":
      return <hr className="a2-divider" />;
    case "Link":
      return <LinkView label={resolveString(c.label, dm)} url={c.url} />;
    case "Button":
      return <ButtonView component={c} />;
    case "ApprovalCard":
      if (typeof c.approvalId !== "string" || c.approvalId === "") return <Fallback reason="An approval card is missing its approval." />;
      return <ApprovalCardView approvalId={c.approvalId} title={resolveString(c.title, dm)} summary={resolveString(c.summary, dm)} />;
    case "Progress":
      return <ProgressView component={c} />;
    case "DeployPreview":
      return <DeployPreviewView component={c} />;
    case "AppCard":
      if (typeof c.appId !== "string" || c.appId === "") return <Fallback reason="An app card is missing its app." />;
      return <AppCardView appId={c.appId} name={resolveString(c.name, dm)} description={resolveString(c.description, dm)} />;
    case "CostSummary":
      return <CostSummaryView component={c} />;
    default:
      return <Fallback reason={`This part of the reply (“${String(c.component).slice(0, 40)}”) can't be shown here.`} />;
  }
}

function oneOf<T extends string>(value: unknown, allowed: readonly T[], fallback: T): T {
  return typeof value === "string" && (allowed as readonly string[]).includes(value) ? (value as T) : fallback;
}

export function Fallback({ reason }: { reason: string }) {
  return (
    <div className="a2-fallback" role="note">
      <AlertCircle size={16} aria-hidden />
      <span>{reason}</span>
    </div>
  );
}

function TextView({ text, variant }: { text: string; variant: string }) {
  switch (variant) {
    case "h1":
      return <p className="a2-text a2-h1" role="heading" aria-level={3}>{text}</p>;
    case "h2":
      return <p className="a2-text a2-h2" role="heading" aria-level={4}>{text}</p>;
    case "h3":
      return <p className="a2-text a2-h3" role="heading" aria-level={5}>{text}</p>;
    case "caption":
      return <p className="a2-text a2-caption">{text}</p>;
    default:
      return (
        <div className="a2-text a2-body">
          <Markdown text={text} />
        </div>
      );
  }
}

function LinkView({ label, url }: { label: string; url: unknown }) {
  const href = safeHttpsUrl(url);
  if (!href) return <span className="a2-link-disabled">{label || "Link"}</span>;
  return (
    <a className="a2-link" href={href} target="_blank" rel="noopener noreferrer">
      {label || href}
      <ExternalLink size={14} aria-hidden />
      <span className="sr-only"> (opens in a new tab)</span>
    </a>
  );
}

function ButtonView({ component }: { component: A2uiComponent }) {
  const { surface, sendAction = defaultSendAction } = useSurface();
  const [state, setState] = useState<"idle" | "busy" | "sent" | "error">("idle");
  const [error, setError] = useState<string>();
  const label = resolveString(component.label, surface.dataModel) || "Continue";
  const action = component.action as { event?: { name?: unknown; context?: unknown } } | undefined;
  const name = typeof action?.event?.name === "string" ? action.event.name : undefined;
  const variant = oneOf(component.variant, ["primary", "secondary", "danger"], "secondary");
  if (!name) return <Fallback reason="A button here has no action." />;
  return (
    <span className="a2-button">
      <Button
        variant={variant}
        busy={state === "busy"}
        disabled={state === "sent"}
        onClick={async () => {
          setState("busy");
          setError(undefined);
          try {
            await sendAction({
              surfaceId: surface.surfaceId,
              name,
              sourceComponentId: component.id,
              context: resolveContext(action?.event?.context, surface.dataModel),
            });
            setState("sent");
          } catch (err) {
            setError(friendlyError(err));
            setState("error");
          }
        }}
      >
        {state === "sent" ? `${label} ✓` : label}
      </Button>
      {error && <span className="a2-error" role="alert">{error}</span>}
    </span>
  );
}

function ApprovalCardView({ approvalId, title, summary }: { approvalId: string; title: string; summary: string }) {
  const { onApprovalSettled } = useSurface();
  return (
    <ApprovalPanel
      approvalId={approvalId}
      title={title || undefined}
      summary={summary || undefined}
      onSettled={onApprovalSettled ? () => onApprovalSettled(approvalId) : undefined}
    />
  );
}

const STEP_ICON: Record<string, ReactNode> = {
  pending: <Circle size={16} aria-hidden />,
  running: <Loader2 size={16} className="spin" aria-hidden />,
  done: <CheckCircle2 size={16} aria-hidden />,
  failed: <XCircle size={16} aria-hidden />,
  skipped: <MinusCircle size={16} aria-hidden />,
};
const STEP_LABEL: Record<string, string> = {
  pending: "Not started",
  running: "In progress",
  done: "Done",
  failed: "Failed",
  skipped: "Skipped",
};

function ProgressView({ component }: { component: A2uiComponent }) {
  const { surface } = useSurface();
  const title = resolveString(component.title, surface.dataModel);
  const steps = Array.isArray(component.steps) ? component.steps.slice(0, 30) : [];
  const percent = resolveNumber(component.percent, surface.dataModel);
  return (
    <div className="a2-progress">
      {title && <p className="a2-progress-title">{title}</p>}
      {percent !== undefined && (
        <div className="a2-progress-bar">
          <Meter value={percent} max={100} label={title || "Progress"} tone="accent" />
          <span className="muted small">{Math.round(percent)}%</span>
        </div>
      )}
      <ol className="steps">
        {steps.map((raw, i) => {
          const step = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;
          const state = oneOf(step.state, ["pending", "running", "done", "failed", "skipped"], "pending");
          return (
            <li key={i} className={`step step-${state}`}>
              {STEP_ICON[state]}
              <span className="step-label">{typeof step.label === "string" ? step.label : ""}</span>
              <span className="sr-only">: {STEP_LABEL[state]}</span>
              {typeof step.detail === "string" && step.detail && <span className="step-detail">{step.detail}</span>}
            </li>
          );
        })}
      </ol>
    </div>
  );
}

function DeployPreviewView({ component }: { component: A2uiComponent }) {
  const { surface } = useSurface();
  const appName = resolveString(component.appName, surface.dataModel) || "Your app";
  const environment = oneOf(component.environment, ["preview", "production"], "preview");
  const status = oneOf(component.status, ["deploying", "ready", "failed"], "deploying");
  const url = safeHttpsUrl(component.url);
  const logs = safeHttpsUrl(component.logsUrl);
  const revision = typeof component.revision === "string" ? component.revision : undefined;
  return (
    <div className={`a2-deploy deploy-${status}`}>
      <div className="a2-deploy-icon" aria-hidden>
        {status === "deploying" ? <Loader2 size={20} className="spin" /> : status === "ready" ? <Rocket size={20} /> : <XCircle size={20} />}
      </div>
      <div className="a2-deploy-body">
        <div className="a2-deploy-head">
          <strong>{appName}</strong>
          <Badge tone={environment === "production" ? "warning" : "info"}>{environment === "production" ? "Production" : "Preview"}</Badge>
          <Badge tone={status === "ready" ? "success" : status === "failed" ? "danger" : "neutral"}>
            {status === "ready" ? "Ready to try" : status === "failed" ? "Deploy failed" : "Deploying…"}
          </Badge>
        </div>
        {revision && <p className="muted small">Version {revision}</p>}
        <div className="a2-deploy-actions">
          {url && status === "ready" && (
            <a className="btn btn-primary btn-sm" href={url} target="_blank" rel="noopener noreferrer">
              <span>Open app</span>
              <ExternalLink size={14} aria-hidden />
              <span className="sr-only"> (opens in a new tab)</span>
            </a>
          )}
          {logs && (
            <a className="btn btn-ghost btn-sm" href={logs} target="_blank" rel="noopener noreferrer">
              <span>View logs</span>
              <span className="sr-only"> (opens in a new tab)</span>
            </a>
          )}
        </div>
      </div>
    </div>
  );
}

function AppCardView({ appId, name, description }: { appId: string; name: string; description: string }) {
  const rpc = useRpc(async (signal) => (await api.apps.getApp({ id: appId }, { signal })).app, [appId], { pollMs: 15000 });
  const app = rpc.data;
  const preview = app?.environments.find((e) => e.name === EnvironmentName.PREVIEW);
  const production = app?.environments.find((e) => e.name === EnvironmentName.PRODUCTION);
  return (
    <div className="a2-appcard">
      <div className="a2-appcard-head">
        <RouterLink to={`/apps/${encodeURIComponent(appId)}`} className="a2-appcard-name">
          {app?.name || name || appId}
        </RouterLink>
        {app && <Badge tone={APP_STATUS_TONE[app.status]}>{APP_STATUS_LABEL[app.status]}</Badge>}
      </div>
      {(app?.description || description) && <p className="a2-appcard-desc">{app?.description || description}</p>}
      {app && (
        <dl className="kv">
          <div>
            <dt>Owner</dt>
            <dd>{personName(app.owner)}</dd>
          </div>
          <div>
            <dt>Cost this month</dt>
            <dd>{money(app.costCentsMtd)}</dd>
          </div>
        </dl>
      )}
      <div className="a2-appcard-links">
        {safeHttpsUrl(preview?.url) && (
          <a href={safeHttpsUrl(preview?.url)} target="_blank" rel="noopener noreferrer" className="a2-link">
            Preview <ExternalLink size={14} aria-hidden />
          </a>
        )}
        {safeHttpsUrl(production?.url) && (
          <a href={safeHttpsUrl(production?.url)} target="_blank" rel="noopener noreferrer" className="a2-link">
            Production <ExternalLink size={14} aria-hidden />
          </a>
        )}
      </div>
      {rpc.error && !app && <p className="muted small">Couldn't load the latest details. {rpc.error}</p>}
      {!app && !rpc.error && (
        <p className="muted small">
          <CircleDashed size={14} aria-hidden /> Loading app details…
        </p>
      )}
    </div>
  );
}

function CostSummaryView({ component }: { component: A2uiComponent }) {
  const { surface } = useSurface();
  const title = resolveString(component.title, surface.dataModel) || "Cost";
  const period = typeof component.period === "string" ? component.period : undefined;
  const total = resolveNumber(component.totalCents, surface.dataModel) ?? 0;
  const limit = resolveNumber(component.limitCents, surface.dataModel);
  const lines = Array.isArray(component.lines) ? component.lines.slice(0, 20) : [];
  return (
    <div className="a2-cost">
      <div className="a2-cost-head">
        <span className="a2-cost-title">{title}</span>
        {period && <span className="muted small">{period}</span>}
      </div>
      <p className="a2-cost-total">{money(total)}</p>
      {limit !== undefined && limit > 0 && (
        <div className="a2-cost-limit">
          <Meter value={total} max={limit} label={`${money(total)} of ${money(limit)} limit`} />
          <span className="muted small">
            {money(total)} of {money(limit)} limit
          </span>
        </div>
      )}
      {lines.length > 0 && (
        <table className="a2-cost-lines">
          <tbody>
            {lines.map((raw, i) => {
              const line = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;
              return (
                <tr key={i}>
                  <th scope="row">{typeof line.label === "string" ? line.label : ""}</th>
                  <td>{money(typeof line.cents === "number" ? line.cents : 0)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </div>
  );
}
