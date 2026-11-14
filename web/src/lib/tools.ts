/** Plain-language names for what an agent's tool calls did. */

export type ToolCategory = "read" | "saved" | "ran" | "deployed" | "showed" | "asked";

export function toolCategory(name: string): ToolCategory {
  const n = name.toLowerCase();
  if (/workbench_show$/.test(n)) return "showed";
  if (/deploy|promote|release|publish/.test(n)) return "deployed";
  if (/(^|_)(read|get|list|fetch|search|find|view|cat|grep|glob|ls|describe|query)(_|$)/.test(n)) return "read";
  if (/(^|_)(write|save|create|update|put|edit|patch|apply|commit|push|mkdir|delete|remove|set)(_|$)/.test(n)) return "saved";
  if (/ask/.test(n)) return "asked";
  return "ran";
}

export const CATEGORY_LABEL: Record<ToolCategory, string> = {
  read: "Read",
  saved: "Saved",
  ran: "Ran",
  deployed: "Deployed",
  showed: "Shared",
  asked: "Asked",
};

/** "write_file" → "Write file"; strips an MCP server prefix ("github__create_pr"). */
export function humanToolName(name: string): string {
  if (/workbench_show$/.test(name)) return "Progress update";
  const base = name.includes("__") ? name.split("__").pop() ?? name : name;
  const words = base.replace(/[_\-.]+/g, " ").replace(/([a-z])([A-Z])/g, "$1 $2").trim().toLowerCase();
  return words ? words[0]!.toUpperCase() + words.slice(1) : name;
}

/** The single most telling argument (a path, a URL, a name), for a one-line summary. */
export function argSummary(args: unknown): string | undefined {
  if (!args || typeof args !== "object" || Array.isArray(args)) return undefined;
  const record = args as Record<string, unknown>;
  for (const key of ["path", "file", "filename", "file_path", "url", "name", "app", "app_name", "query", "command", "revision", "bucket"]) {
    const value = record[key];
    if (typeof value === "string" && value !== "") return value.length > 80 ? `${value.slice(0, 77)}…` : value;
  }
  return undefined;
}
