import { useState } from "react";
import { Link2, Trash2 } from "lucide-react";
import { api } from "@/api/clients";
import { friendlyError } from "@/api/errors";
import { useRpc } from "@/api/useRpc";
import { fullTime } from "@/lib/format";
import { Button } from "@/ui/Button";
import { CopyButton } from "@/ui/CopyButton";
import { Dialog } from "@/ui/Dialog";
import { ErrorBanner, Skeleton } from "@/ui/States";

const WEEK_SECONDS = 7n * 24n * 3600n;

/** The link a share token opens; ConversationPage stores the token for the A2A client. */
export function shareLink(sessionId: string, token: string): string {
  return `${window.location.origin}/c/${sessionId}?share=${encodeURIComponent(token)}`;
}

export function ShareDialog({ sessionId, open, onClose }: { sessionId: string; open: boolean; onClose: () => void }) {
  const shares = useRpc(async (signal) => (await api.sessions.listSessionShares({ sessionId }, { signal })).shares, [sessionId], {
    enabled: open,
  });
  const [link, setLink] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  const create = async () => {
    setBusy(true);
    setError(undefined);
    try {
      const response = await api.sessions.createSessionShare({ sessionId, ttl: { seconds: WEEK_SECONDS, nanos: 0 } });
      setLink(shareLink(sessionId, response.token));
      shares.reload();
    } catch (err) {
      setError(friendlyError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title="Share this conversation">
      <p className="muted">
        Anyone signed in to Workbench with the link can read this conversation for 7 days. They can't send messages or approve anything.
      </p>
      {error && <ErrorBanner message={error} />}
      {link ? (
        <div className="share-link">
          <input className="input" readOnly value={link} aria-label="Share link" onFocus={(e) => e.currentTarget.select()} />
          <CopyButton text={link} />
        </div>
      ) : (
        <Button variant="primary" icon={<Link2 size={16} />} busy={busy} onClick={() => void create()}>
          Create link
        </Button>
      )}
      <h3 className="subhead">Active links</h3>
      {shares.loading && <Skeleton lines={2} />}
      {shares.error && <ErrorBanner message={shares.error} onRetry={shares.reload} />}
      {shares.data && shares.data.length === 0 && <p className="muted small">No links yet.</p>}
      <ul className="list compact">
        {shares.data?.map((share) => (
          <li key={share.id} className="list-row static">
            <Link2 size={16} className="muted" aria-hidden />
            <span className="list-main">
              <span className="list-title">Link created {fullTime(share.createdAt)}</span>
              <span className="list-sub">Expires {fullTime(share.expiresAt)}</span>
            </span>
            <Button
              size="sm"
              variant="ghost"
              icon={<Trash2 size={16} />}
              onClick={async () => {
                try {
                  await api.sessions.revokeSessionShare({ shareId: share.id });
                  shares.reload();
                } catch (err) {
                  setError(friendlyError(err));
                }
              }}
            >
              Revoke
            </Button>
          </li>
        ))}
      </ul>
    </Dialog>
  );
}
