import { useState } from "react";
import { CalendarClock, Pause, Play, Plus, Trash2, Zap } from "lucide-react";
import { useNavigate } from "react-router";
import { api } from "@/api/clients";
import { friendlyError } from "@/api/errors";
import { useRpc } from "@/api/useRpc";
import { agentRef, config } from "@/config";
import type { ScheduledRun } from "@/gen/kagent/api/v1/scheduled_runs_pb";
import { fullTime, newRequestId } from "@/lib/format";
import { Badge } from "@/ui/Badge";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Select, TextArea, TextInput } from "@/ui/Field";
import { PageHeader } from "@/ui/PageHeader";
import { EmptyState, ErrorBanner, Skeleton } from "@/ui/States";
import { useToast } from "@/ui/Toast";

const FREQUENCIES = [
  { value: "daily", label: "Every day" },
  { value: "weekdays", label: "Every weekday" },
  { value: "weekly", label: "Every week" },
  { value: "monthly", label: "On the 1st of every month" },
];
const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

/** Plain-language choices become a cron expression; the server owns scheduling. */
export function toCron(frequency: string, time: string, weekday: number): string {
  const [h = "9", m = "0"] = time.split(":");
  const hour = Number(h);
  const minute = Number(m);
  switch (frequency) {
    case "weekdays":
      return `${minute} ${hour} * * 1-5`;
    case "weekly":
      return `${minute} ${hour} * * ${weekday}`;
    case "monthly":
      return `${minute} ${hour} 1 * *`;
    default:
      return `${minute} ${hour} * * *`;
  }
}

export function describeCron(cron: string): string {
  const parts = cron.trim().split(/\s+/);
  if (parts.length !== 5) return cron;
  const [minute, hour, dom, , dow] = parts as [string, string, string, string, string];
  if (!/^\d+$/.test(minute) || !/^\d+$/.test(hour)) return cron;
  const at = `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}`;
  if (dom === "1" && dow === "*") return `On the 1st of every month at ${at}`;
  if (dow === "1-5") return `Every weekday at ${at}`;
  if (/^\d$/.test(dow)) return `Every ${DAYS[Number(dow)]} at ${at}`;
  if (dom === "*" && dow === "*") return `Every day at ${at}`;
  return cron;
}

export function SchedulesPage() {
  const runs = useRpc(async (signal) => (await api.schedules.listScheduledRuns({}, { signal })).scheduledRuns, []);
  const [open, setOpen] = useState(false);
  return (
    <div className="page">
      <PageHeader
        title="Schedules"
        description="Ask the builder to do something regularly — a weekly report, a monthly clean-up."
        actions={
          <Button variant="primary" icon={<Plus size={16} />} onClick={() => setOpen(true)}>
            New schedule
          </Button>
        }
      />
      {runs.error && <ErrorBanner message={runs.error} onRetry={runs.reload} />}
      {runs.loading && !runs.data && <Skeleton lines={4} />}
      {runs.data && runs.data.length === 0 && (
        <EmptyState icon={<CalendarClock size={22} />} title="No schedules yet">
          Create one to have the builder run a request for you on a regular rhythm.
        </EmptyState>
      )}
      <ul className="stack">
        {runs.data?.map((run) => (
          <li key={run.id}>
            <ScheduleRow run={run} onChange={runs.reload} />
          </li>
        ))}
      </ul>
      <CreateSchedule open={open} onClose={() => setOpen(false)} onCreated={runs.reload} />
    </div>
  );
}

