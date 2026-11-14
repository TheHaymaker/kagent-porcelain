import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { api } from "@/api/clients";
import { useRpc } from "@/api/useRpc";
import { useAuth } from "@/auth/AuthContext";
import { humanGroup } from "@/approvals/logic";
import { LimitSubject, UsageGroupBy, type UsageLine } from "@/gen/workbench/v1/usage_pb";
import { money } from "@/lib/format";
import { PageHeader } from "@/ui/PageHeader";
import { Meter } from "@/ui/Meter";
import { ErrorBanner, Skeleton } from "@/ui/States";

function since(days: number) {
  return timestampFromDate(new Date(Date.now() - days * 86400_000));
}

export function UsagePage() {
  const { me } = useAuth();
  const daily = useRpc(async (signal) => api.usage.getUsageSummary({ mine: true, groupBy: UsageGroupBy.DAY, since: since(14) }, { signal }), []);
  const byApp = useRpc(async (signal) => api.usage.getUsageSummary({ mine: true, groupBy: UsageGroupBy.APP, since: since(30) }, { signal }), []);
  const byModel = useRpc(async (signal) => api.usage.getUsageSummary({ mine: true, groupBy: UsageGroupBy.MODEL, since: since(30) }, { signal }), []);
  const limits = useRpc(async (signal) => (await api.usage.listLimits({}, { signal })).limits, []);

  return (
    <div className="page">
      <PageHeader title="Usage" description="What your requests have cost this month, and the limits that apply to you." />
      <section className="section" aria-labelledby="limits-title">
        <h2 id="limits-title">Limits</h2>
        {limits.error && <ErrorBanner message={limits.error} onRetry={limits.reload} />}
        {limits.loading && !limits.data && <Skeleton lines={2} />}
        <div className="stat-grid">
          {limits.data?.map((l) => {
            const spent = Number(l.spentCentsMtd);
            const cap = Number(l.monthlyCents);
            const label = l.subject === LimitSubject.PERSON ? (l.subjectId === me?.person?.id ? "Your monthly limit" : "Personal limit") : `${humanGroup(l.subjectId)} limit`;
            return (
              <div key={`${l.subject}-${l.subjectId}`} className="card stat">
                <p className="stat-label">{label}</p>
                <p className="stat-value">
                  {money(spent)} <span className="muted stat-of">of {money(cap)}</span>
                </p>
                <Meter value={spent} max={cap} label={`${label}: ${money(spent)} of ${money(cap)}`} />
                <p className="muted small">{cap > 0 ? `${Math.round((spent / cap) * 100)}% used · ${money(Math.max(0, cap - spent))} left` : "No limit"}</p>
              </div>
            );
          })}
        </div>
      </section>

      <section className="section" aria-labelledby="daily-title">
        <h2 id="daily-title">Spend per day, last 14 days</h2>
        {daily.error && <ErrorBanner message={daily.error} onRetry={daily.reload} />}
        {daily.data && <DailyBars lines={daily.data.lines} total={Number(daily.data.totalCostCents)} />}
      </section>

      <div className="detail-grid">
        <section className="section" aria-labelledby="byapp-title">
          <h2 id="byapp-title">By app, this month</h2>
          {byApp.data && <Breakdown lines={byApp.data.lines} />}
        </section>
        <section className="section" aria-labelledby="bymodel-title">
          <h2 id="bymodel-title">By what was used</h2>
          {byModel.data && <Breakdown lines={byModel.data.lines} />}
        </section>
      </div>
    </div>
  );
}

function DailyBars({ lines, total }: { lines: UsageLine[]; total: number }) {
  const max = Math.max(1, ...lines.map((l) => Number(l.costCents)));
  const day = (key: string) => new Date(`${key}T12:00:00`).toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
  return (
    <div className="card chart-card">
      <p className="chart-total">
        {money(total)} <span className="muted small">in 14 days</span>
      </p>
      <div className="bars" role="img" aria-label={`Daily spend over 14 days, ${money(total)} in total. A table follows.`}>
        {lines.map((l) => {
          const cents = Number(l.costCents);
          return (
            <div key={l.key} className="bar-col" tabIndex={0} aria-label={`${day(l.key)}: ${money(cents)}`}>
              <span className="bar-tip" role="tooltip">
                {day(l.key)}
                <strong>{money(cents)}</strong>
              </span>
              <div className="bar" style={{ height: `${(cents / max) * 100}%` }} />
            </div>
          );
        })}
      </div>
      <div className="bars-axis" aria-hidden>
        <span>{lines[0] ? day(lines[0].key) : ""}</span>
        <span>{lines.at(-1) ? day(lines.at(-1)!.key) : ""}</span>
      </div>
      <details className="table-toggle">
        <summary>Show as a table</summary>
        <table className="table">
          <thead>
            <tr>
              <th scope="col">Day</th>
              <th scope="col">Spend</th>
            </tr>
          </thead>
          <tbody>
            {lines.map((l) => (
              <tr key={l.key}>
                <td>{day(l.key)}</td>
                <td>{money(l.costCents)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  );
}

function Breakdown({ lines }: { lines: UsageLine[] }) {
  const max = Math.max(1, ...lines.map((l) => Number(l.costCents)));
  if (lines.length === 0) return <p className="muted">Nothing this month.</p>;
  return (
    <ul className="breakdown card">
      {lines.map((l) => (
        <li key={l.key}>
          <span className="breakdown-label">{l.key}</span>
          <span className="breakdown-track" aria-hidden>
            <span className="breakdown-fill" style={{ width: `${(Number(l.costCents) / max) * 100}%` }} />
          </span>
          <span className="breakdown-value">{money(l.costCents)}</span>
        </li>
      ))}
    </ul>
  );
}
