/**
 * The surfaces of one conversation.
 *
 * A2UI reaches a conversation three ways — an `application/a2ui+json` DataPart,
 * a `workbench_show` tool result, and ComponentService.ListSurfaces — and often
 * the same messages arrive by more than one. The store is keyed by surfaceId and
 * remembers every message it has applied to a surface, so a message replayed by
 * a second route is a no-op rather than a reset or a regression.
 */
import { setAt } from "./pointer";
import type { A2uiComponent, A2uiMessage, SurfaceState } from "./types";
import { WORKBENCH_CATALOG_ID } from "./types";

type Listener = () => void;

const MAX_COMPONENTS = 500;

export class SurfaceStore {
  private surfaces = new Map<string, SurfaceState>();
  private applied = new Map<string, Set<string>>();
  private listeners = new Set<Listener>();
  private version = 0;

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  /** Monotonic snapshot token for useSyncExternalStore. */
  getVersion = (): number => this.version;

  get(surfaceId: string): SurfaceState | undefined {
    return this.surfaces.get(surfaceId);
  }

  ids(): string[] {
    return [...this.surfaces.keys()];
  }

  /** Every approvalId an ApprovalCard on a live surface points at. */
  approvalIds(): Set<string> {
    const ids = new Set<string>();
    for (const surface of this.surfaces.values()) {
      if (surface.deleted) continue;
      for (const component of Object.values(surface.components)) {
        if (component.component === "ApprovalCard" && typeof component.approvalId === "string") ids.add(component.approvalId);
      }
    }
    return ids;
  }

  /**
   * Applies messages in order and returns the surfaceIds they touched.
   * Malformed messages are skipped, never thrown.
   */
  apply(messages: readonly unknown[]): string[] {
    const touched: string[] = [];
    let changed = false;
    for (const raw of messages) {
      const message = asMessage(raw);
      if (!message) continue;
      const surfaceId = surfaceIdOf(message);
      const key = stableKey(raw);
      let seen = this.applied.get(surfaceId);
      if (!seen) {
        seen = new Set();
        this.applied.set(surfaceId, seen);
      }
      if (seen.has(key)) {
        if (!touched.includes(surfaceId)) touched.push(surfaceId);
        continue;
      }
      seen.add(key);
      if (this.applyOne(message)) changed = true;
      if (!touched.includes(surfaceId)) touched.push(surfaceId);
    }
    if (changed) {
      this.version += 1;
      for (const listener of this.listeners) listener();
    }
    return touched;
  }

  private applyOne(message: A2uiMessage): boolean {
    if ("createSurface" in message) {
      const { surfaceId, catalogId } = message.createSurface;
      const existing = this.surfaces.get(surfaceId);
      if (existing && !existing.deleted) return false;
      this.surfaces.set(surfaceId, {
        surfaceId,
        catalogId: typeof catalogId === "string" ? catalogId : WORKBENCH_CATALOG_ID,
        components: {},
        dataModel: {},
        deleted: false,
        revision: 0,
      });
      return true;
    }
    if ("updateComponents" in message) {
      const { surfaceId, components } = message.updateComponents;
      const surface = this.ensure(surfaceId);
      if (surface.deleted) return false;
      const next: Record<string, A2uiComponent> = { ...surface.components };
      for (const component of components) {
        if (!isComponent(component)) continue;
        if (!(component.id in next) && Object.keys(next).length >= MAX_COMPONENTS) break;
        next[component.id] = component;
      }
      this.surfaces.set(surfaceId, { ...surface, components: next, revision: surface.revision + 1 });
      return true;
    }
    if ("updateDataModel" in message) {
      const { surfaceId, path, value } = message.updateDataModel;
      const surface = this.ensure(surfaceId);
      if (surface.deleted) return false;
      const dataModel = setAt(surface.dataModel, typeof path === "string" ? path : "/", value);
      this.surfaces.set(surfaceId, { ...surface, dataModel, revision: surface.revision + 1 });
      return true;
    }
    if ("deleteSurface" in message) {
      const surface = this.surfaces.get(message.deleteSurface.surfaceId);
      if (!surface || surface.deleted) return false;
      this.surfaces.set(surface.surfaceId, { ...surface, deleted: true, revision: surface.revision + 1 });
      return true;
    }
    return false;
  }

  /** A surface updated before it was created (a lost createSurface) is created implicitly. */
  private ensure(surfaceId: string): SurfaceState {
    let surface = this.surfaces.get(surfaceId);
    if (!surface) {
      surface = { surfaceId, catalogId: WORKBENCH_CATALOG_ID, components: {}, dataModel: {}, deleted: false, revision: 0 };
      this.surfaces.set(surfaceId, surface);
    }
    return surface;
  }
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isComponent(value: unknown): value is A2uiComponent {
  return isObject(value) && typeof value.id === "string" && value.id !== "" && typeof value.component === "string";
}

const KINDS = ["createSurface", "updateComponents", "updateDataModel", "deleteSurface"] as const;

/** Validates the envelope: exactly one known operation with a surfaceId. */
export function asMessage(raw: unknown): A2uiMessage | undefined {
  if (!isObject(raw)) return undefined;
  const present = KINDS.filter((k) => k in raw);
  if (present.length !== 1) return undefined;
  const kind = present[0]!;
  const body = raw[kind];
  if (!isObject(body) || typeof body.surfaceId !== "string" || body.surfaceId === "") return undefined;
  if (kind === "updateComponents" && !Array.isArray(body.components)) return undefined;
  return raw as unknown as A2uiMessage;
}

export function surfaceIdOf(message: A2uiMessage): string {
  if ("createSurface" in message) return message.createSurface.surfaceId;
  if ("updateComponents" in message) return message.updateComponents.surfaceId;
  if ("updateDataModel" in message) return message.updateDataModel.surfaceId;
  return message.deleteSurface.surfaceId;
}

/** A key independent of property order, so the same message from two routes matches. */
export function stableKey(value: unknown): string {
  return JSON.stringify(sortKeys(value));
}

function sortKeys(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(sortKeys);
  if (isObject(value)) {
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(value).sort()) {
      if (key === "version") continue;
      out[key] = sortKeys(value[key]);
    }
    return out;
  }
  return value;
}
