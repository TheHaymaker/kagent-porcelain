import { useEffect, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { ArrowLeft, CheckSquare, ChevronRight } from "lucide-react";
import { api } from "@/api/clients";
import { useRpc } from "@/api/useRpc";
import { ApprovalPanel } from "@/approvals/ApprovalPanel";
import { approvalTitle } from "@/approvals/logic";
import { InboxView, type Approval } from "@/gen/workbench/v1/approvals_pb";
import { APPROVAL_STATE_LABEL, APPROVAL_STATE_TONE, KIND_LABEL, KIND_TONE, personName, relativeTime } from "@/lib/format";
import { Badge } from "@/ui/Badge";
import { PageHeader } from "@/ui/PageHeader";
import { EmptyState, ErrorBanner, Skeleton } from "@/ui/States";
import { Tabs } from "@/ui/Tabs";

type TabKey = "decide" | "mine" | "decided";
const VIEW: Record<TabKey, InboxView> = {
  decide: InboxView.TO_DECIDE,
  mine: InboxView.MY_REQUESTS,
  decided: InboxView.DECIDED_BY_ME,
};
const EMPTY: Record<TabKey, { title: string; body: string }> = {
  decide: { title: "Nothing waiting for you", body: "When someone needs your approval, it shows up here and in your notifications." },
  mine: { title: "No requests yet", body: "Steps your conversations needed approval for will appear here." },
  decided: { title: "You haven't decided anything yet", body: "Approvals you approve or refuse are kept here." },
};

export function ApprovalsPage() {
  const { approvalId } = useParams();
  const [search, setSearch] = useSearchParams();
  const navigate = useNavigate();
  const tab = (["decide", "mine", "decided"].includes(search.get("tab") ?? "") ? search.get("tab") : "decide") as TabKey;
  const [counts, setCounts] = useState<Partial<Record<TabKey, number>>>({});

  const list = useRpc(async (signal) => (await api.approvals.listInbox({ view: VIEW[tab] }, { signal })).approvals, [tab], { pollMs: 15000 });
  useEffect(() => {
    if (list.data) setCounts((c) => ({ ...c, [tab]: list.data!.length }));
  }, [list.data, tab]);
  useEffect(() => {
    if (tab === "decide") return;
    let cancelled = false;
    api.approvals
      .listInbox({ view: InboxView.TO_DECIDE })
      .then((r) => !cancelled && setCounts((c) => ({ ...c, decide: r.approvals.length })))
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [tab]);

  const selected = approvalId ?? undefined;

  return (
    <div className={`page approvals-page ${selected ? "has-detail" : ""}`}>
      <PageHeader title="Approvals" description="Steps that need a person's go-ahead before an agent carries on." />
      <Tabs
        label="Approval lists"
        idPrefix="inbox"
        value={tab}
        onChange={(key) => {
          setSearch({ tab: key });
          if (approvalId) navigate(`/approvals?tab=${key}`);
        }}
        items={[
          { key: "decide", label: "To decide", count: counts.decide },
          { key: "mine", label: "My requests" },
          { key: "decided", label: "Decided by me" },
        ]}
      />
      <div className="split" id="inbox-panel" role="tabpanel" aria-labelledby={`inbox-tab-${tab}`}>
        <div className="split-list">
          {list.error && <ErrorBanner message={list.error} onRetry={list.reload} />}
          {list.loading && !list.data && <Skeleton lines={5} />}
          {list.data && list.data.length === 0 && (
            <EmptyState icon={<CheckSquare size={22} />} title={EMPTY[tab].title}>
              {EMPTY[tab].body}
            </EmptyState>
          )}
          <ul className="list">
            {list.data?.map((a) => (
              <li key={a.id}>
                <InboxRow approval={a} selected={a.id === selected} tab={tab} />
              </li>
            ))}
          </ul>
        </div>
        <div className="split-detail">
          {selected ? (
            <>
              <Link to={`/approvals?tab=${tab}`} className="back-link">
                <ArrowLeft size={16} aria-hidden /> All approvals
              </Link>
              <ApprovalPanel
                key={selected}
                approvalId={selected}
                initial={list.data?.find((a) => a.id === selected)}
                variant="detail"
                showConversationLink
                onSettled={() => list.reload()}
              />
            </>
          ) : (
            <div className="detail-placeholder">
              <EmptyState title="Pick an approval">Select one from the list to see exactly what the agent wants to do.</EmptyState>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function InboxRow({ approval, selected, tab }: { approval: Approval; selected: boolean; tab: TabKey }) {
  return (
    <Link to={`/approvals/${approval.id}?tab=${tab}`} className={`list-row inbox-row ${selected ? "is-selected" : ""}`} aria-current={selected ? "true" : undefined}>
      <span className="list-main">
        <span className="inbox-badges">
          <Badge tone={KIND_TONE[approval.kind]}>{KIND_LABEL[approval.kind]}</Badge>
          <Badge tone={APPROVAL_STATE_TONE[approval.state]}>{APPROVAL_STATE_LABEL[approval.state]}</Badge>
        </span>
        <span className="list-title">{approvalTitle(approval)}</span>
        <span className="list-sub">
          {personName(approval.requester)} · {relativeTime(approval.createdAt)}
        </span>
      </span>
      <ChevronRight size={16} className="muted" aria-hidden />
    </Link>
  );
}
