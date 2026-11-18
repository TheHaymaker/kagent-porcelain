import { CheckCircle2, ChevronDown, CircleSlash, Clock, Loader2, XCircle } from "lucide-react";
import type { ToolActivity } from "@/conversation/state";
import { argSummary, CATEGORY_LABEL, humanToolName, toolCategory, type ToolCategory } from "@/lib/tools";
import { relativeTime } from "@/lib/format";
import { JsonView } from "@/ui/JsonView";
import { EmptyState } from "@/ui/States";

const STATUS_LABEL = { running: "Running", waiting: "Waiting for approval", done: "Done", failed: "Failed", not_run: "Not run" } as const;

export function StatusIcon({ status }: { status: ToolActivity["status"] }) {
  if (status === "running") return <Loader2 size={16} className="spin tone-info" aria-label="Running" />;
  if (status === "waiting") return <Clock size={16} className="tone-warning" aria-label="Waiting for approval" />;
  if (status === "done") return <CheckCircle2 size={16} className="tone-success" aria-label="Done" />;
  if (status === "failed") return <XCircle size={16} className="tone-danger" aria-label="Failed" />;
  return <CircleSlash size={16} className="muted" aria-label="Not run" />;
}

/** Everything the agent did, with what it read and saved, newest last. */
export function ActivityPanel({ tools }: { tools: ToolActivity[] }) {
  const counts = new Map<ToolCategory, number>();
  for (const t of tools) counts.set(toolCategory(t.name), (counts.get(toolCategory(t.name)) ?? 0) + 1);
  return (
    <div className="activity">
      {tools.length === 0 ? (
        <EmptyState title="Nothing yet">Each step the agent takes — files it reads and saves, things it runs — appears here.</EmptyState>
      ) : (
        <>
          <ul className="activity-summary" aria-label="Summary">
            {[...counts.entries()].map(([cat, n]) => (
              <li key={cat}>
                <strong>{n}</strong> {CATEGORY_LABEL[cat].toLowerCase()}
              </li>
            ))}
          </ul>
          <ol className="activity-list">
            {tools.map((tool) => {
              const summary = argSummary(tool.args);
              const cat = toolCategory(tool.name);
              return (
                <li key={tool.id} className={`activity-item status-${tool.status}`}>
                  <details>
                    <summary>
                      <StatusIcon status={tool.status} />
                      <span className="activity-text">
                        <span className="activity-title">
                          <span className={`cat cat-${cat}`}>{CATEGORY_LABEL[cat]}</span> {humanToolName(tool.name)}
                        </span>
                        {summary && <code className="activity-arg">{summary}</code>}
                      </span>
                      <span className="sr-only">, {STATUS_LABEL[tool.status]}</span>
                      <span className="activity-time">{relativeTime(tool.at)}</span>
                      <ChevronDown size={16} className="chev" aria-hidden />
                    </summary>
                    <div className="activity-detail">
                      <p className="detail-label">Sent</p>
                      <JsonView value={tool.args ?? {}} label="Arguments" />
                      {tool.result !== undefined && (
                        <>
                          <p className="detail-label">Got back</p>
                          <JsonView value={tool.result} label="Result" />
                        </>
                      )}
                    </div>
                  </details>
                </li>
              );
            })}
          </ol>
        </>
      )}
    </div>
  );
}
