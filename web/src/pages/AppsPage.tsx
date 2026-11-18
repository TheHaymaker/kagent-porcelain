import { useState } from "react";
import { Link } from "react-router";
import { AppWindow, ExternalLink, Search } from "lucide-react";
import { api } from "@/api/clients";
import { useRpc } from "@/api/useRpc";
import { EnvironmentName, type App } from "@/gen/workbench/v1/apps_pb";
import { APP_STATUS_LABEL, APP_STATUS_TONE, money, personName, relativeTime, safeHttpsUrl } from "@/lib/format";
import { Badge } from "@/ui/Badge";
import { PageHeader } from "@/ui/PageHeader";
import { EmptyState, ErrorBanner, Skeleton } from "@/ui/States";
import { Tabs } from "@/ui/Tabs";

export function AppsPage() {
  const [scope, setScope] = useState<"mine" | "all">("mine");
  const [query, setQuery] = useState("");
  const apps = useRpc(
    async (signal) => (await api.apps.listApps({ mine: scope === "mine", query: query.trim() }, { signal })).apps,
    [scope, query],
  );
  return (
    <div className="page">
      <PageHeader title="Apps" description="Everything Workbench has built, where it runs, and what it costs." />
      <div className="toolbar">
        <Tabs
          label="Which apps"
          idPrefix="apps"
          value={scope}
          onChange={setScope}
          items={[
            { key: "mine", label: "My apps" },
            { key: "all", label: "All apps" },
          ]}
        />
        <div className="search">
          <Search size={16} aria-hidden />
          <label htmlFor="app-search" className="sr-only">
            Search apps
          </label>
          <input id="app-search" className="input" type="search" placeholder="Search apps" value={query} onChange={(e) => setQuery(e.target.value)} />
        </div>
      </div>
      <div id="apps-panel" role="tabpanel" aria-labelledby={`apps-tab-${scope}`}>
        {apps.error && <ErrorBanner message={apps.error} onRetry={apps.reload} />}
        {apps.loading && !apps.data && <Skeleton lines={6} />}
        {apps.data && apps.data.length === 0 && (
          <EmptyState icon={<AppWindow size={22} />} title={query ? "No apps match" : "No apps yet"}>
            {query ? "Try a different search." : "Start a conversation on the home page to build one."}
          </EmptyState>
        )}
        <ul className="card-grid">
          {apps.data?.map((app) => (
            <li key={app.id}>
              <AppTile app={app} />
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

function AppTile({ app }: { app: App }) {
  const preview = safeHttpsUrl(app.environments.find((e) => e.name === EnvironmentName.PREVIEW)?.url);
  const production = safeHttpsUrl(app.environments.find((e) => e.name === EnvironmentName.PRODUCTION)?.url);
  return (
    <article className="card app-tile">
      <div className="app-tile-head">
        <span className="app-icon" aria-hidden>
          {app.name.slice(0, 1).toUpperCase()}
        </span>
        <div className="app-tile-title">
          <h2>
            <Link to={`/apps/${app.id}`} className="stretched">
              {app.name}
            </Link>
          </h2>
          <span className="muted small">
            {personName(app.owner)} · updated {relativeTime(app.updatedAt)}
          </span>
        </div>
        <Badge tone={APP_STATUS_TONE[app.status]}>{APP_STATUS_LABEL[app.status]}</Badge>
      </div>
      {app.description && <p className="app-tile-desc">{app.description}</p>}
      <dl className="kv kv-row">
        <div>
          <dt>Cost this month</dt>
          <dd>{money(app.costCentsMtd)}</dd>
        </div>
        {app.pendingApprovalId && (
          <div>
            <dt>Production</dt>
            <dd>
              <Badge tone="info">Waiting for approval</Badge>
            </dd>
          </div>
        )}
      </dl>
      <div className="app-tile-links">
        {preview && (
          <a href={preview} target="_blank" rel="noopener noreferrer" className="lift">
            Preview <ExternalLink size={14} aria-hidden />
            <span className="sr-only"> (opens in a new tab)</span>
          </a>
        )}
        {production && (
          <a href={production} target="_blank" rel="noopener noreferrer" className="lift">
            Production <ExternalLink size={14} aria-hidden />
            <span className="sr-only"> (opens in a new tab)</span>
          </a>
        )}
        {!preview && !production && <span className="muted small">Not deployed yet</span>}
      </div>
    </article>
  );
}
