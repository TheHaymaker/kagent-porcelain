# workbench-app

An internal app built from the **node-react-cloudrun** golden path: a React
single-page app (Vite) and a small Express 5 API, served by one Node 22
process on Cloud Run behind IAP.

## Run it

```sh
make test   # vitest: API (supertest) + React components (jsdom)
make run    # API on :8080 signed in as $LOCAL_DEV_USER, UI on http://localhost:5173
make lint   # node --check + production build
```

## Layout

| Path | What it is |
| --- | --- |
| `server/app.js` | Express app: health, request logging, security headers, IAP identity, `/api/*`, SPA fallback |
| `server/identity.js` | IAP header parsing and JWT verification (`IAP_AUDIENCE`) |
| `server/logger.js` | JSON lines for Cloud Logging, with trace correlation |
| `server/store.js` | In-memory store; replace for persistent data |
| `web/` | React app (Vite root); `web/src/api.js` is the only place that calls the API |
| `test/` | vitest suites |
| `Dockerfile` | Multi-stage: build client, prod-only deps, non-root runtime |
| `.gitlab-ci.yml`, `IAM.md`, `scaffold.yaml` | CI (no deploy), permissions, golden-path manifest |

## API

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/healthz` | No sign-in |
| GET | `/api/me` | Signed-in user's email |
| GET | `/api/items` | List |
| POST | `/api/items` | `{ "title": "..." }` -> 201, or 422 |
| POST | `/api/items/:id/toggle` | 404 if unknown |

## Configuration

`PORT`, `GOOGLE_CLOUD_PROJECT` (trace correlation), `IAP_AUDIENCE`,
`LOCAL_DEV_USER` (local only), `LOG_LEVEL`.
