/**
 * A deliberately small Markdown subset for Text(body): paragraphs, bullet and
 * numbered lists, **bold**, *italic*, `code` and [links](https://…). Output is
 * React elements built from text — nothing is ever parsed as HTML, and only
 * https links become anchors.
 */
import type { ReactNode } from "react";
import { safeHttpsUrl } from "@/lib/format";

const INLINE = /(\*\*([^*]+)\*\*|\*([^*]+)\*|_([^_]+)_|`([^`]+)`|\[([^\]]+)\]\(([^)\s]+)\))/g;

export function renderInline(text: string, keyPrefix = "i"): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let index = 0;
  for (const match of text.matchAll(INLINE)) {
    const start = match.index ?? 0;
    if (start > last) out.push(text.slice(last, start));
    const key = `${keyPrefix}-${index++}`;
    if (match[2] !== undefined) out.push(<strong key={key}>{match[2]}</strong>);
    else if (match[3] !== undefined) out.push(<em key={key}>{match[3]}</em>);
    else if (match[4] !== undefined) out.push(<em key={key}>{match[4]}</em>);
    else if (match[5] !== undefined) out.push(<code key={key}>{match[5]}</code>);
    else if (match[6] !== undefined) {
      const href = safeHttpsUrl(match[7]);
      out.push(
        href ? (
          <a key={key} href={href} target="_blank" rel="noopener noreferrer">
            {match[6]}
          </a>
        ) : (
          <span key={key}>{match[6]}</span>
        ),
      );
    }
    last = start + match[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

export function Markdown({ text }: { text: string }) {
  const blocks = text.replace(/\r\n/g, "\n").split(/\n{2,}/);
  return (
    <div className="md">
      {blocks.map((block, b) => {
        const lines = block.split("\n").filter((l) => l.trim() !== "");
        if (lines.length === 0) return null;
        if (lines.every((l) => /^\s*[-*]\s+/.test(l))) {
          return (
            <ul key={b}>
              {lines.map((l, i) => (
                <li key={i}>{renderInline(l.replace(/^\s*[-*]\s+/, ""), `${b}-${i}`)}</li>
              ))}
            </ul>
          );
        }
        if (lines.every((l) => /^\s*\d+[.)]\s+/.test(l))) {
          return (
            <ol key={b}>
              {lines.map((l, i) => (
                <li key={i}>{renderInline(l.replace(/^\s*\d+[.)]\s+/, ""), `${b}-${i}`)}</li>
              ))}
            </ol>
          );
        }
        const heading = /^(#{1,3})\s+(.*)$/.exec(lines[0] ?? "");
        if (heading && lines.length === 1) {
          return (
            <p key={b} className="md-heading">
              <strong>{renderInline(heading[2] ?? "", `${b}`)}</strong>
            </p>
          );
        }
        return (
          <p key={b}>
            {lines.map((l, i) => (
              <span key={i}>
                {i > 0 && <br />}
                {renderInline(l, `${b}-${i}`)}
              </span>
            ))}
          </p>
        );
      })}
    </div>
  );
}
