"""Where the app keeps its records.

The scaffold ships an in-memory store: simple, testable, and reset on every
restart (Cloud Run may run several instances, each with its own copy). Replace
``MemoryStore`` with a persistent implementation of the same methods when the
data matters (scaffold.yaml ``state.persistent``).
"""

from __future__ import annotations

import itertools
import threading
from dataclasses import dataclass, field
from datetime import UTC, datetime


@dataclass
class Item:
    id: int
    title: str
    created_by: str
    created_at: datetime = field(default_factory=lambda: datetime.now(UTC))
    done: bool = False


class MemoryStore:
    def __init__(self) -> None:
        self._items: dict[int, Item] = {}
        self._ids = itertools.count(1)
        self._lock = threading.Lock()

    def list(self) -> list[Item]:
        with self._lock:
            return sorted(self._items.values(), key=lambda i: i.created_at, reverse=True)

    def add(self, title: str, created_by: str) -> Item:
        title = title.strip()
        if not title:
            raise ValueError("title must not be empty")
        if len(title) > 200:
            raise ValueError("title must be at most 200 characters")
        with self._lock:
            item = Item(id=next(self._ids), title=title, created_by=created_by)
            self._items[item.id] = item
            return item

    def toggle(self, item_id: int) -> Item:
        with self._lock:
            item = self._items[item_id]
            item.done = not item.done
            return item
