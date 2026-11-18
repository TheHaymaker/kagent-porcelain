/** Structured data shown as text. Never parsed as HTML. */
export function JsonView({ value, label }: { value: unknown; label?: string }) {
  let text: string;
  try {
    text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
  } catch {
    text = String(value);
  }
  return (
    <pre className="json" aria-label={label} tabIndex={0}>
      {text}
    </pre>
  );
}
