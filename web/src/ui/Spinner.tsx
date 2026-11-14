export function Spinner({ size = 16, label }: { size?: number; label?: string }) {
  return (
    <span className="spinner" style={{ width: size, height: size }} role={label ? "status" : undefined} aria-label={label}>
      {label ? <span className="sr-only">{label}</span> : null}
    </span>
  );
}
