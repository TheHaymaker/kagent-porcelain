import { useEffect, useId, useRef, useState } from "react";
import { Link } from "react-router";
import { Check, ChevronDown, Clock, ShieldCheck, UserRound, X } from "lucide-react";
import { api } from "@/api/clients";
import { friendlyError } from "@/api/errors";
import { useRpc } from "@/api/useRpc";
import { useMyId } from "@/auth/AuthContext";
import { ActionKind, ApprovalState, RememberScope, type Approval } from "@/gen/workbench/v1/approvals_pb";
import {
  APPROVAL_STATE_LABEL,
  APPROVAL_STATE_TONE,
  KIND_LABEL,
  KIND_TONE,
  personName,
  relativeTime,
} from "@/lib/format";
import { argSummary, humanToolName } from "@/lib/tools";
import { Badge } from "@/ui/Badge";
import { Button } from "@/ui/Button";
import { JsonView } from "@/ui/JsonView";
import { ErrorBanner, Skeleton } from "@/ui/States";
import { approvalTitle, canDecide, isSettled, REMEMBER_LABEL, rememberOptions, waitingOn, whyNotDecider } from "./logic";

const POLL_MS = 4000;

export interface ApprovalPanelProps {
  approvalId: string;
  /** An approval already in hand (from a list), shown while the live one loads. */
  initial?: Approval;
  title?: string;
  summary?: string;
  /** Called once when a pending approval becomes decided or superseded. */
  onSettled?: (approval: Approval) => void;
  /** Show a link to the conversation that asked. */
  showConversationLink?: boolean;
  variant?: "inline" | "detail";
}

/**
 * One approval, drawn entirely from the ApprovalService. Polls while pending so
 * a decision made elsewhere (by an approver, in Teams) shows up here.
 */
export function ApprovalPanel({ approvalId, initial, title, summary, onSettled, showConversationLink, variant = "inline" }: ApprovalPanelProps) {
  const [pollMs, setPollMs] = useState<number | undefined>(POLL_MS);
  const rpc = useRpc(
    async (signal) => (await api.approvals.getApproval({ id: approvalId }, { signal })).approval,
    [approvalId],
    { pollMs },
  );
  const approval = rpc.data ?? initial;
  const settledRef = useRef<boolean | undefined>(undefined);

  useEffect(() => {
    if (!approval) return;
    const settled = isSettled(approval);
    if (settled) setPollMs(undefined);
    if (settledRef.current === false && settled) onSettled?.(approval);
    settledRef.current = settled;
  }, [approval, onSettled]);

  if (!approval) {
    if (rpc.error) return <ErrorBanner message={rpc.error} onRetry={rpc.reload} />;
    return (
      <div className="approval approval-loading" aria-busy>
        <Skeleton lines={3} />
      </div>
    );
  }

  return (
    <ApprovalView
      approval={approval}
      title={title}
      summary={summary}
      variant={variant}
      showConversationLink={showConversationLink}
      onDecided={(next) => rpc.setData(next)}
    />
  );
}

export function ApprovalView({
  approval,
  title,
  summary,
  variant = "inline",
  showConversationLink,
  onDecided,
}: {
  approval: Approval;
  title?: string;
  summary?: string;
  variant?: "inline" | "detail";
  showConversationLink?: boolean;
  onDecided?: (approval: Approval) => void;
}) {
  const myId = useMyId();
  const headingId = useId();
  const pending = approval.state === ApprovalState.PENDING;
  const decider = canDecide(approval);
  const why = whyNotDecider(approval, myId);
  const special = approval.kind !== ActionKind.ORDINARY && approval.kind !== ActionKind.UNSPECIFIED;

  return (
    <section
      className={`approval approval-${variant} ${pending ? "is-pending" : "is-settled"} ${special ? "is-special" : ""}`}
      aria-labelledby={headingId}
    >
      <div className="approval-head">
        <div className="approval-badges">
          <Badge tone={KIND_TONE[approval.kind]} icon={<ShieldCheck size={14} aria-hidden />}>
            {KIND_LABEL[approval.kind]}
          </Badge>
          <Badge tone={APPROVAL_STATE_TONE[approval.state]}>{APPROVAL_STATE_LABEL[approval.state]}</Badge>
        </div>
        {approval.createdAt && <span className="muted small">{relativeTime(approval.createdAt)}</span>}
      </div>
      <h3 id={headingId} className="approval-title">
        {title || approvalTitle(approval)}
      </h3>
      {summary && <p className="approval-summary">{summary}</p>}
      <p className="muted small">
        Requested by {personName(approval.requester)}
        {approval.agent ? ` through ${approval.agent.split("/").pop()}` : ""}
        {showConversationLink && approval.sessionId && (
          <>
            {" · "}
            <Link to={`/c/${approval.sessionId}`}>Open conversation</Link>
          </>
        )}
      </p>

      {approval.calls.length > 0 && <CallList approval={approval} defaultOpen={variant === "detail"} />}

      {pending && decider && <DecisionForm approval={approval} onDecided={onDecided} />}

      {pending && !decider && (
        <div className="approval-waiting" role="status">
          <Clock size={18} aria-hidden />
          <div>
            <p>
              <strong>Waiting on {waitingOn(approval)}.</strong> This goes ahead as soon as they approve.
            </p>
            {why && <p className="muted small">{why}</p>}
          </div>
        </div>
      )}

      {!pending && (
        <div className={`approval-outcome outcome-${approval.state === ApprovalState.APPROVED ? "yes" : "no"}`}>
          {approval.state === ApprovalState.APPROVED ? <Check size={18} aria-hidden /> : <X size={18} aria-hidden />}
          <p>
            {approval.state === ApprovalState.SUPERSEDED
              ? "This is no longer needed — the conversation moved on."
              : `${APPROVAL_STATE_LABEL[approval.state]} by ${personName(approval.decidedBy)}`}
            {approval.decidedAt && <span className="muted"> · {relativeTime(approval.decidedAt)}</span>}
            {approval.reason && <span className="approval-reason">“{approval.reason}”</span>}
          </p>
        </div>
      )}
    </section>
  );
}