function ScheduleRow({ run, onChange }: { run: ScheduledRun; onChange: () => void }) {
  const [busy, setBusy] = useState<string>();
  const toast = useToast();
  const navigate = useNavigate();
  const paused = run.config?.paused ?? false;
  const act = async (what: string, fn: () => Promise<unknown>) => {
    setBusy(what);
    try {
      await fn();
      onChange();
    } catch (err) {
      toast(friendlyError(err), "danger");
    } finally {
      setBusy(undefined);
    }
  };
  return (
    <article className="card schedule">
      <div className="schedule-main">
        <div className="schedule-head">
          <h2>{run.config?.name || "Untitled schedule"}</h2>
          <Badge tone={paused ? "neutral" : "success"}>{paused ? "Paused" : "Active"}</Badge>
        </div>
        <p className="schedule-prompt">{run.config?.prompt}</p>
        <p className="muted small">
          {describeCron(run.config?.schedule ?? "")}
          {run.config?.timeZone && ` (${run.config.timeZone})`}
          {!paused && run.nextExecutionTime && ` · next run ${fullTime(run.nextExecutionTime)}`}
        </p>
      </div>
      <div className="schedule-actions">
        <Button
          size="sm"
          icon={paused ? <Play size={16} /> : <Pause size={16} />}
          busy={busy === "pause"}
          onClick={() =>
            void act("pause", () =>
              api.schedules.updateScheduledRun({
                scheduledRunId: run.id,
                etag: run.etag,
                config: { ...run.config!, paused: !paused },
              }),
            )
          }
        >
          {paused ? "Resume" : "Pause"}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          icon={<Zap size={16} />}
          busy={busy === "run"}
          onClick={() =>
            void act("run", async () => {
              const r = await api.schedules.triggerScheduledRun({ scheduledRunId: run.id, requestId: newRequestId() });
              toast("Started a run now.", "success");
              if (r.execution?.sessionId) navigate(`/c/${r.execution.sessionId}`);
            })
          }
        >
          Run now
        </Button>
        <Button
          size="sm"
          variant="ghost"
          icon={<Trash2 size={16} />}
          busy={busy === "delete"}
          aria-label={`Delete ${run.config?.name ?? "schedule"}`}
          onClick={() => {
            if (window.confirm(`Delete “${run.config?.name}”? It won't run again.`)) void act("delete", () => api.schedules.deleteScheduledRun({ scheduledRunId: run.id }));
          }}
        >
          Delete
        </Button>
      </div>
    </article>
  );
}

function CreateSchedule({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState("");
  const [prompt, setPrompt] = useState("");
  const [frequency, setFrequency] = useState("weekly");
  const [time, setTime] = useState("09:00");
  const [weekday, setWeekday] = useState(1);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;

  const create = async () => {
    if (!prompt.trim()) {
      setError("Tell the builder what to do each time.");
      return;
    }
    setBusy(true);
    setError(undefined);
    try {
      await api.schedules.createScheduledRun({
        agent: agentRef(config().defaultAgent),
        requestId: newRequestId(),
        config: { name: name.trim() || prompt.trim().slice(0, 60), schedule: toCron(frequency, time, weekday), timeZone, prompt: prompt.trim(), paused: false },
      });
      onCreated();
      onClose();
      setName("");
      setPrompt("");
    } catch (err) {
      setError(friendlyError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="New schedule"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" busy={busy} onClick={() => void create()}>
            Create schedule
          </Button>
        </>
      }
    >
      {error && <ErrorBanner message={error} />}
      <Field label="What should the builder do each time?">
        {(id) => <TextArea id={id} rows={3} value={prompt} onChange={(e) => setPrompt(e.target.value)} placeholder="e.g. Email me a list of invoices that are more than 7 days overdue." />}
      </Field>
      <Field label="Name (optional)">{(id) => <TextInput id={id} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
      <div className="form-row">
        <Field label="How often">
          {(id) => (
            <Select id={id} value={frequency} onChange={(e) => setFrequency(e.target.value)}>
              {FREQUENCIES.map((f) => (
                <option key={f.value} value={f.value}>
                  {f.label}
                </option>
              ))}
            </Select>
          )}
        </Field>
        {frequency === "weekly" && (
          <Field label="On">
            {(id) => (
              <Select id={id} value={weekday} onChange={(e) => setWeekday(Number(e.target.value))}>
                {DAYS.map((d, i) => (
                  <option key={d} value={i}>
                    {d}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}
        <Field label="At" hint={timeZone}>
          {(id, hintId) => <TextInput id={id} type="time" value={time} onChange={(e) => setTime(e.target.value)} aria-describedby={hintId} />}
        </Field>
      </div>
    </Dialog>
  );
}
