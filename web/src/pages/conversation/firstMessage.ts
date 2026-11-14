/** The prompt typed on Home, handed to the conversation page that sends it. */
const queued = new Map<string, string>();

export function queueFirstMessage(sessionId: string, text: string): void {
  queued.set(sessionId, text);
}

export function takeFirstMessage(sessionId: string): string | undefined {
  const text = queued.get(sessionId);
  queued.delete(sessionId);
  return text;
}
