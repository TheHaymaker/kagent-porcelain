import { useState, type FormEvent, type KeyboardEvent } from "react";
import { Link, useNavigate } from "react-router";
import { ArrowRight, MessageSquare, Sparkles } from "lucide-react";
import { api } from "@/api/clients";
import { friendlyError } from "@/api/errors";
import { useRpc } from "@/api/useRpc";
import { useAuth } from "@/auth/AuthContext";
import { agentRef, config } from "@/config";
import { APP_STATUS_LABEL, APP_STATUS_TONE, money, newRequestId, relativeTime } from "@/lib/format";
import { Badge } from "@/ui/Badge";
import { Button } from "@/ui/Button";
import { EmptyState, ErrorBanner, Skeleton } from "@/ui/States";
import { queueFirstMessage } from "./conversation/firstMessage";

const SUGGESTIONS = [
  "An expense tracker where my team can upload receipts",
  "A form for booking the meeting rooms on floor 3",
  "A weekly summary of overdue invoices, emailed to me",
];

export function HomePage() {
  const navigate = useNavigate();
  const { me } = useAuth();
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const sessions = useRpc(async (signal) => (await api.sessions.listSessions({ page: { limit: 6 } }, { signal })).sessions, []);
  const apps = useRpc(async (signal) => (await api.apps.listApps({ mine: true }, { signal })).apps, []);

  const start = async (event?: FormEvent) => {
    event?.preventDefault();
    const text = prompt.trim();
    if (!text || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      const response = await api.sessions.createSession({
        agent: agentRef(config().defaultAgent),
        requestId: newRequestId(),
        name: text.length > 60 ? `${text.slice(0, 57)}…` : text,
      });
      const id = response.session?.id;
      if (!id) throw new Error("The conversation couldn't be started.");
      queueFirstMessage(id, text);
      navigate(`/c/${id}`);
    } catch (err) {
      setError(friendlyError(err));
      setBusy(false);
    }
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void start();
    }
  };

  const firstName = me?.person?.displayName?.split(" ")[0];

  return (
    <div className="page home">
      <section className="hero" aria-labelledby="hero-title">
        <p className="hero-greeting">{firstName ? `Hi ${firstName}` : "Hi there"}</p>
        <h1 id="hero-title">What do you want to build?</h1>
        <form className="prompt-box" onSubmit={start}>
          <label htmlFor="prompt" className="sr-only">
            Describe the app you want
          </label>
          <textarea
            id="prompt"
            className="prompt-input"
            rows={3}
            placeholder="Describe it in your own words — who it's for and what it should do."
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            onKeyDown={onKey}
            aria-describedby="prompt-hint"
          />
          <div className="prompt-foot">
            <span id="prompt-hint" className="muted small">
              Enter to start · Shift+Enter for a new line
            </span>
            <Button type="submit" variant="primary" icon={<ArrowRight size={18} />} busy={busy} disabled={!prompt.trim()}>
              Start building
            </Button>
          </div>
        </form>
        {error && <ErrorBanner message={error} />}
        <div className="suggestions" aria-label="Ideas to start from">
          {SUGGESTIONS.map((s) => (
            <button key={s} type="button" className="chip" onClick={() => setPrompt(s)}>
              <Sparkles size={14} aria-hidden />
              {s}
            </button>
          ))}
        </div>
      </section>

      <div className="home-grid">
        <section aria-labelledby="recent-title" className="home-section">
          <div className="section-head">
            <h2 id="recent-title">Recent conversations</h2>
          </div>
          {sessions.error && <ErrorBanner message={sessions.error} onRetry={sessions.reload} />}
          {sessions.loading && !sessions.data && <Skeleton lines={4} />}
          {sessions.data && sessions.data.length === 0 && (
            <EmptyState icon={<MessageSquare size={22} />} title="No conversations yet">
              Your requests will show up here.
            </EmptyState>
          )}
          <ul className="list">
            {sessions.data?.map((s) => (
              <li key={s.id}>
                <Link to={`/c/${s.id}`} className="list-row">
                  <MessageSquare size={18} className="muted" aria-hidden />
                  <span className="list-main">
                    <span className="list-title">{s.name || "Untitled conversation"}</span>
                    <span className="list-sub">{relativeTime(s.updatedAt ?? s.createdAt)}</span>
                  </span>
                  <ArrowRight size={16} className="muted" aria-hidden />
                </Link>
              </li>
            ))}
          </ul>
        </section>

        <section aria-labelledby="apps-title" className="home-section">
          <div className="section-head">
            <h2 id="apps-title">My apps</h2>
            <Link to="/apps" className="small">
              See all
            </Link>
          </div>
          {apps.error && <ErrorBanner message={apps.error} onRetry={apps.reload} />}
          {apps.loading && !apps.data && <Skeleton lines={4} />}
          {apps.data && apps.data.length === 0 && <EmptyState title="No apps yet">Apps you build appear here.</EmptyState>}
          <ul className="list">
            {apps.data?.slice(0, 5).map((a) => (
              <li key={a.id}>
                <Link to={`/apps/${a.id}`} className="list-row">
                  <span className="app-icon" aria-hidden>
                    {a.name.slice(0, 1).toUpperCase()}
                  </span>
                  <span className="list-main">
                    <span className="list-title">{a.name}</span>
                    <span className="list-sub">{money(a.costCentsMtd)} this month</span>
                  </span>
                  <Badge tone={APP_STATUS_TONE[a.status]}>{APP_STATUS_LABEL[a.status]}</Badge>
                </Link>
              </li>
            ))}
          </ul>
        </section>
      </div>
    </div>
  );
}
