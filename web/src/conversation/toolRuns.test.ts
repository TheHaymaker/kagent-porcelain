import { describe, expect, it } from "vitest";
import type { ToolActivity } from "./state";
import { collapseRuns, newestRuns } from "./toolRuns";

let n = 0;
const call = (name: string, status: ToolActivity["status"] = "done", args: unknown = {}): ToolActivity => ({
  id: `t${++n}`,
  name,
  args,
  status,
  at: "",
});

describe("collapseRuns", () => {
  it("folds a polling loop into one row with the newest status", () => {
    const runs = collapseRuns([
      call("build_start_image"),
      ...Array.from({ length: 21 }, () => call("build_get_status")),
      call("build_get_status", "running"),
      call("cloudrun_deploy_preview", "waiting"),
    ]);
    expect(runs.map((r) => [r.latest.name, r.count, r.latest.status])).toEqual([
      ["build_start_image", 1, "done"],
      ["build_get_status", 22, "running"],
      ["cloudrun_deploy_preview", 1, "waiting"],
    ]);
  });

  it("keeps reads of different files apart, and counts earlier failures", () => {
    const runs = collapseRuns([
      call("gitlab_get_file", "done", { path: "app/main.py" }),
      call("gitlab_get_file", "done", { path: "app/store.py" }),
      call("build_get_status", "failed"),
      call("build_get_status", "done"),
    ]);
    expect(runs.map((r) => r.count)).toEqual([1, 1, 2]);
    expect(runs[2]?.earlierFailures).toBe(1);
  });

  it("does not fold calls that are not next to each other", () => {
    expect(collapseRuns([call("a"), call("b"), call("a")]).length).toBe(3);
  });
});

describe("newestRuns", () => {
  it("keeps the newest rows and counts the rest", () => {
    const runs = collapseRuns(["a", "b", "c", "d", "e", "f"].map((x) => call(x)));
    const { shown, hidden } = newestRuns(runs, 4);
    expect(shown.map((r) => r.latest.name)).toEqual(["c", "d", "e", "f"]);
    expect(hidden).toBe(2);
    expect(newestRuns(runs.slice(0, 3), 4).hidden).toBe(0);
  });
});
