import itertools

import pytest

from app.store import FirestoreStore, MemoryStore, make_store


class FakeDoc:
    def __init__(self, col, doc_id):
        self._col, self.id = col, doc_id

    @property
    def exists(self):
        return self.id in self._col.docs

    def to_dict(self):
        return dict(self._col.docs[self.id]) if self.exists else None

    # DocumentReference methods
    def set(self, data):
        self._col.docs[self.id] = dict(data)

    def get(self):
        return self

    def update(self, data):
        self._col.docs[self.id].update(data)


class FakeCollection:
    """The part of google.cloud.firestore's collection API FirestoreStore uses."""

    def __init__(self):
        self.docs = {}
        self._ids = (f"doc{i}" for i in itertools.count(1))

    def document(self, doc_id=None):
        return FakeDoc(self, doc_id or next(self._ids))

    def stream(self):
        return [FakeDoc(self, i) for i in list(self.docs)]


class FakeClient:
    def __init__(self):
        self.collections = {}

    def collection(self, name):
        return self.collections.setdefault(name, FakeCollection())


@pytest.fixture(params=["memory", "firestore"])
def store(request):
    if request.param == "memory":
        return MemoryStore()
    return FirestoreStore(FakeClient(), "workbench-app-items")


def test_add_list_toggle(store):
    first = store.add("  Buy milk ", created_by="alice@example.com")
    store.add("Walk the cat", created_by="bob@example.com")
    assert first.title == "Buy milk"
    assert [i.title for i in store.list()] == ["Walk the cat", "Buy milk"]
    assert store.toggle(first.id).done is True
    assert store.toggle(first.id).done is False


def test_validation_and_unknown_items(store):
    with pytest.raises(ValueError):
        store.add("   ", created_by="alice@example.com")
    with pytest.raises(ValueError):
        store.add("x" * 201, created_by="alice@example.com")
    with pytest.raises(KeyError):
        store.toggle("missing")


def test_firestore_store_uses_the_named_collection():
    client = FakeClient()
    FirestoreStore(client, "team-todo-items").add("A", created_by="alice@example.com")
    assert list(client.collections) == ["team-todo-items"]
    doc = next(iter(client.collections["team-todo-items"].docs.values()))
    assert doc["created_by"] == "alice@example.com" and doc["done"] is False


def test_make_store_reads_app_store(monkeypatch):
    monkeypatch.delenv("APP_STORE", raising=False)
    assert isinstance(make_store("workbench-app"), MemoryStore)
    monkeypatch.setenv("APP_STORE", "nonsense")
    with pytest.raises(ValueError):
        make_store("workbench-app")
