import pytest
from fastapi.testclient import TestClient

from app.main import create_app
from app.store import MemoryStore

IAP_ALICE = {"X-Goog-Authenticated-User-Email": "accounts.google.com:alice@example.com"}


@pytest.fixture(autouse=True)
def _no_ambient_identity(monkeypatch):
    for name in ("IAP_AUDIENCE", "LOCAL_DEV_USER", "K_SERVICE"):
        monkeypatch.delenv(name, raising=False)


@pytest.fixture
def client():
    return TestClient(create_app(MemoryStore()))
