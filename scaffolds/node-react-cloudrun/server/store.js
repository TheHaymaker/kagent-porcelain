// In-memory store: resets on restart and is per instance. Replace with a
// persistent implementation of the same methods when the data matters
// (scaffold.yaml `state.persistent`).
export class MemoryStore {
  #items = new Map();
  #nextId = 1;

  list() {
    return [...this.#items.values()].sort((a, b) => b.id - a.id);
  }

  add(title, createdBy) {
    const clean = String(title ?? '').trim();
    if (!clean) throw new ValidationError('title must not be empty');
    if (clean.length > 200) throw new ValidationError('title must be at most 200 characters');
    const item = { id: this.#nextId++, title: clean, createdBy, createdAt: new Date().toISOString(), done: false };
    this.#items.set(item.id, item);
    return item;
  }

  toggle(id) {
    const item = this.#items.get(id);
    if (!item) return undefined;
    item.done = !item.done;
    return item;
  }
}

export class ValidationError extends Error {}