function CallList({ approval, defaultOpen }: { approval: Approval; defaultOpen: boolean }) {
  return (
    <ul className="call-list" aria-label="What the agent wants to do">
      {approval.calls.map((call) => {
        const summary = argSummary(call.args);
        return (
          <li key={call.id || call.callId || call.name}>
            <details open={defaultOpen}>
              <summary>
                <ChevronDown size={16} className="chev" aria-hidden />
                <span className="call-name">{humanToolName(call.name)}</span>
                {summary && <code className="call-arg">{summary}</code>}
                {call.kind !== ActionKind.ORDINARY && call.kind !== ActionKind.UNSPECIFIED && (
                  <Badge tone={KIND_TONE[call.kind]}>{KIND_LABEL[call.kind]}</Badge>
                )}
              </summary>
              <JsonView value={call.args ?? {}} label={`Details for ${call.name}`} />
            </details>
          </li>
        );
      })}
    </ul>
  );
}

function DecisionForm({ approval, onDecided }: { approval: Approval; onDecided?: (approval: Approval) => void }) {
  const [reason, setReason] = useState("");
  const options = rememberOptions(approval);
  const [remember, setRemember] = useState<RememberScope>(RememberScope.ONCE);
  const [busy, setBusy] = useState<"approve" | "refuse">();
  const [error, setError] = useState<string>();
  const reasonId = useId();
  const groupName = useId();

  const decide = async (approved: boolean) => {
    setBusy(approved ? "approve" : "refuse");
    setError(undefined);
    try {
      const response = await api.approvals.decide({
        approvalId: approval.id,
        approved,
        reason: reason.trim(),
        remember: options.includes(remember) ? remember : RememberScope.ONCE,
      });
      if (response.approval) onDecided?.(response.approval);
    } catch (err) {
      setError(friendlyError(err));
    } finally {
      setBusy(undefined);
    }
  };

  return (
    <div className="decision">
      <label htmlFor={reasonId} className="field-label">
        Reason <span className="muted">(optional, shared with the requester)</span>
      </label>
      <textarea
        id={reasonId}
        className="input textarea"
        rows={2}
        value={reason}
        onChange={(e) => setReason(e.target.value)}
        placeholder="e.g. Looks good — checked the preview."
      />
      {options.length > 1 && (
        <fieldset className="remember">
          <legend className="field-label">Remember this answer</legend>
          {options.map((scope) => (
            <label key={scope} className="radio">
              <input
                type="radio"
                name={groupName}
                checked={remember === scope}
                onChange={() => setRemember(scope)}
              />
              <span>{REMEMBER_LABEL[scope]}</span>
            </label>
          ))}
        </fieldset>
      )}
      {error && <ErrorBanner message={error} />}
      <div className="decision-actions">
        <Button variant="primary" icon={<Check size={16} />} busy={busy === "approve"} disabled={!!busy} onClick={() => void decide(true)}>
          Approve
        </Button>
        <Button variant="secondary" icon={<X size={16} />} busy={busy === "refuse"} disabled={!!busy} onClick={() => void decide(false)}>
          Refuse
        </Button>
        {approval.approverGroups.length > 0 && (
          <span className="muted small decision-note">
            <UserRound size={14} aria-hidden /> Approvers: {waitingOn(approval)}
          </span>
        )}
      </div>
    </div>
  );
}
