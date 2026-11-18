import type { ToolActivity } from "./state";

/** Consecutive calls of one tool on one target, shown as a single row. */
export interface ToolRun {
  /** The newest call: its status and arguments are what the row shows. */
  latest: ToolActivity;
  count: number;
  /** Earlier calls in the run that failed (the latest is shown by its status). */
  earlierFailures: number;
}

/** The target a call acts on, when it names one (a file path or a name). */
export function targetOf(args: unknown): string | undefined {
  if (!args || typeof args !== "object") return undefined;
  const a = args as Record<string, unknown>;
  const v = a.path ?? a.name;
  return typeof v === "string" ? v : undefined;
}

/**
 * Folds runs of the same tool on the same target into one row, so an agent
 * polling a build ("get status" twenty times) reads as one line with a count
 * and the newest status.
 */
export function collapseRuns(tools: readonly ToolActivity[]): ToolRun[] {
  const runs: ToolRun[] = [];
  for (const t of tools) {
    const last = runs[runs.length - 1];
    if (last && last.latest.name === t.name && targetOf(last.latest.args) === targetOf(t.args)) {
      if (last.latest.status === "failed") last.earlierFailures++;
      last.latest = t;
      last.count++;
    } else {
      runs.push({ latest: t, count: 1, earlierFailures: 0 });
    }
  }
  return runs;
}

/** At most `max` rows: the newest ones, and how many earlier rows were left out. */
export function newestRuns(runs: readonly ToolRun[], max: number): { shown: ToolRun[]; hidden: number } {
  if (runs.length <= max) return { shown: [...runs], hidden: 0 };
  return { shown: runs.slice(runs.length - max), hidden: runs.length - max };
}
