import { useState } from "react";
import { Link, useParams } from "react-router";
import { ArrowLeft, ExternalLink, GitBranch, MessageSquare, Rocket } from "lucide-react";
import { api } from "@/api/clients";
import { friendlyError } from "@/api/errors";
import { useRpc } from "@/api/useRpc";
import { ApprovalPanel } from "@/approvals/ApprovalPanel";
import { AppStatus, EnvironmentName } from "@/gen/workbench/v1/apps_pb";
import { APP_STATUS_LABEL, APP_STATUS_TONE, ENV_LABEL, fullTime, money, personName, relativeTime, safeHttpsUrl } from "@/lib/format";
import { Badge } from "@/ui/Badge";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, TextArea } from "@/ui/Field";
import { EmptyState, ErrorBanner, Loading } from "@/ui/States";
import { useToast } from "@/ui/Toast";

export function AppDetailPage() {
  const { appId = "" } = useParams();
  const toast = useToast();
  const app = useRpc(async (signal) => (await api.apps.getApp({ id: appId }, { signal })).app, [appId], { pollMs: 15000 });
  const deployments = useRpc(async (signal) => (await api.apps.listDeployments({ appId }, { signal })).deployments, [appId]);
  const [askOpen, setAskOpen] = useState(false);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  if (app.error && !app.data) return <div className="page"><ErrorBanner message={app.error} onRetry={app.reload} /></div>;
  if (!app.data) return <div className="page"><Loading label="Loading app" /></div>;
  const a = app.data;
  const preview = a.environments.find((e) => e.name === EnvironmentName.PREVIEW);
  const canRequest = !!preview && !a.pendingApprovalId && a.status !== AppStatus.ARCHIVED;

  const request = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await api.apps.requestPromotion({ appId: a.id, note: note.trim() });
      setAskOpen(false);
      setNote("");
      toast("Production request sent to the approvers.", "success");
      app.reload();
    } catch (err) {
      setError(friendlyError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="page app-detail">
      <Link to="/apps" className="back-link">
        <ArrowLeft size={16} aria-hidden /> All apps
      </Link>
      <header className="page-header">
        <div className="app-detail-title">
          <span className="app-icon app-icon-lg" aria-hidden>
            {a.name.slice(0, 1).toUpperCase()}
          </span>
          <div>
            <h1>{a.name}</h1>
            <p className="page-desc">{a.description}</p>
          </div>
        </div>
        <div className="page-actions">
          <Badge tone={APP_STATUS_TONE[a.status]}>{APP_STATUS_LABEL[a.status]}</Badge>
          {canRequest && (
            <Button variant="primary" icon={<Rocket size={16} />} onClick={() => setAskOpen(true)}>
              Request production
            </Button>
          )}
        </div>
      </header>

      {a.pendingApprovalId && (
        <section aria-labelledby="pending-title" className="section">
          <h2 id="pending-title">Production request</h2>
          <ApprovalPanel approvalId={a.pendingApprovalId} variant="detail" onSettled={() => app.reload()} />
        </section>
      )}

      <div className="detail-grid">
        <section aria-labelledby="envs-title" className="section">
          <h2 id="envs-title">Where it runs</h2>
          {a.environments.length === 0 && <EmptyState title="Not deployed yet">The builder deploys a preview once the app is ready.</EmptyState>}
          <ul className="env-list">
            {a.environments.map((env) => {
              const url = safeHttpsUrl(env.url);
              return (
                <li key={env.name} className="card env">
                  <div className="env-head">
                    <Badge tone={env.name === EnvironmentName.PRODUCTION ? "success" : "info"}>{ENV_LABEL[env.name]}</Badge>
                    <span className="muted small">updated {relativeTime(env.updatedAt)}</span>
                  </div>
                  {url && (
                    <a href={url} target="_blank" rel="noopener noreferrer" className="env-url">
                      {url.replace(/^https:\/\//, "").replace(/\/$/, "")} <ExternalLink size={14} aria-hidden />
                      <span className="sr-only"> (opens in a new tab)</span>
                    </a>
                  )}
                  <p className="muted small">Version {env.revision}</p>
                </li>
              );
            })}
          </ul>
        </section>
        <section aria-labelledby="about-title" className="section">
          <h2 id="about-title">About</h2>
          <dl className="kv">
            <div>
              <dt>Owner</dt>
              <dd>{personName(a.owner)}</dd>
            </div>
            <div>
              <dt>Cost this month</dt>
              <dd>{money(a.costCentsMtd)}</dd>
            </div>
            <div>
              <dt>Started from</dt>
              <dd>{a.scaffold || "—"}</dd>
            </div>
            <div>
              <dt>Created</dt>
              <dd>{fullTime(a.createdAt)}</dd>
            </div>
          </dl>
          <div className="link-row">
            {a.sessionId && (
              <Link to={`/c/${a.sessionId}`} className="lift">
                <MessageSquare size={16} aria-hidden /> Open the conversation
              </Link>
            )}
            {safeHttpsUrl(a.repositoryUrl) && (
              <a href={safeHttpsUrl(a.repositoryUrl)} target="_blank" rel="noopener noreferrer" className="lift">
                <GitBranch size={16} aria-hidden /> Source code
                <span className="sr-only"> (opens in a new tab)</span>
              </a>
            )}
          </div>
        </section>
      </div>

      <section aria-labelledby="deploys-title" className="section">
        <h2 id="deploys-title">Deployment history</h2>
        {deployments.error && <ErrorBanner message={deployments.error} onRetry={deployments.reload} />}
        {deployments.data && deployments.data.length === 0 && <p className="muted">No deployments yet.</p>}
        {deployments.data && deployments.data.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">When</th>
                  <th scope="col">Where</th>
                  <th scope="col">Version</th>
                  <th scope="col">By</th>
                  <th scope="col">Approval</th>
                </tr>
              </thead>
              <tbody>
                {deployments.data.map((d) => (
                  <tr key={d.id}>
                    <td>{fullTime(d.createdAt)}</td>
                    <td>
                      <Badge tone={d.environment === EnvironmentName.PRODUCTION ? "success" : "info"}>{ENV_LABEL[d.environment]}</Badge>
                    </td>
                    <td>
                      <code>{d.revision}</code>
                    </td>
                    <td>{personName(d.deployedBy)}</td>
                    <td>{d.approvalId ? <Link to={`/approvals/${d.approvalId}`}>View</Link> : <span className="muted">—</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <Dialog
        open={askOpen}
        onClose={() => setAskOpen(false)}
        title="Request production"
        footer={
          <>
            <Button variant="ghost" onClick={() => setAskOpen(false)}>
              Cancel
            </Button>
            <Button variant="primary" busy={busy} onClick={() => void request()}>
              Send request
            </Button>
          </>
        }
      >
        <p>
          This asks an approver to put <strong>{preview?.revision}</strong> — what preview runs now — into production. You'll be notified when they
          decide.
        </p>
        {error && <ErrorBanner message={error} />}
        <Field label="Note for the approver (optional)">
          {(id) => <TextArea id={id} rows={3} value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. Finance team has tested it this week." />}
        </Field>
      </Dialog>
    </div>
  );
}
