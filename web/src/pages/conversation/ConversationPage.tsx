import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type KeyboardEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { ArrowUp, ChevronRight, Clock, ListChecks, RotateCcw, Share2, Square } from "lucide-react";
import { config } from "@/config";
import { A2uiSurface } from "@/a2ui/Renderer";
import { ApprovalPanel } from "@/approvals/ApprovalPanel";
import { waitingOn } from "@/approvals/logic";
import type { ChatMessage, ChatPart } from "@/api/chat/types";
import { friendlyError } from "@/api/errors";
import { activeTurn, completedTurnEnds, type Entry, type ToolActivity } from "@/conversation/state";
import { useConversation } from "@/conversation/useConversation";
import { Markdown } from "@/a2ui/markdown";
import { ApprovalState } from "@/gen/workbench/v1/approvals_pb";
import { CATEGORY_LABEL, humanToolName, toolCategory } from "@/lib/tools";
import { Button, IconButton } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { ErrorBanner, Loading } from "@/ui/States";
import { useToast } from "@/ui/Toast";
import { ActivityPanel, StatusIcon } from "./ActivityPanel";
import { takeFirstMessage } from "./firstMessage";
import { QuestionCard } from "./QuestionCard";
import { ShareDialog } from "./ShareDialog";
import { forkFromTurn } from "./tryAgain";

function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (cb: () => void) => {
      const mq = window.matchMedia(query);
      mq.addEventListener("change", cb);
      return () => mq.removeEventListener("change", cb);
    },
    [query],
  );
  return useSyncExternalStore(subscribe, () => window.matchMedia(query).matches, () => false);
}

export function ConversationPage() {
  const { sessionId = "" } = useParams();
  const [search, setSearch] = useSearchParams();
  // A share link carries a token the A2A client sends as X-Share-Token.
  const shareToken = search.get("share");
  useEffect(() => {
    if (!shareToken) return;
    try {
      sessionStorage.setItem(`workbench.share.${sessionId}`, shareToken);
    } catch {
      /* storage unavailable */
    }
    search.delete("share");
    setSearch(search, { replace: true });
  }, [shareToken, sessionId, search, setSearch]);

  return <Conversation key={sessionId} sessionId={sessionId} />;
}

