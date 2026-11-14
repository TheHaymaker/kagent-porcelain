import { useState } from "react";
import { Link } from "react-router";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { History } from "lucide-react";
import { api } from "@/api/clients";
import { useRpc } from "@/api/useRpc";
import { fullTime, personName } from "@/lib/format";
import { Field, Select } from "@/ui/Field";
import { PageHeader } from "@/ui/PageHeader";
import { EmptyState, ErrorBanner, Skeleton } from "@/ui/States";
import { JsonView } from "@/ui/JsonView";

const TYPES = [
  { value: "", label: "Everything" },
  { value: "approval.", label: "Approvals" },
  { value: "app.", label: "Apps and deploys" },
  { value: "conversation.", label: "Conversations" },
  { value: "schedule.", label: "Schedules" },
];
const RANGES = [
  { value: "1", label: "Last 24 hours" },
  { value: "7", label: "Last 7 days" },
  { value: "30", label: "Last 30 days" },
  { value: "", label: "Any time" },
];

export function AuditPage() {
  const [type, setType] = useState("");
  const [range, setRange] = useState("7");
  const [app, setApp] = useState("");
  const apps = useRpc(async (signal) => (await api.apps.listApps({ mine: true }, { signal })).apps, []);
  const events = useRpc(
    async (signal) =>
      (
        await api.audit.listAuditEvents(
          {
            typePrefix: type,
            appId: app,
            since: range ? timestampFromDate(new Date(Date.now() - Number(range) * 86400_000)) : undefined,
            page: { pageSize: 100 },
          },
          { signal },
        )
      ).events,
    [type, range, app],
  );
  return (
    <div className="page">
      <PageHeader title="Activity" description="A permanent record of what was asked, approved, refused and done in your conversations." />
      <div className="filters" role="search" aria-label="Filter activity">
        <Field label="What">
          {(id) => (
            <Select id={id} value={type} onChange={(e) => setType(e.target.value)}>
              {TYPES.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="When">
          {(id) => (
            <Select id={id} value={range} onChange={(e) => setRange(e.target.value)}>
              {RANGES.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="App">
          {(id) => (
            <Select id={id} value={app} onChange={(e) => setApp(e.target.value)}>
              <option value="">All apps</option>
              {apps.data?.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
      </div>
      {events.error && <ErrorBanner message={events.error} onRetry={events.reload} />}
      {events.loading && !events.data && <Skeleton lines={6} />}
      {events.data && events.data.length === 0 && (
        <EmptyState icon={<History size={22} />} title="No activity matches">
          Try a wider time range.
        </EmptyState>
      )}
      <ol className="audit-list">
        {events.data?.map((e) => (
          <li key={e.id} className="audit-item">
            <span className="audit-dot" aria-hidden />
            <div className="audit-body">
              <p className="audit-summary">{e.summary}</p>
              <p className="muted small">
                {personName(e.actor)}
                {e.onBehalfOf && ` for ${personName(e.onBehalfOf)}`} · {fullTime(e.time)}
                {e.surface && ` · via ${e.surface === "web" ? "Workbench" : e.surface === "teams" ? "Teams" : e.surface}`}
              </p>
              <p className="audit-links small">
                <code className="audit-type">{e.type}</code>
                {e.sessionId && <Link to={`/c/${e.sessionId}`}>Conversation</Link>}
                {e.approvalId && <Link to={`/approvals/${e.approvalId}`}>Approval</Link>}
                {e.appId && <Link to={`/apps/${e.appId}`}>App</Link>}
              </p>
              {e.detail && Object.keys(e.detail).length > 0 && (
                <details>
                  <summary className="small">Details</summary>
                  <JsonView value={e.detail} />
                </details>
              )}
            </div>
          </li>
        ))}
      </ol>
    </div>
  );
}
