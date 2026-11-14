import pytest
from fastapi import HTTPException
from starlette.requests import Request

from app import identity


def make_request(headers: dict[str, str]) -> Request:
    raw = [(k.lower().encode(), v.encode()) for k, v in headers.items()]
    return Request({"type": "http", "headers": raw})


@pytest.mark.parametrize(
    ("value", "expected"),
    [
        ("accounts.google.com:alice@example.com", "alice@example.com"),
        ("bob@example.com", "bob@example.com"),
        ("accounts.google.com:not-an-email", None),
        ("", None),
        (None, None),
    ],
)
def test_parse_email_header(value, expected):
    assert identity.parse_email_header(value) == expected


def test_jwt_required_when_audience_configured(monkeypatch):
    monkeypatch.setenv("IAP_AUDIENCE", "/projects/1/locations/europe-west1/services/app")
    # The plain email header alone is not enough once verification is on.
    request = make_request({"X-Goog-Authenticated-User-Email": "accounts.google.com:a@example.com"})
    with pytest.raises(HTTPException) as err:
        identity.current_user(request)
    assert err.value.status_code == 401


def test_verified_jwt_email_is_used(monkeypatch):
    monkeypatch.setenv("IAP_AUDIENCE", "aud")
    monkeypatch.setattr(identity, "_verify_jwt", lambda token, audience: "carol@example.com")
    request = make_request({"X-Goog-IAP-JWT-Assertion": "token"})
    assert identity.current_user(request).email == "carol@example.com"


def test_invalid_jwt_is_401(monkeypatch):
    monkeypatch.setenv("IAP_AUDIENCE", "aud")

    def reject(token, audience):
        raise ValueError("bad signature")

    monkeypatch.setattr(identity, "_verify_jwt", reject)
    with pytest.raises(HTTPException) as err:
        identity.current_user(make_request({"X-Goog-IAP-JWT-Assertion": "token"}))
    assert err.value.status_code == 401


def test_local_dev_user_only_off_cloud_run(monkeypatch):
    monkeypatch.setenv("LOCAL_DEV_USER", "dev@example.com")
    assert identity.current_user(make_request({})).email == "dev@example.com"
    monkeypatch.setenv("K_SERVICE", "workbench-app")
    with pytest.raises(HTTPException):
        identity.current_user(make_request({}))
