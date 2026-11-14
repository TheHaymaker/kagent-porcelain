/**
 * Drives one conversation: loads its history, surfaces and approvals, streams
 * turns, answers questions, and re-attaches to a turn once an approval resumes it.
 *
 * Approvals are never answered here. A tool-approval pause makes this hook
 * fetch ApprovalService.ListSessionApprovals; the approval cards decide through
 * ApprovalService.Decide and poll; when one settles, this hook follows the task
 * (A2A SubscribeToTask) to watch the agent carry on.
 */
import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { api } from "@/api/clients";
import { chatClient } from "@/api/chat/a2aChatClient";
import { answerText, askUserAnswer, type PendingRequest } from "@/api/chat/hitl";
import type { ChatConversationRef, ChatEvent } from "@/api/chat/types";
import { friendlyError, isAbort } from "@/api/errors";
import { a2uiFromPart, a2uiFromSurface } from "@/a2ui/extract";
import { SurfaceStore } from "@/a2ui/store";
import { ApprovalState, type Approval } from "@/gen/workbench/v1/approvals_pb";
import type { Session } from "@/gen/kagent/api/v1/sessions_pb";
import { newRequestId } from "@/lib/format";
import { activeTurn, conversationReducer, initialConversation } from "./state";

export function useConversation(sessionId: string, fallbackAgent: string) {
  const [state, dispatch] = useReducer(conversationReducer, initialConversation);
  const store = useMemo(() => new SurfaceStore(), []);
  const [session, setSession] = useState<Session>();
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string>();
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<string>();
  const [approvals, setApprovals] = useState<Record<string, Approval>>({});
  const streamRef = useRef<AbortController | undefined>(undefined);
  const followingRef = useRef(new Set<string>());
  const mounted = useRef(true);

  const agent = session?.agent?.ref ? `${session.agent.ref.namespace}/${session.agent.ref.name}` : fallbackAgent;
  const conversation: ChatConversationRef = useMemo(() => ({ id: sessionId, agent }), [sessionId, agent]);
  const conversationRef = useRef(conversation);
  conversationRef.current = conversation;

  const applySurfaces = useCallback(
    (event: ChatEvent) => {
      if (event.type !== "message") return;
      for (const part of event.message.parts) {
        const messages = a2uiFromPart(part);
        if (messages.length) store.apply(messages);
      }
    },
    [store],
  );

  const refreshApprovals = useCallback(async () => {
    try {
      const response = await api.approvals.listSessionApprovals({ sessionId });
      if (!mounted.current) return;
      setApprovals((prev) => {
        const next = { ...prev };
        for (const a of response.approvals) next[a.id] = a;
        return next;
      });
      dispatch({ type: "approvals", approvals: response.approvals.map((a) => ({ id: a.id, taskId: a.taskId })) });
    } catch (err) {
      if (!isAbort(err)) setError(friendlyError(err));
    }
  }, [sessionId]);

  const run = useCallback(
    async (events: AsyncIterable<ChatEvent>, controller: AbortController) => {
      streamRef.current = controller;
      setStreaming(true);
      setError(undefined);
      try {
        for await (const event of events) {
          if (controller.signal.aborted) break;
          applySurfaces(event);
          dispatch({ type: "event", event });
          if (event.type === "status" && event.state === "input_required" && event.awaiting?.kind === "tool_approval") {
            void refreshApprovals();
          }
        }
      } catch (err) {
        if (!isAbort(err) && !controller.signal.aborted) setError(friendlyError(err));
      } finally {
        if (streamRef.current === controller) {
          streamRef.current = undefined;
          if (mounted.current) setStreaming(false);
        }
      }
    },
    [applySurfaces, refreshApprovals],
  );

  /** Watches a task that is running (or was just resumed) to its next stop. */
  const follow = useCallback(
    async (taskId: string) => {
      if (followingRef.current.has(taskId) || streamRef.current) return;
      followingRef.current.add(taskId);
      const controller = new AbortController();
      try {
        await run(chatClient().follow(conversationRef.current, taskId, controller.signal), controller);
      } finally {
        followingRef.current.delete(taskId);
      }
    },
    [run],
  );

  // Load everything the server already knows about this conversation.
  useEffect(() => {
    mounted.current = true;
    let cancelled = false;
    const controller = new AbortController();
    (async () => {
      setLoading(true);
      setLoadError(undefined);
      try {
        const sessionResponse = await api.sessions.getSession({ sessionId }, { signal: controller.signal }).catch(() => undefined);
        if (cancelled) return;
        if (sessionResponse?.session) setSession(sessionResponse.session);
        const ref = sessionResponse?.session?.agent?.ref;
        const conv = { id: sessionId, agent: ref ? `${ref.namespace}/${ref.name}` : fallbackAgent };
        const [history, surfaces] = await Promise.all([
          chatClient().history(conv, { signal: controller.signal }),
          api.components.listSurfaces({ sessionId }, { signal: controller.signal }).catch(() => undefined),
        ]);
        if (cancelled) return;
        for (const message of history.messages) {
          for (const part of message.parts) {
            const messages = a2uiFromPart(part);
            if (messages.length) store.apply(messages);
          }
        }
        dispatch({ type: "history", history });
        if (surfaces) {
          for (const surface of surfaces.surfaces) store.apply(a2uiFromSurface(surface));
          dispatch({ type: "surfaces", surfaces: surfaces.surfaces.map((s) => ({ surfaceId: s.surfaceId, taskId: s.taskId || undefined })) });
        }
        await refreshApprovals();
        // A turn still running when the page opened is followed live.
        const last = history.turns.at(-1);
        if (last && (last.state === "working" || last.state === "submitted")) void follow(last.taskId);
      } catch (err) {
        if (!cancelled && !isAbort(err)) setLoadError(friendlyError(err));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
      mounted.current = false;
      controller.abort();
      streamRef.current?.abort();
      streamRef.current = undefined;
    };
  }, [sessionId, fallbackAgent, store, refreshApprovals, follow]);

  const send = useCallback(
    async (text: string) => {
      const trimmed = text.trim();
      if (!trimmed || streamRef.current) return;
      const messageId = newRequestId();
      dispatch({
        type: "local-user",
        message: { id: messageId, role: "user", parts: [{ kind: "text", text: trimmed }], createdAt: new Date().toISOString() },
      });
      const controller = new AbortController();
      await run(
        chatClient().send({ conversation: conversationRef.current, text: trimmed, messageId, signal: controller.signal }),
        controller,
      );
    },
    [run],
  );

  /** ask_user questions are answered by the requester directly, in the same task. */
  const answer = useCallback(
    async (request: Extract<PendingRequest, { kind: "ask_user" }>, answers: string[][]) => {
      if (streamRef.current) return;
      const text = answerText(answers);
      const messageId = newRequestId();
      dispatch({ type: "answered", requestId: request.requestId, answers, messageId });
      const controller = new AbortController();
      await run(
        chatClient().send({
          conversation: conversationRef.current,
          text,
          messageId,
          taskId: request.taskId,
          hitl: askUserAnswer(request.requestId, answers),
          signal: controller.signal,
        }),
        controller,
      );
    },
    [run],
  );

  const stop = useCallback(async () => {
    const turn = activeTurn(state);
    streamRef.current?.abort();
    if (turn) {
      try {
        await chatClient().cancel(conversationRef.current, turn.taskId);
        dispatch({ type: "turn", taskId: turn.taskId, state: "canceled" });
      } catch (err) {
        setError(friendlyError(err));
      }
    }
  }, [state]);

  /** An approval card saw its approval decided: the agent resumes, so follow the task. */
  const onApprovalSettled = useCallback(
    (approval: Approval | string) => {
      const record = typeof approval === "string" ? approvals[approval] : approval;
      if (typeof approval !== "string") setApprovals((prev) => ({ ...prev, [approval.id]: approval }));
      const taskId = record?.taskId || state.awaiting?.taskId;
      if (!taskId) return;
      if (record && record.state === ApprovalState.SUPERSEDED) return;
      void follow(taskId);
    },
    [approvals, follow, state.awaiting],
  );

  return {
    state,
    store,
    session,
    agent,
    loading,
    loadError,
    streaming,
    error,
    approvals,
    send,
    answer,
    stop,
    onApprovalSettled,
    dismissError: () => setError(undefined),
  };
}
