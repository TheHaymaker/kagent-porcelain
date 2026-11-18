import { describe, expect, it, vi } from "vitest";
import { getAt, setAt } from "./pointer";
import { SurfaceStore, asMessage, stableKey } from "./store";

const create = (surfaceId = "s1") => ({ version: "v0.9.1", createSurface: { surfaceId, catalogId: "c" } });
const components = (surfaceId: string, list: object[]) => ({ version: "v0.9.1", updateComponents: { surfaceId, components: list } });
const data = (surfaceId: string, path: string | undefined, value: unknown) => ({ version: "v0.9.1", updateDataModel: { surfaceId, path, value } });

describe("JSON pointer", () => {
  it("reads nested values, arrays and escaped tokens", () => {
    const doc = { a: { "b/c": [1, { "d~e": "x" }] } };
    expect(getAt(doc, "/a/b~1c/1/d~0e")).toBe("x");
    expect(getAt(doc, "/a/missing")).toBeUndefined();
    expect(getAt(doc, "")).toBe(doc);
    expect(getAt(doc, "nope")).toBeUndefined();
  });
  it("refuses prototype keys", () => {
    expect(getAt({}, "/__proto__")).toBeUndefined();
    const out = setAt({}, "/__proto__/polluted", 1) as Record<string, unknown>;
    expect(({} as Record<string, unknown>).polluted).toBeUndefined();
    expect(out).toEqual({});
  });
  it("writes immutably, creating parents, and deletes on undefined", () => {
    const before = { build: { percent: 10 } };
    const after = setAt(before, "/build/percent", 80);
    expect(after).toEqual({ build: { percent: 80 } });
    expect(before.build.percent).toBe(10);
    expect(setAt({}, "/x/y", 1)).toEqual({ x: { y: 1 } });
    expect(setAt({ a: 1, b: 2 }, "/a", undefined)).toEqual({ b: 2 });
    expect(setAt({ a: 1 }, "/", { z: 1 })).toEqual({ z: 1 });
  });
});

describe("SurfaceStore", () => {
  it("applies createSurface, updateComponents, updateDataModel and deleteSurface", () => {
    const store = new SurfaceStore();
    store.apply([create(), components("s1", [{ id: "root", component: "Text", text: { path: "/msg" } }]), data("s1", "/msg", "hi")]);
    const s = store.get("s1")!;
    expect(s.components.root?.component).toBe("Text");
    expect(getAt(s.dataModel, "/msg")).toBe("hi");
    store.apply([{ deleteSurface: { surfaceId: "s1" } }]);
    expect(store.get("s1")!.deleted).toBe(true);
  });

  it("upserts components by id", () => {
    const store = new SurfaceStore();
    store.apply([create(), components("s1", [{ id: "a", component: "Text", text: "one" }])]);
    store.apply([components("s1", [{ id: "a", component: "Text", text: "two" }, { id: "b", component: "Divider" }])]);
    expect(store.get("s1")!.components.a?.text).toBe("two");
    expect(Object.keys(store.get("s1")!.components)).toEqual(["a", "b"]);
  });

  it("dedupes the same messages arriving by a second route", () => {
    const store = new SurfaceStore();
    const listener = vi.fn();
    store.subscribe(listener);
    const stream = [create(), components("s1", [{ id: "root", component: "Progress", steps: [] }]), data("s1", "/p", 20), data("s1", "/p", 80)];
    store.apply(stream);
    expect(listener).toHaveBeenCalledTimes(1);
    // ListSurfaces replays the whole history later (keys in another order): nothing changes, no regression to 20.
    const replay = stream.map((m) => JSON.parse(JSON.stringify(m, Object.keys(m).reverse())));
    store.apply(replay);
    expect(listener).toHaveBeenCalledTimes(1);
    expect(getAt(store.get("s1")!.dataModel, "/p")).toBe(80);
  });

  it("does not reset a live surface when createSurface repeats with new content", () => {
    const store = new SurfaceStore();
    store.apply([create(), components("s1", [{ id: "root", component: "Divider" }])]);
    store.apply([{ version: "v0.9", createSurface: { surfaceId: "s1", catalogId: "other" } }]);
    expect(store.get("s1")!.components.root).toBeDefined();
  });

  it("creates a surface implicitly when updates arrive first, and keeps surfaces apart", () => {
    const store = new SurfaceStore();
    expect(store.apply([components("late", [{ id: "root", component: "Divider" }]), create("other")])).toEqual(["late", "other"]);
    expect(store.ids().sort()).toEqual(["late", "other"]);
  });

  it("skips malformed messages", () => {
    const store = new SurfaceStore();
    expect(store.apply([null, 3, "x", {}, { createSurface: {} }, { createSurface: { surfaceId: "a" }, deleteSurface: { surfaceId: "a" } }, { updateComponents: { surfaceId: "a" } }])).toEqual([]);
    expect(asMessage({ updateDataModel: { surfaceId: "a" } })).toBeDefined();
  });

  it("lists approval ids referenced by live ApprovalCards", () => {
    const store = new SurfaceStore();
    store.apply([create(), components("s1", [{ id: "root", component: "ApprovalCard", approvalId: "apr_9" }])]);
    expect([...store.approvalIds()]).toEqual(["apr_9"]);
  });

  it("stableKey ignores key order and the version field", () => {
    expect(stableKey({ a: 1, b: { c: 2, d: 3 }, version: "v0.9.1" })).toBe(stableKey({ b: { d: 3, c: 2 }, a: 1 }));
  });
});
