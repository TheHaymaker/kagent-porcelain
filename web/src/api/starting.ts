/**
 * Workbench pauses its servers after an idle hour and starts them on the next
 * request (tools/internal/waker). While they start, nginx answers API calls
 * with 503 and `X-Workbench-Starting: 1`. This module notices that, so the
 * app can say what is happening instead of "couldn't reach Workbench", and
 * reload by itself once the server answers.
 */
import { ConnectError } from "@connectrpc/connect";

export const STARTING_HEADER = "x-workbench-starting";

type Listener = (starting: boolean) => void;
let starting = false;
const listeners = new Set<Listener>();

export function isStartingError(error: unknown): boolean {
  return error instanceof ConnectError && error.metadata.get(STARTING_HEADER) === "1";
}

export function markStarting(): void {
  if (starting) return;
  starting = true;
  for (const l of listeners) l(true);
}

export function isStarting(): boolean {
  return starting;
}

export function onStarting(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/**
 * Whether the server answers again: any API response without the starting
 * header (a 404 for this path is fine) means it is up.
 */
export async function serverIsUp(apiBaseUrl: string, fetchFn: typeof fetch = fetch): Promise<boolean> {
  try {
    const r = await fetchFn(`${apiBaseUrl.replace(/\/$/, "")}/workbench.ping`, { method: "GET", cache: "no-store" });
    return r.headers.get(STARTING_HEADER) !== "1" && r.status !== 502 && r.status !== 504;
  } catch {
    return false;
  }
}
