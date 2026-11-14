/**
 * Runtime configuration, loaded from `/config.json` before the app mounts so one
 * build serves every environment.
 */
export interface WorkbenchConfig {
  oidcIssuer: string;
  oidcClientId: string;
  /** Root of the gRPC-Web services; same origin, normally `/api`. */
  apiBaseUrl: string;
  /** The builder agent new conversations go to, as `namespace/name`. */
  defaultAgent: string;
  /** Serve every call from in-process fixtures instead of the network. */
  mock?: boolean;
  /** Optional OIDC scope override. */
  oidcScope?: string;
}

let current: WorkbenchConfig | undefined;

export async function loadConfig(url = "/config.json"): Promise<WorkbenchConfig> {
  const response = await fetch(url, { cache: "no-store" });
  if (!response.ok) throw new Error(`Could not load ${url} (${response.status}).`);
  const raw = (await response.json()) as Partial<WorkbenchConfig>;
  current = normaliseConfig(raw);
  return current;
}

export function normaliseConfig(raw: Partial<WorkbenchConfig>): WorkbenchConfig {
  return {
    oidcIssuer: raw.oidcIssuer ?? "",
    oidcClientId: raw.oidcClientId ?? "",
    apiBaseUrl: (raw.apiBaseUrl ?? "/api").replace(/\/$/, ""),
    defaultAgent: raw.defaultAgent ?? "",
    mock: raw.mock === true,
    oidcScope: raw.oidcScope,
  };
}

export function setConfig(config: WorkbenchConfig): void {
  current = config;
}

export function config(): WorkbenchConfig {
  if (!current) throw new Error("Configuration has not been loaded.");
  return current;
}

/** `namespace/name` split for kagent's ResourceReference. */
export function agentRef(agent: string): { namespace: string; name: string } {
  const [namespace = "", name = ""] = agent.split("/", 2);
  return { namespace, name };
}
