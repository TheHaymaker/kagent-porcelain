"""Who is signed in, from Identity-Aware Proxy (see IAM.md).

Streamlit exposes the request headers of the session's initial request through
``st.context.headers``. IAP adds ``X-Goog-Authenticated-User-Email`` and the
signed ``X-Goog-IAP-JWT-Assertion``. Set ``IAP_AUDIENCE`` to verify the JWT
(recommended). ``LOCAL_DEV_USER`` stands in for IAP on local runs only.
"""

from __future__ import annotations

import os
from collections.abc import Mapping

EMAIL_HEADER = "X-Goog-Authenticated-User-Email"
JWT_HEADER = "X-Goog-IAP-JWT-Assertion"
IAP_CERTS_URL = "https://www.gstatic.com/iap/verify/public_key"


class NotSignedIn(Exception):
    pass


def parse_email_header(value: str | None) -> str | None:
    if not value:
        return None
    email = (value.split(":", 1)[1] if ":" in value else value).strip()
    return email if "@" in email else None


def _verify_jwt(token: str, audience: str) -> str:
    from google.auth.transport import requests as google_requests
    from google.oauth2 import id_token

    claims = id_token.verify_token(
        token, google_requests.Request(), audience=audience, certs_url=IAP_CERTS_URL
    )
    email = claims.get("email")
    if not email:
        raise ValueError("IAP JWT has no email claim")
    return email


def _get(headers: Mapping[str, str], name: str) -> str | None:
    # st.context.headers is case-insensitive; plain dicts in tests may not be.
    value = headers.get(name)
    if value is None:
        value = headers.get(name.lower())
    return value


def user_email(headers: Mapping[str, str]) -> str:
    audience = os.environ.get("IAP_AUDIENCE")
    if audience:
        token = _get(headers, JWT_HEADER)
        if not token:
            raise NotSignedIn("missing IAP assertion")
        try:
            return _verify_jwt(token, audience)
        except Exception as exc:  # any verification failure means not signed in
            raise NotSignedIn("IAP assertion could not be verified") from exc

    email = parse_email_header(_get(headers, EMAIL_HEADER))
    if email:
        return email
    local = os.environ.get("LOCAL_DEV_USER")
    if local and os.environ.get("K_SERVICE") is None:
        return local
    raise NotSignedIn("no IAP identity")