function Conversation({ sessionId }: { sessionId: string }) {
  const convo = useConversation(sessionId, config().defaultAgent);
  const { state, store } = convo;
  const navigate = useNavigate();
  const toast = useToast();
  const isNarrow = useMediaQuery("(max-width: 1023px)");
  const [activityOpen, setActivityOpen] = useState(true);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [shareOpen, setShareOpen] = useState(false);
  const [forking, setForking] = useState<string>();
  const [draft, setDraft] = useState("");
  const scrollRef = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  useSyncExternalStore(store.subscribe, store.getVersion);

  // The first message, typed on Home, is sent once the conversation has loaded.
  const sentFirst = useRef(false);
  useEffect(() => {
    if (convo.loading || sentFirst.current) return;
    sentFirst.current = true;
    const first = takeFirstMessage(sessionId);
    if (first && state.entries.length === 0) void convo.send(first);
  }, [convo.loading, convo, sessionId, state.entries.length]);

  // Follow the conversation down while the reader is at the bottom — including
  // when a card grows after it loads, which is not a render of this component.
  useEffect(() => {
    const el = scrollRef.current;
    if (el && atBottom.current) el.scrollTop = el.scrollHeight;
  });
  useEffect(() => {
    const el = scrollRef.current;
    const content = el?.firstElementChild;
    if (!el || !content || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      if (atBottom.current) el.scrollTop = el.scrollHeight;
    });
    observer.observe(content);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const turn = activeTurn(state);
  const awaiting = state.awaiting;
  const turnEnds = useMemo(() => completedTurnEnds(state), [state]);
  const cardApprovals = store.approvalIds();
  const tools = useMemo(() => Object.values(state.tools), [state.tools]);
  const groups = useMemo(() => groupEntries(state.entries), [state.entries]);
  const pendingApproval =
    awaiting?.kind === "tool_approval"
      ? Object.values(convo.approvals).find((a) => a.taskId === awaiting.taskId && a.state === ApprovalState.PENDING)
      : undefined;

  const busy = convo.streaming || turn?.state === "working" || turn?.state === "submitted";
  const blockedReason =
    awaiting?.kind === "ask_user"
      ? "Answer the question above to continue."
      : awaiting?.kind === "tool_approval"
        ? pendingApproval
          ? pendingApproval.callerMayDecide
            ? "Approve or refuse the step above to continue."
            : `Waiting on ${waitingOn(pendingApproval)} to approve the step above.`
          : "Waiting for an approval."
        : awaiting?.kind === "unknown"
          ? "The agent is waiting for something this app can't show. You can stop the turn and try again."
          : undefined;

  const submit = () => {
    if (!draft.trim() || busy || blockedReason) return;
    const text = draft;
    setDraft("");
    atBottom.current = true;
    void convo.send(text);
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      submit();
    }
  };

  const tryAgain = async (taskId: string) => {
    setForking(taskId);
    try {
      const id = await forkFromTurn(sessionId, taskId);
      toast("Started a new conversation from that point.", "success");
      navigate(`/c/${id}`);
    } catch (err) {
      toast(friendlyError(err), "danger");
    } finally {
      setForking(undefined);
    }
  };

  const title = convo.session?.name || "New conversation";
  const showPanel = !isNarrow && activityOpen;
  const runningCount = tools.filter((t) => t.status === "running").length;

  return (
    <div className={`convo ${showPanel ? "with-panel" : ""}`}>
      <div className="convo-main">
        <header className="convo-header">
          <nav aria-label="Breadcrumb" className="crumbs">
            <Link to="/">Home</Link>
            <ChevronRight size={14} aria-hidden />
          </nav>
          <h1 className="convo-title">{title}</h1>
          <div className="convo-actions">
            <Button size="sm" variant="ghost" icon={<Share2 size={16} />} onClick={() => setShareOpen(true)}>
              Share
            </Button>
            <Button
              size="sm"
              variant={showPanel ? "secondary" : "ghost"}
              icon={<ListChecks size={16} />}
              aria-expanded={isNarrow ? drawerOpen : activityOpen}
              aria-controls="activity-panel"
              onClick={() => (isNarrow ? setDrawerOpen(true) : setActivityOpen((o) => !o))}
            >
              Activity{runningCount > 0 ? ` (${runningCount})` : ""}
            </Button>
          </div>
        </header>

        <div
          className="timeline-scroll"
          ref={scrollRef}
          onScroll={(e) => {
            const el = e.currentTarget;
            atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
          }}
        >
          <div className="timeline" aria-live="polite" aria-busy={busy}>
            {convo.loading && <Loading label="Opening conversation" />}
            {convo.loadError && <ErrorBanner message={convo.loadError} />}
            {groups.map((group) => {
              const end = turnEnds.get(group.lastIndex);
              return (
                <div key={group.key} className="timeline-item">
                  {renderGroup(group, {
                    state,
                    store,
                    cardApprovals,
                    approvals: convo.approvals,
                    busy: busy || !!convo.streaming,
                    onAnswer: (request, answers) => {
                      atBottom.current = true;
                      return convo.answer(request, answers);
                    },
                    onApprovalSettled: (approval) => {
                      // The reader just acted: follow the agent as it carries on.
                      atBottom.current = true;
                      convo.onApprovalSettled(approval);
                    },
                    openActivity: () => (isNarrow ? setDrawerOpen(true) : setActivityOpen(true)),
                  })}
                  {end && !busy && (
                    <div className="turn-end">
                      <Button size="sm" variant="ghost" icon={<RotateCcw size={14} />} busy={forking === end} onClick={() => void tryAgain(end)}>
                        Try again from here
                      </Button>
                    </div>
                  )}
                </div>
              );
            })}
            {busy && !awaiting && (
              <div className="thinking" role="status">
                <span className="dots" aria-hidden>
                  <span />
                  <span />
                  <span />
                </span>
                <span>{runningCount > 0 ? `Working — ${humanToolName(tools.filter((t) => t.status === "running").at(-1)!.name).toLowerCase()}` : "Working…"}</span>
              </div>
            )}
            {awaiting?.kind === "unknown" && (
              <div className="banner banner-info" role="status">
                <Clock size={18} aria-hidden />
                <span>{state.awaitingText || "The agent is waiting for input this app can't show."}</span>
              </div>
            )}
          </div>
        </div>

        <div className="composer-wrap">
          {convo.error && (
            <div className="composer-error">
              <ErrorBanner message={convo.error} onRetry={convo.dismissError} />
            </div>
          )}
          {blockedReason && (
            <p className="composer-note" role="status">
              <Clock size={14} aria-hidden /> {blockedReason}
            </p>
          )}
          <form
            className="composer"
            onSubmit={(e) => {
              e.preventDefault();
              submit();
            }}
          >
            <label htmlFor="composer-input" className="sr-only">
              Message the builder
            </label>
            <textarea
              id="composer-input"
              rows={1}
              className="composer-input"
              placeholder={blockedReason ? "Type now, send when the builder is ready" : "Ask for a change, or tell the builder more…"}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={onKey}
            />
            {busy ? (
              <IconButton label="Stop" className="composer-btn stop" onClick={() => void convo.stop()}>
                <Square size={16} />
              </IconButton>
            ) : (
              <button type="submit" className="composer-btn" aria-label="Send" disabled={!draft.trim() || !!blockedReason}>
                <ArrowUp size={18} />
              </button>
            )}
          </form>
        </div>
      </div>

      {showPanel && (
        <aside id="activity-panel" className="activity-panel" aria-label="Activity">
          <div className="panel-head">
            <h2>Activity</h2>
          </div>
          <ActivityPanel tools={tools} />
        </aside>
      )}
      {isNarrow && (
        <Dialog open={drawerOpen} onClose={() => setDrawerOpen(false)} title="Activity" variant="drawer">
          <ActivityPanel tools={tools} />
        </Dialog>
      )}
      <ShareDialog sessionId={sessionId} open={shareOpen} onClose={() => setShareOpen(false)} />
    </div>
  );
}

