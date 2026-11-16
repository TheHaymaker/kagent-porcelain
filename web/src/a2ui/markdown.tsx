/**
 * A deliberately small Markdown subset for agent messages and Text(body):
 * headings, paragraphs, bullet and numbered lists (nested by indentation),
 * block quotes, rules, fenced code and simple tables; inline **bold**,
 * *italic*, `code`, [links](https://…) and bare https://… addresses, where
 * bold and italic may contain the others. Output is React elements built from
 * text — nothing is ever parsed as HTML, and only https links become anchors.
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

type ListKind = "ul" | "ol";
interface ListItem {
  text: string[];
  children?: ListBlock;
}
interface ListBlock {
  type: "list";
  kind: ListKind;
  start: number;
  items: ListItem[];
}
type Block =
  | { type: "heading"; level: number; text: string }
  | { type: "paragraph"; lines: string[] }
  | { type: "quote"; lines: string[] }
  | { type: "code"; text: string }
  | { type: "rule" }
  | { type: "table"; head: string[]; rows: string[][] }
  | ListBlock;

const HEADING = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const RULE = /^\s{0,3}([-*_])(\s*\1){2,}\s*$/;
const FENCE = /^\s{0,3}(```|~~~)/;
const QUOTE = /^\s{0,3}>\s?(.*)$/;
const ITEM = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/;
const TABLE_SEP = /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/;

function indentOf(s: string): number {
  return (s.match(/^\s*/)?.[0] ?? "").replace(/\t/g, "  ").length;
}

function cells(row: string): string[] {
  return row
    .trim()
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split("|")
    .map((c) => c.trim());
}

/** Reads a list starting at lines[i]; returns the block and the next line. */
function parseList(lines: string[], i: number): [ListBlock, number] {
  const first = ITEM.exec(lines[i]!)!;
  const base = indentOf(first[1] ?? "");
  const ordered = /\d/.test(first[2] ?? "");
  const list: ListBlock = {
    type: "list",
    kind: ordered ? "ol" : "ul",
    start: ordered ? parseInt(first[2] ?? "1", 10) : 1,
    items: [],
  };
  while (i < lines.length) {
    const line = lines[i]!;
    if (line.trim() === "") {
      // A blank line ends the list unless more of it follows, indented or as an item.
      const next = lines[i + 1];
      if (next !== undefined && (ITEM.test(next) ? indentOf(next) >= base : indentOf(next) > base)) {
        i++;
        continue;
      }
      break;
    }
    const m = ITEM.exec(line);
    const indent = indentOf(line);
    if (m && indent <= base + 1) {
      if (/\d/.test(m[2] ?? "") !== ordered && list.items.length > 0) break;
      list.items.push({ text: [m[3] ?? ""] });
      i++;
      continue;
    }
    const last = list.items[list.items.length - 1];
    if (!last) break;
    if (m && indent > base) {
      const [child, next] = parseList(lines, i);
      if (last.children) last.children.items.push(...child.items);
      else last.children = child;
      i = next;
      continue;
    }
    if (indent > base || !(HEADING.test(line) || FENCE.test(line) || QUOTE.test(line) || RULE.test(line))) {
      // A continuation of the item's text (lazy continuation, as in CommonMark).
      if (indent <= base && m) break;
      last.text.push(line.trim());
      i++;
      continue;
    }
    break;
  }
  return [list, i];
}

export function parseBlocks(text: string): Block[] {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i]!;
    if (line.trim() === "") {
      i++;
      continue;
    }
    const fence = FENCE.exec(line);
    if (fence) {
      const body: string[] = [];
      i++;
      while (i < lines.length && !lines[i]!.trim().startsWith(fence[1]!)) body.push(lines[i++]!);
      i++;
      blocks.push({ type: "code", text: body.join("\n") });
      continue;
    }
    const heading = HEADING.exec(line);
    if (heading) {
      blocks.push({ type: "heading", level: heading[1]!.length, text: heading[2] ?? "" });
      i++;
      continue;
    }
    if (RULE.test(line)) {
      blocks.push({ type: "rule" });
      i++;
      continue;
    }
    if (QUOTE.test(line)) {
      const body: string[] = [];
      while (i < lines.length && QUOTE.test(lines[i]!)) body.push(QUOTE.exec(lines[i++]!)![1] ?? "");
      blocks.push({ type: "quote", lines: body });
      continue;
    }
    if (ITEM.test(line)) {
      const [list, next] = parseList(lines, i);
      blocks.push(list);
      i = next;
      continue;
    }
    if (line.includes("|") && i + 1 < lines.length && TABLE_SEP.test(lines[i + 1]!) && lines[i + 1]!.includes("-")) {
      const head = cells(line);
      const rows: string[][] = [];
      i += 2;
      while (i < lines.length && lines[i]!.includes("|") && lines[i]!.trim() !== "") rows.push(cells(lines[i++]!));
      blocks.push({ type: "table", head, rows });
      continue;
    }
    const para: string[] = [];
    while (
      i < lines.length &&
      lines[i]!.trim() !== "" &&
      !(HEADING.test(lines[i]!) || FENCE.test(lines[i]!) || QUOTE.test(lines[i]!) || RULE.test(lines[i]!) || ITEM.test(lines[i]!))
    ) {
      para.push(lines[i++]!.trim());
    }
    blocks.push({ type: "paragraph", lines: para });
  }
  return blocks;
}

function Lines({ lines, k }: { lines: string[]; k: string }) {
  return (
    <>
      {lines.map((l, i) => (
        <span key={i}>
          {i > 0 && <br />}
          {renderInline(l, `${k}-${i}`)}
        </span>
      ))}
    </>
  );
}

function ListView({ list, k }: { list: ListBlock; k: string }) {
  const items = list.items.map((item, i) => (
    <li key={i}>
      <Lines lines={item.text} k={`${k}-${i}`} />
      {item.children && <ListView list={item.children} k={`${k}-${i}c`} />}
    </li>
  ));
  return list.kind === "ol" ? <ol start={list.start === 1 ? undefined : list.start}>{items}</ol> : <ul>{items}</ul>;
}

export function Markdown({ text }: { text: string }) {
  return (
    <div className="md">
      {parseBlocks(text).map((block, b) => {
        const k = String(b);
        switch (block.type) {
          case "heading": {
            // Messages sit inside a page that has its own h1/h2.
            const Tag = (["h3", "h4", "h5", "h6"] as const)[Math.min(block.level, 4) - 1]!;
            return (
              <Tag key={b} className={`md-h md-h${Math.min(block.level, 4)}`}>
                {renderInline(block.text, k)}
              </Tag>
            );
          }
          case "rule":
            return <hr key={b} />;
          case "code":
            return (
              <pre key={b}>
                <code>{block.text}</code>
              </pre>
            );
          case "quote":
            return (
              <blockquote key={b}>
                <Lines lines={block.lines} k={k} />
              </blockquote>
            );
          case "list":
            return <ListView key={b} list={block} k={k} />;
          case "table":
            return (
              <div key={b} className="md-table">
                <table>
                  <thead>
                    <tr>
                      {block.head.map((c, i) => (
                        <th key={i}>{renderInline(c, `${k}-h${i}`)}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {block.rows.map((r, ri) => (
                      <tr key={ri}>
                        {block.head.map((_, ci) => (
                          <td key={ci}>{renderInline(r[ci] ?? "", `${k}-${ri}-${ci}`)}</td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            );
          default:
            return (
              <p key={b}>
                <Lines lines={block.lines} k={k} />
              </p>
            );
        }
      })}
    </div>
  );
}
