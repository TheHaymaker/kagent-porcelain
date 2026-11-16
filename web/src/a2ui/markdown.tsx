/**
 * A deliberately small Markdown subset for Text(body): paragraphs, bullet and
 * numbered lists, **bold**, *italic*, `code`, [links](https://…) and bare
 * https://… addresses; bold and italic may contain the others. Output is
 * React elements built from text — nothing is ever parsed as HTML, and only
 * https links become anchors.
 */
import type { ReactNode } from "react";
import { safeHttpsUrl } from "@/lib/format";

const INLINE =
  /(\*\*([^*]+)\*\*|\*([^*]+)\*|_([^_]+)_|`([^`]+)`|\[([^\]]+)\]\(([^)\s]+)\)|(https:\/\/[^\s<>()[\]*`]+[^\s<>()[\]*`.,;:!?'"]))/g;

export function renderInline(text: string, keyPrefix = "i"): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let index = 0;
  for (const match of text.matchAll(INLINE)) {
    const start = match.index ?? 0;
    if (start > last) out.push(text.slice(last, start));
    const key = `${keyPrefix}-${index++}`;
    if (match[2] !== undefined)
      out.push(<strong key={key}>{renderInline(match[2], key)}</strong>);
    else if (match[3] !== undefined)
      out.push(<em key={key}>{renderInline(match[3], key)}</em>);
    else if (match[4] !== undefined)
      out.push(<em key={key}>{renderInline(match[4], key)}</em>);
    else if (match[5] !== undefined)
      out.push(<code key={key}>{match[5]}</code>);
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
    } else if (match[8] !== undefined) {
      const href = safeHttpsUrl(match[8]);
      out.push(
        href ? (
          <a key={key} href={href} target="_blank" rel="noopener noreferrer">
            {match[8]}
          </a>
        ) : (
          match[8]
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
                <li key={i}>
                  {renderInline(l.replace(/^\s*[-*]\s+/, ""), `${b}-${i}`)}
                </li>
              ))}
            </ul>
          );
        }
        if (lines.every((l) => /^\s*\d+[.)]\s+/.test(l))) {
          return (
            <ol key={b}>
              {lines.map((l, i) => (
                <li key={i}>
                  {renderInline(l.replace(/^\s*\d+[.)]\s+/, ""), `${b}-${i}`)}
                </li>
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
