import { useCallback, useEffect, useRef, useState } from "react";
import { friendlyError, isAbort } from "./errors";

export interface RpcState<T> {
  data: T | undefined;
  error: string | undefined;
  loading: boolean;
  reload: () => void;
  setData: (data: T) => void;
}

/**
 * Loads one server read for a component, with optional polling.
 * `deps` decide when it re-runs; `pollMs` keeps it fresh while mounted.
 */
export function useRpc<T>(
  load: (signal: AbortSignal) => Promise<T>,
  deps: readonly unknown[],
  options: { pollMs?: number; enabled?: boolean } = {},
): RpcState<T> {
  const { pollMs, enabled = true } = options;
  const [data, setData] = useState<T>();
  const [error, setError] = useState<string>();
  const [loading, setLoading] = useState(enabled);
  const [tick, setTick] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    if (!enabled) {
      setLoading(false);
      return;
    }
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const run = async (first: boolean) => {
      if (first) setLoading(true);
      try {
        const result = await loadRef.current(controller.signal);
        if (controller.signal.aborted) return;
        setData(result);
        setError(undefined);
      } catch (err) {
        if (controller.signal.aborted || isAbort(err)) return;
        setError(friendlyError(err));
      } finally {
        if (!controller.signal.aborted) {
          setLoading(false);
          if (pollMs) timer = setTimeout(() => void run(false), pollMs);
        }
      }
    };
    void run(true);
    return () => {
      controller.abort();
      if (timer) clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick, pollMs, enabled]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { data, error, loading, reload, setData };
}
