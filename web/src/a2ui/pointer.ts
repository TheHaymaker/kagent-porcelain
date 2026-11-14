/** RFC 6901 JSON Pointer reads and immutable writes for the A2UI data model. */

export function parsePointer(pointer: string): string[] | undefined {
  if (pointer === "" || pointer === "/") return [];
  if (!pointer.startsWith("/")) return undefined;
  return pointer
    .slice(1)
    .split("/")
    .map((token) => token.replace(/~1/g, "/").replace(/~0/g, "~"));
}

const FORBIDDEN = new Set(["__proto__", "prototype", "constructor"]);

export function getAt(root: unknown, pointer: string): unknown {
  const tokens = parsePointer(pointer);
  if (!tokens) return undefined;
  let current: unknown = root;
  for (const token of tokens) {
    if (current === null || typeof current !== "object") return undefined;
    if (FORBIDDEN.has(token)) return undefined;
    if (Array.isArray(current)) {
      if (!/^\d+$/.test(token)) return undefined;
      current = current[Number(token)];
    } else {
      if (!Object.prototype.hasOwnProperty.call(current, token)) return undefined;
      current = (current as Record<string, unknown>)[token];
    }
  }
  return current;
}

/**
 * Returns a copy of `root` with `value` at `pointer`, creating objects on the
 * way. `value === undefined` removes the entry. An empty pointer replaces the root.
 */
export function setAt(root: unknown, pointer: string, value: unknown): unknown {
  const tokens = parsePointer(pointer);
  if (!tokens) return root;
  if (tokens.some((t) => FORBIDDEN.has(t))) return root;
  if (tokens.length === 0) return value === undefined ? {} : value;
  return write(root, tokens, value);
}

function write(node: unknown, tokens: string[], value: unknown): unknown {
  const [head, ...rest] = tokens as [string, ...string[]];
  if (Array.isArray(node) && /^\d+$|^-$/.test(head)) {
    const copy = [...node];
    const index = head === "-" ? copy.length : Number(head);
    if (rest.length === 0) {
      if (value === undefined) copy.splice(index, 1);
      else copy[index] = value;
    } else {
      copy[index] = write(copy[index], rest, value);
    }
    return copy;
  }
  const base: Record<string, unknown> =
    node !== null && typeof node === "object" && !Array.isArray(node) ? { ...(node as Record<string, unknown>) } : {};
  if (rest.length === 0) {
    if (value === undefined) delete base[head];
    else base[head] = value;
  } else {
    base[head] = write(base[head], rest, value);
  }
  return base;
}
