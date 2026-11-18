"""Who is signed in, from Identity-Aware Proxy.

IAP authenticates every request before it reaches Cloud Run and adds:

- ``X-Goog-Authenticated-User-Email``: ``accounts.google.com:alice@example.com``
- ``X-Goog-IAP-JWT-Assertion``: a signed JWT for the same user.

The plain header is only trustworthy if nothing but IAP can reach the service
(IAM.md: IAP enabled, no unauthenticated invoker). Set ``IAP_AUDIENCE`` to also
verify the signed JWT, which is the recommended production setting.
For local development only, ``LOCAL_DEV_USER`` stands in for IAP.
"""

from __future__ import annotations

import os
from dataclasses import dataclass

from fastapi import HTTPException, Request

EMAIL_HEADER = "x-goog-authenticated-user-email"
JWT_HEADER = "x-goog-iap-jwt-assertion"
IAP_CERTS_URL = "https://www.gstatic.com/iap/verify/public_key"


@dataclass(frozen=True)
class User:
    email: str

    @property
    def display_name(self) -> str:
        return self.email.split("@", 1)[0]


def parse_email_header(value: str | None) -> str | None:
    if not value:
        return None
    # "accounts.google.com:alice@example.com" -> "alice@example.com"
    email = value.split(":", 1)[1] if ":" in value else value
    email = email.strip()
    return email if "@" in email else None


def _verify_jwt(token: str, audience: str) -> str:
    # Imported lazily so tests and local runs do not need network access.
    from google.auth.transport import requests as google_requests
    from google.oauth2 import id_token

    claims = id_token.verify_token(
        token, google_requests.Request(), audience=audience, certs_url=IAP_CERTS_URL
    )
    email = claims.get("email")
    if not email:
        raise ValueError("IAP JWT has no email claim")
    return email


def current_user(request: Request) -> User:
    """FastAPI dependency: the signed-in user, or 401."""
    audience = os.environ.get("IAP_AUDIENCE")
    if audience:
        token = request.headers.get(JWT_HEADER)
        if not token:
            raise HTTPException(status_code=401, detail="Sign-in required")
        try:
            return User(email=_verify_jwt(token, audience))
        except Exception as exc:  # noqa: BLE001 - any verification failure is a 401
            raise HTTPException(status_code=401, detail="Sign-in could not be verified") from exc

    email = parse_email_header(request.headers.get(EMAIL_HEADER))
    if email:
        return User(email=email)

    local = os.environ.get("LOCAL_DEV_USER")
    if local and os.environ.get("K_SERVICE") is None:  # never on Cloud Run
        return User(email=local)

    raise HTTPException(status_code=401, detail="Sign-in required")
