import { useEffect, useState } from "react";
import { Link2, Trash2 } from "lucide-react";
import { api } from "@/api/clients";
import { friendlyError } from "@/api/errors";
import { useRpc } from "@/api/useRpc";
import { useAuth } from "@/auth/AuthContext";
import { REMEMBER_LABEL, humanGroup } from "@/approvals/logic";
import { Channel, NotificationKind, type Preference } from "@/gen/workbench/v1/notifications_pb";
import { fullTime, personName } from "@/lib/format";
import { humanToolName } from "@/lib/tools";
import { Badge } from "@/ui/Badge";
import { Button } from "@/ui/Button";
import { PageHeader } from "@/ui/PageHeader";
import { EmptyState, ErrorBanner, Skeleton } from "@/ui/States";
import { useToast } from "@/ui/Toast";

const KINDS: { kind: NotificationKind; label: string }[] = [
  { kind: NotificationKind.APPROVAL_NEEDED, label: "Someone needs my approval" },
  { kind: NotificationKind.INPUT_NEEDED, label: "The builder has a question" },
  { kind: NotificationKind.APPROVAL_DECIDED, label: "My request was approved or refused" },
  { kind: NotificationKind.TASK_FINISHED, label: "A request finished" },
  { kind: NotificationKind.TASK_FAILED, label: "A request failed" },
  { kind: NotificationKind.APP_PROMOTED, label: "An app went live" },
  { kind: NotificationKind.LIMIT_REACHED, label: "I'm near a spending limit" },
];
const CHANNELS: { channel: Channel; label: string }[] = [
  { channel: Channel.IN_APP, label: "In Workbench" },
  { channel: Channel.EMAIL, label: "Email" },
  { channel: Channel.TEAMS, label: "Teams" },
  { channel: Channel.MOBILE_PUSH, label: "Phone" },
];

export function SettingsPage() {
  const { me } = useAuth();
  return (
    <div className="page settings">
      <PageHeader title="Settings" />
      <section className="section card" aria-labelledby="you-title">
        <h2 id="you-title">You</h2>
        <p>
          <strong>{personName(me?.person)}</strong> <span className="muted">{me?.person?.email}</span>
        </p>
        <div className="chips">
          {me?.groups.map((g) => (
            <Badge key={g}>{humanGroup(g)}</Badge>
          ))}
        </div>
      </section>
      <Identities />
      <Remembered />
      <Preferences />
    </div>
  );
}

function Identities() {
  const list = useRpc(async (signal) => (await api.identity.listLinkedIdentities({}, { signal })).identities, []);
  const toast = useToast();
  return (
    <section className="section card" aria-labelledby="ids-title">
      <h2 id="ids-title">Linked accounts</h2>
      <p className="muted">Accounts that act as you in Workbench — for example in Microsoft Teams or on your phone. To link one, sign in to Workbench from that app.</p>
      {list.error && <ErrorBanner message={list.error} onRetry={list.reload} />}
      {list.loading && !list.data && <Skeleton lines={2} />}
      {list.data?.length === 0 && <p className="muted small">No linked accounts.</p>}
      <ul className="list compact">
        {list.data?.map((id) => (
          <li key={`${id.issuer}/${id.subject}`} className="list-row static">
            <Link2 size={16} className="muted" aria-hidden />
            <span className="list-main">
              <span className="list-title">{id.displayHint || id.issuer}</span>
              <span className="list-sub">Linked {fullTime(id.linkedAt)}</span>
            </span>
            <Button
              size="sm"
              variant="ghost"
              icon={<Trash2 size={16} />}
              onClick={async () => {
                try {
                  await api.identity.unlinkIdentity({ issuer: id.issuer, subject: id.subject });
                  list.reload();
                } catch (err) {
                  toast(friendlyError(err), "danger");
                }
              }}
            >
              Unlink
            </Button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function Remembered() {
  const list = useRpc(async (signal) => (await api.approvals.listRememberedAnswers({}, { signal })).answers, []);
  const toast = useToast();
  return (
    <section className="section card" aria-labelledby="rem-title">
      <h2 id="rem-title">Remembered answers</h2>
      <p className="muted">When you tell an agent “don't ask again”, it's kept here. Forget one and you'll be asked next time.</p>
      {list.error && <ErrorBanner message={list.error} onRetry={list.reload} />}
      {list.data?.length === 0 && <EmptyState title="Nothing remembered" />}
      <ul className="list compact">
        {list.data?.map((a) => (
          <li key={a.id} className="list-row static">
            <span className="list-main">
              <span className="list-title">
                {a.approved ? "Allow" : "Refuse"} “{humanToolName(a.toolName)}”
              </span>
              <span className="list-sub">
                {REMEMBER_LABEL[a.scope]} · {fullTime(a.createdAt)}
              </span>
            </span>
            <Button
              size="sm"
              variant="ghost"
              onClick={async () => {
                try {
                  await api.approvals.forgetRememberedAnswer({ id: a.id });
                  list.reload();
                } catch (err) {
                  toast(friendlyError(err), "danger");
                }
              }}
            >
              Forget
            </Button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function Preferences() {
  const prefs = useRpc(async (signal) => (await api.notifications.getPreferences({}, { signal })).preferences, []);
  const [draft, setDraft] = useState<Map<NotificationKind, Set<Channel>>>(new Map());
  const [saving, setSaving] = useState(false);
  const toast = useToast();
  useEffect(() => {
    if (prefs.data) setDraft(new Map(prefs.data.map((p) => [p.kind, new Set(p.channels)])));
  }, [prefs.data]);

  const toggle = (kind: NotificationKind, channel: Channel) =>
    setDraft((prev) => {
      const next = new Map(prev);
      const set = new Set(next.get(kind) ?? []);
      if (set.has(channel)) set.delete(channel);
      else set.add(channel);
      next.set(kind, set);
      return next;
    });

  const save = async () => {
    setSaving(true);
    try {
      const preferences: Omit<Preference, "$typeName">[] = KINDS.map(({ kind }) => ({ kind, channels: [...(draft.get(kind) ?? [])] }));
      await api.notifications.setPreferences({ preferences });
      toast("Notification settings saved.", "success");
    } catch (err) {
      toast(friendlyError(err), "danger");
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="section card" aria-labelledby="prefs-title">
      <h2 id="prefs-title">Notifications</h2>
      {prefs.error && <ErrorBanner message={prefs.error} onRetry={prefs.reload} />}
      <div className="table-wrap">
        <table className="table prefs">
          <caption className="sr-only">Where to tell you about each kind of event</caption>
          <thead>
            <tr>
              <th scope="col">Tell me when…</th>
              {CHANNELS.map((c) => (
                <th key={c.channel} scope="col">
                  {c.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {KINDS.map(({ kind, label }) => (
              <tr key={kind}>
                <th scope="row">{label}</th>
                {CHANNELS.map((c) => (
                  <td key={c.channel}>
                    <input
                      type="checkbox"
                      aria-label={`${label}: ${c.label}`}
                      checked={draft.get(kind)?.has(c.channel) ?? false}
                      onChange={() => toggle(kind, c.channel)}
                    />
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div>
        <Button variant="primary" busy={saving} onClick={() => void save()}>
          Save
        </Button>
      </div>
    </section>
  );
}