type Group =
  | { type: "tools"; key: string; ids: string[]; lastIndex: number }
  | { type: "entry"; key: string; entry: Entry; lastIndex: number };

/** Consecutive tool calls collapse into one compact row. */
function groupEntries(entries: Entry[]): Group[] {
  const groups: Group[] = [];
  entries.forEach((entry, index) => {
    const last = groups.at(-1);
    if (entry.kind === "tool") {
      if (last?.type === "tools") {
        last.ids.push(entry.id);
        last.lastIndex = index;
      } else groups.push({ type: "tools", key: `tools-${entry.id}`, ids: [entry.id], lastIndex: index });
      return;
    }
    const key =
      entry.kind === "message" ? `m-${entry.id}` : entry.kind === "surface" ? `s-${entry.surfaceId}` : entry.kind === "approval" ? `a-${entry.approvalId}` : `q-${entry.requestId}`;
    groups.push({ type: "entry", key, entry, lastIndex: index });
  });
  return groups;
}

interface RenderCtx {
  state: ReturnType<typeof useConversation>["state"];
  store: ReturnType<typeof useConversation>["store"];
  cardApprovals: Set<string>;
  approvals: ReturnType<typeof useConversation>["approvals"];
  busy: boolean;
  onAnswer: ReturnType<typeof useConversation>["answer"];
  onApprovalSettled: ReturnType<typeof useConversation>["onApprovalSettled"];
  openActivity: () => void;
}

