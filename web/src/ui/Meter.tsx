export function Meter({ value, max = 100, label, tone }: { value: number; max?: number; label: string; tone?: "accent" | "warning" | "danger" | "success" }) {
  const pct = max > 0 ? Math.max(0, Math.min(100, (value / max) * 100)) : 0;
  const computedTone = tone ?? (pct >= 100 ? "danger" : pct >= 80 ? "warning" : "accent");
  return (
    <div
      className={`meter meter-${computedTone}`}
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={max}
      aria-valuenow={Math.round(value)}
    >
      <div className="meter-fill" style={{ width: `${pct}%` }} />
    </div>
  );
}
