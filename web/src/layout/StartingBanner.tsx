import { useEffect, useState } from "react";
import { isStarting, onStarting, serverIsUp } from "@/api/starting";
import { config } from "@/config";

const POLL_MS = 15_000;

/**
 * Shown while Workbench's servers start after an idle pause: says what is
 * happening, checks every few seconds, and reloads the page once the server
 * answers, so the person does not have to.
 */
export function StartingBanner() {
  const [starting, setStarting] = useState(isStarting);
  useEffect(() => onStarting(setStarting), []);
  useEffect(() => {
    if (!starting) return;
    const timer = window.setInterval(async () => {
      if (await serverIsUp(config().apiBaseUrl)) window.location.reload();
    }, POLL_MS);
    return () => window.clearInterval(timer);
  }, [starting]);
  if (!starting) return null;
  return (
    <div className="starting-banner" role="status">
      <span className="dots" aria-hidden>
        <span />
        <span />
        <span />
      </span>
      <span>
        <strong>Workbench is starting up</strong> after a quiet spell. This takes about 3 to 5 minutes; the page will
        continue on its own.
      </span>
    </div>
  );
}
