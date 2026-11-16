"""Where the app keeps its records.

Two stores with the same methods:

- ``MemoryStore``: simple and testable, but reset on every restart, and each
  Cloud Run instance has its own copy. Fine for demos.
- ``FirestoreStore``: Firestore in Native mode, for data that must survive
  restarts. It uses the credentials Cloud Run gives the app (no keys) and
  keeps everything in collections named after the app.

``make_store()`` picks one from ``APP_STORE`` ("memory" or "firestore"),
falling back to ``DEFAULT_STORE``. Change ``DEFAULT_STORE`` to "firestore"
when the app's data must persist; tests keep using ``MemoryStore``.
"""

from __future__ import annotations

import itertools
import os
import threading
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Any, Protocol

DEFAULT_STORE = "memory"


@dataclass
class Item:
    id: str
    title: str
    created_by: str
    created_at: datetime = field(default_factory=lambda: datetime.now(UTC))
    done: bool = False


def _check_title(title: str) -> str:
    title = title.strip()
    if not title:
        raise ValueError("title must not be empty")
    if len(title) > 200:
        raise ValueError("title must be at most 200 characters")
    return title


class Store(Protocol):
    def list(self) -> list[Item]: ...
    def add(self, title: str, created_by: str) -> Item: ...
    def toggle(self, item_id: str) -> Item: ...


class MemoryStore:
    def __init__(self) -> None:
        self._items: dict[str, Item] = {}
        self._ids = itertools.count(1)
        self._lock = threading.Lock()

    def list(self) -> list[Item]:
        with self._lock:
            return sorted(self._items.values(), key=lambda i: i.created_at, reverse=True)

    def add(self, title: str, created_by: str) -> Item:
        title = _check_title(title)
        with self._lock:
            item = Item(id=str(next(self._ids)), title=title, created_by=created_by)
            self._items[item.id] = item
            return item

    def toggle(self, item_id: str) -> Item:
        with self._lock:
            item = self._items[item_id]
            item.done = not item.done
            return item


class FirestoreStore:
    """Items as documents in one Firestore collection.

    ``client`` is a ``google.cloud.firestore.Client`` (or anything with the
    same ``collection`` API, as in the tests). ``collection`` must start with
    the app's name: every app shares the project's ``(default)`` database.
    """

    def __init__(self, client: Any, collection: str) -> None:
        self._col = client.collection(collection)

    @staticmethod
    def _item(doc: Any) -> Item:
        d = doc.to_dict() or {}
        return Item(
            id=doc.id,
            title=d.get("title", ""),
            created_by=d.get("created_by", ""),
            created_at=d.get("created_at") or datetime.now(UTC),
            done=bool(d.get("done", False)),
        )

    def list(self) -> list[Item]:
        items = [self._item(doc) for doc in self._col.stream()]
        return sorted(items, key=lambda i: i.created_at, reverse=True)

    def add(self, title: str, created_by: str) -> Item:
        title = _check_title(title)
        ref = self._col.document()
        now = datetime.now(UTC)
        ref.set({"title": title, "created_by": created_by, "created_at": now, "done": False})
        return Item(id=ref.id, title=title, created_by=created_by, created_at=now)

    def toggle(self, item_id: str) -> Item:
        ref = self._col.document(item_id)
        doc = ref.get()
        if not doc.exists:
            raise KeyError(item_id)
        item = self._item(doc)
        item.done = not item.done
        ref.update({"done": item.done})
        return item


def make_store(app_name: str) -> Store:
    """The store ``APP_STORE`` (or ``DEFAULT_STORE``) names."""
    kind = os.environ.get("APP_STORE", DEFAULT_STORE).strip().lower()
    if kind == "memory":
        return MemoryStore()
    if kind == "firestore":
        # Imported here so tests and local runs need no Google credentials.
        from google.cloud import firestore

        client = firestore.Client(project=os.environ.get("GOOGLE_CLOUD_PROJECT") or None)
        return FirestoreStore(client, f"{app_name}-items")
    raise ValueError(f"APP_STORE must be 'memory' or 'firestore', not {kind!r}")
