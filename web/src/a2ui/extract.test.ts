import { describe, expect, it } from "vitest";
import { create, type JsonObject } from "@bufbuild/protobuf";
import { SurfaceSchema } from "@/gen/workbench/v1/components_pb";
import { a2uiFromPart, a2uiFromSurface, a2uiFromToolResponse, isWorkbenchShow, surfaceIdsIn } from "./extract";

const msgs = [
  { version: "v0.9.1", createSurface: { surfaceId: "d1", catalogId: "c" } },
  { version: "v0.9.1", updateComponents: { surfaceId: "d1", components: [{ id: "root", component: "Divider" }] } },
];

describe("A2UI routes", () => {
  it("route a: an application/a2ui+json DataPart", () => {
    expect(a2uiFromPart({ kind: "a2ui", messages: msgs })).toEqual(msgs);
  });

  it("route b: workbench_show structuredContent, with or without a server prefix", () => {
    const response = { content: [{ type: "text", text: "ok" }], structuredContent: { a2ui: { surfaceId: "d1", messages: msgs } } };
    expect(a2uiFromPart({ kind: "data", dataKind: "tool_result", data: { id: "1", name: "workbench_show", response } })).toEqual(msgs);
    expect(isWorkbenchShow("workbench__workbench_show")).toBe(true);
    expect(isWorkbenchShow("not_workbench_shower")).toBe(false);
    // Another tool returning the same shape is not trusted as a surface.
    expect(a2uiFromPart({ kind: "data", dataKind: "tool_result", data: { id: "1", name: "other", response } })).toEqual([]);
    // A call (not its result) carries nothing to draw.
    expect(a2uiFromPart({ kind: "data", dataKind: "tool_call", data: { id: "1", name: "workbench_show", args: {} } })).toEqual([]);
  });

  it("route b: the payload as bare response, as JSON text, and with surfaceId filled in", () => {
    expect(a2uiFromToolResponse({ a2ui: { surfaceId: "d1", messages: msgs } })).toEqual(msgs);
    expect(a2uiFromToolResponse({ content: [{ type: "text", text: JSON.stringify({ a2ui: { surfaceId: "d1", messages: msgs } }) }] })).toEqual(msgs);
    const filled = a2uiFromToolResponse({ a2ui: { surfaceId: "zz", messages: [{ deleteSurface: {} }] } });
    expect(filled).toEqual([{ deleteSurface: { surfaceId: "zz" } }]);
    expect(a2uiFromToolResponse({ content: [{ type: "text", text: "not json" }] })).toEqual([]);
  });

  it("route c: ComponentService surfaces", () => {
    const surface = create(SurfaceSchema, { surfaceId: "d1", sessionId: "s", messages: msgs as unknown as JsonObject[] });
    expect(a2uiFromSurface(surface)).toEqual(msgs);
    expect(surfaceIdsIn(a2uiFromSurface(surface))).toEqual(["d1"]);
  });
});
