# workbench-app

An internal web tool built from the **python-fastapi-cloudrun** golden path:
FastAPI, server-rendered Jinja2 pages with HTMX, running on Cloud Run behind
Identity-Aware Proxy.

## Run it

```sh
make test   # install locked dependencies with uv, run the tests
make run    # http://localhost:8080, signed in as $LOCAL_DEV_USER (default dev@example.com)
make lint
```

## Layout

| Path | What it is |
| --- | --- |
| `app/main.py` | Routes, request logging, security headers |
| `app/identity.py` | The signed-in user from IAP (verifies the IAP JWT when `IAP_AUDIENCE` is set) |
| `app/logging_config.py` | One JSON object per log line, with Cloud Logging `severity` and trace fields |
| `app/store.py` | `MemoryStore` (default) and `FirestoreStore` for data that must persist; `make_store()` picks one |
| `app/templates/` | Jinja2 pages; `_board.html` is the part HTMX swaps |
| `app/static/htmx.min.js` | Vendored htmx 2.0.4 (no CDN, so the CSP stays `'self'`) |
| `tests/` | pytest suite |
| `Dockerfile` | Two-stage, non-root image; listens on `$PORT` |
| `.gitlab-ci.yml` | Test, then build the image into Artifact Registry. No deploy job. |
| `IAM.md` | Runtime service account and who may deploy |
| `scaffold.yaml` | Golden-path manifest |

## Configuration

| Variable | Purpose |
| --- | --- |
| `PORT` | Set by Cloud Run (8080) |
| `GOOGLE_CLOUD_PROJECT` | Enables trace correlation in logs |
| `IAP_AUDIENCE` | Verify IAP JWTs (recommended in production) |
| `LOCAL_DEV_USER` | Local runs only; ignored on Cloud Run |
| `LOG_LEVEL` | Default `INFO` |
| `APP_STORE` | `memory` or `firestore`; default `DEFAULT_STORE` in `app/store.py` (`memory`) |

## Endpoints

- `GET /healthz` - liveness, no sign-in, no dependencies.
- `GET /` - the page.
- `POST /items`, `POST /items/{id}/toggle` - HTMX actions; return the board fragment.

## Keeping data

`MemoryStore` resets on every restart and is per instance. For data that must
survive, set `DEFAULT_STORE = "firestore"` in `app/store.py` (or `APP_STORE`
on the service). `FirestoreStore` uses the project's `(default)` Firestore
database with the service's own identity (needs `roles/datastore.user`, see
`IAM.md`) and keeps items in the `<app name>-items` collection. Add more
collections the same way, always prefixed with the app's name. Tests use
`MemoryStore` and a fake Firestore client; they need no credentials.
