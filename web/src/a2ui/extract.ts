/**
 * Finds A2UI messages in what the server sends, by each of the three routes.
 */
import type { JsonObject } from "@bufbuild/protobuf";
import type { ChatPart } from "@/api/chat/types";
import type { Surface } from "@/gen/workbench/v1/components_pb";
import { asMessage, surfaceIdOf } from "./store";

/** The MCP tool agents call to show components. MCP tool names may carry a server prefix. */
export const WORKBENCH_SHOW_TOOL = "workbench_show";

export function isWorkbenchShow(name: unknown): boolean {
  return typeof name === "string" && (name === WORKBENCH_SHOW_TOOL || /(^|[_.:/-])workbench_show$/.test(name));
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Route b: the structured result of a `workbench_show` call,
 * `{"a2ui": {"surfaceId": "...", "messages": [...]}}`.
 *
 * The result reaches the A2A stream as a `function_response` DataPart whose
 * `response` is the MCP CallToolResult. It is looked for in
 * `response.structuredContent`, then `response` itself, then `response.result`,
 * then any text content that parses as JSON — runtimes differ in how much of the
 * MCP envelope they keep.
 */
export function a2uiFromToolResponse(response: unknown): unknown[] {
  const candidates: unknown[] = [];
  if (isObject(response)) {
    candidates.push(response.structuredContent, response, response.result);
    if (Array.isArray(response.content)) {
      for (const item of response.content) {
        if (isObject(item) && item.type === "text" && typeof item.text === "string") {
          try {
            candidates.push(JSON.parse(item.text));
          } catch {
            /* not JSON */
          }
        }
      }
    }
  } else if (typeof response === "string") {
    try {
      candidates.push(JSON.parse(response));
    } catch {
      /* not JSON */
    }
  }
  for (const candidate of candidates) {
    if (!isObject(candidate) || !isObject(candidate.a2ui)) continue;
    const { surfaceId, messages } = candidate.a2ui;
    if (!Array.isArray(messages)) continue;
    return messages.map((m) => withSurfaceId(m, typeof surfaceId === "string" ? surfaceId : undefined));
  }
  return [];
}

/** Fills in a surfaceId omitted from a message when the envelope names it. */
function withSurfaceId(message: unknown, surfaceId: string | undefined): unknown {
  if (!surfaceId || !isObject(message)) return message;
  for (const key of ["createSurface", "updateComponents", "updateDataModel", "deleteSurface"]) {
    const body = message[key];
    if (isObject(body) && body.surfaceId === undefined) return { ...message, [key]: { ...body, surfaceId } };
  }
  return message;
}

/** Routes a and b, from one transcript part. */
export function a2uiFromPart(part: ChatPart): unknown[] {
  if (part.kind === "a2ui") return part.messages;
  if (part.kind === "data" && part.dataKind === "tool_result" && isWorkbenchShow(part.data.name)) {
    return a2uiFromToolResponse(part.data.response);
  }
  return [];
}

/** Route c: a stored surface from ComponentService.ListSurfaces. */
export function a2uiFromSurface(surface: Surface): unknown[] {
  return surface.messages.map((m: JsonObject) => withSurfaceId(m, surface.surfaceId));
}

/** The surfaceIds a list of messages refers to, in first-seen order. */
export function surfaceIdsIn(messages: readonly unknown[]): string[] {
  const ids: string[] = [];
  for (const raw of messages) {
    const message = asMessage(raw);
    if (!message) continue;
    const id = surfaceIdOf(message);
    if (!ids.includes(id)) ids.push(id);
  }
  return ids;
}
