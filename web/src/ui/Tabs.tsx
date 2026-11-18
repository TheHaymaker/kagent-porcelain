import { useRef, type KeyboardEvent, type ReactNode } from "react";

export interface TabItem<K extends string> {
  key: K;
  label: ReactNode;
  count?: number;
}

/** WAI-ARIA tabs: arrow keys move between tabs, the panel is labelled by its tab. */
export function Tabs<K extends string>({
  items,
  value,
  onChange,
  label,
  idPrefix,
}: {
  items: TabItem<K>[];
  value: K;
  onChange: (key: K) => void;
  label: string;
  idPrefix: string;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const onKey = (event: KeyboardEvent, index: number) => {
    let next: number;
    if (event.key === "ArrowRight") next = (index + 1) % items.length;
    else if (event.key === "ArrowLeft") next = (index - 1 + items.length) % items.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = items.length - 1;
    else return;
    event.preventDefault();
    const item = items[next];
    if (item) {
      onChange(item.key);
      refs.current[next]?.focus();
    }
  };
  return (
    <div className="tabs" role="tablist" aria-label={label}>
      {items.map((item, index) => {
        const selected = item.key === value;
        return (
          <button
            key={item.key}
            ref={(el) => {
              refs.current[index] = el;
            }}
            id={`${idPrefix}-tab-${item.key}`}
            role="tab"
            type="button"
            aria-selected={selected}
            aria-controls={`${idPrefix}-panel`}
            tabIndex={selected ? 0 : -1}
            className="tab"
            onClick={() => onChange(item.key)}
            onKeyDown={(e) => onKey(e, index)}
          >
            {item.label}
            {item.count !== undefined && item.count > 0 && <span className="tab-count">{item.count}</span>}
          </button>
        );
      })}
    </div>
  );
}