function renderGroup(group: Group, ctx: RenderCtx) {
  if (group.type === "tools") {
    const tools = group.ids.map((id) => ctx.state.tools[id]).filter((t): t is ToolActivity => !!t && toolCategory(t.name) !== "showed");
    if (tools.length === 0) return null;
    return <ToolSummary tools={tools} onOpen={ctx.openActivity} />;
  }
  const entry = group.entry;
  switch (entry.kind) {
    case "message": {
      const message = ctx.state.messages[entry.id];
      return message ? <MessageBubble message={message} /> : null;
    }
    case "surface":
      return (
        <div className="surface-wrap">
          <A2uiSurface store={ctx.store} surfaceId={entry.surfaceId} onApprovalSettled={ctx.onApprovalSettled} />
        </div>
      );
    case "approval": {
      // An ApprovalCard surface already draws this approval.
      if (ctx.cardApprovals.has(entry.approvalId)) return null;
      return <ApprovalPanel approvalId={entry.approvalId} initial={ctx.approvals[entry.approvalId]} onSettled={ctx.onApprovalSettled} />;
    }
    case "question": {
      const question = ctx.state.questions[entry.requestId];
      if (!question) return null;
      return <QuestionCard question={question} disabled={ctx.busy} onAnswer={(answers) => void ctx.onAnswer(question.request, answers)} />;
    }
  }
}

function ToolSummary({ tools, onOpen }: { tools: ToolActivity[]; onOpen: () => void }) {
  return (
    <button type="button" className="tool-summary" onClick={onOpen}>
      <span className="sr-only">{tools.length === 1 ? "1 step" : `${tools.length} steps`}, show details in Activity: </span>
      <ul>
        {tools.map((t) => (
          <li key={t.id}>
            <StatusIcon status={t.status} />
            <span>
              <span className="muted">{t.status === "waiting" ? "Waiting for approval to" : CATEGORY_LABEL[toolCategory(t.name)]}</span>{" "}
              {humanToolName(t.name).toLowerCase()}
              {pathOf(t.args) && <code>{pathOf(t.args)}</code>}
            </span>
          </li>
        ))}
      </ul>
    </button>
  );
}

function pathOf(args: unknown): string | undefined {
  if (!args || typeof args !== "object") return undefined;
  const v = (args as Record<string, unknown>).path ?? (args as Record<string, unknown>).name;
  return typeof v === "string" ? v : undefined;
}

function MessageBubble({ message }: { message: ChatMessage }) {
  return (
    <div className={`msg msg-${message.role}`}>
      {message.role === "agent" && (
        <span className="msg-avatar" aria-hidden>
          W
        </span>
      )}
      <div className="msg-body">
        <span className="sr-only">{message.role === "user" ? "You said:" : "Builder:"}</span>
        {message.parts.map((part, i) => (
          <PartView key={i} part={part} />
        ))}
      </div>
    </div>
  );
}

function PartView({ part }: { part: ChatPart }) {
  switch (part.kind) {
    case "text":
      return part.thought ? (
        <details className="thought">
          <summary>Thinking</summary>
          <Markdown text={part.text} />
        </details>
      ) : (
        <Markdown text={part.text} />
      );
    case "file":
      return part.url ? (
        <a href={part.url} target="_blank" rel="noopener noreferrer" className="file-chip">
          {part.name}
        </a>
      ) : (
        <span className="file-chip">{part.name}</span>
      );
    case "ask_user":
      return (
        <div className="answer-record">
          {part.interaction.answers.map((a, i) => (
            <p key={i}>
              {part.interaction.questions[i]?.question && <span className="answer-q">{part.interaction.questions[i]!.question}</span>}
              {a.join(", ")}
            </p>
          ))}
        </div>
      );
    default:
      return null;
  }
}
