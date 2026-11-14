import pytest

from app import identity


@pytest.fixture(autouse=True)
def _clean(monkeypatch):
    for name in ("IAP_AUDIENCE", "K_SERVICE", "LOCAL_DEV_USER"):
        monkeypatch.delenv(name, raising=False)


def test_email_from_iap_header():
    headers = {"X-Goog-Authenticated-User-Email": "accounts.google.com:ann@example.com"}
    assert identity.user_email(headers) == "ann@example.com"


def test_lowercase_header_names_work():
    headers = {"x-goog-authenticated-user-email": "accounts.google.com:ann@example.com"}
    assert identity.user_email(headers) == "ann@example.com"


def test_jwt_is_required_when_audience_set(monkeypatch):
    monkeypatch.setenv("IAP_AUDIENCE", "aud")
    with pytest.raises(identity.NotSignedIn):
        identity.user_email({"X-Goog-Authenticated-User-Email": "accounts.google.com:a@b.c"})
    monkeypatch.setattr(identity, "_verify_jwt", lambda token, audience: "v@example.com")
    assert identity.user_email({"X-Goog-IAP-JWT-Assertion": "t"}) == "v@example.com"


def test_local_dev_user_never_on_cloud_run(monkeypatch):
    monkeypatch.setenv("LOCAL_DEV_USER", "dev@example.com")
    assert identity.user_email({}) == "dev@example.com"
    monkeypatch.setenv("K_SERVICE", "svc")
    with pytest.raises(identity.NotSignedIn):
        identity.user_email({})
