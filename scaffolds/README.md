# Golden-path scaffolds

Three complete, minimal app templates the Workbench builder starts every app
from. Each is runnable and tested as-is, and each carries the same platform
contract so every app built from it is deployable, observable and reviewable
the same way. The builder image bakes this directory in at
`/opt/workbench/scaffolds` (see `../agents/image/Dockerfile`).

## Which one

| | `python-fastapi-cloudrun` | `streamlit-bigquery` | `node-react-cloudrun` |
| --- | --- | --- | --- |
| **Build this when the app...** | collects input, tracks a process, replaces a spreadsheet or an email thread | shows charts, tables and filters over data already in BigQuery | needs a rich interactive UI (inline editing, drag-and-drop, many live controls) |
| **Not for** | charts over BigQuery; very interactive UIs | writing data, multi-step workflows, public pages | simple forms (more code to maintain than the Python path) |
| **Stack** | FastAPI, Jinja2 templates, HTMX 2 (vendored) | Streamlit, google-cloud-bigquery, pandas | Express 5 API + React 19 SPA (Vite) |
| **Language / tooling** | Python ≥3.12, uv | Python ≥3.12, uv | Node ≥22, npm |
| **Port / health** | `$PORT` (8080) / `GET /healthz` | `$PORT` (8080) / `GET /_stcore/health` | `$PORT` (8080) / `GET /healthz` |
| **Identity** | IAP header; IAP JWT verified when `IAP_AUDIENCE` set | same, via `st.context.headers` | same, server-side (`google-auth-library`) |
| **Data** | in-memory store behind a small interface (swap for Firestore) | read-only BigQuery via `QueryRunner` | in-memory store behind a small interface |
| **Cost guard** | - | `maximum_bytes_billed` on every query (default 1 GiB), free dry-run first, job labels | - |
| **Runtime SA roles** | none by default | `bigquery.jobUser` (billing project) + `bigquery.dataViewer` (datasets read) | none by default |
| **Cloud Run notes** | defaults | session affinity on, 3600 s timeout (websocket) | defaults |
| **Tests** | 22 pytest (routes, IAP identity, logging) | 17 pytest (query guard rails with a fake client, Streamlit `AppTest`, identity) | 22 vitest (supertest API, React with jsdom, logger) |
| **Relative maintenance** | lowest | low | highest |

When two fit, prefer the left-most.

## The shared contract

Every scaffold has:

- **`scaffold.yaml`** - machine-readable manifest: `name`, `description`,
  `whenToUse` / `whenNotToUse`, `language`, `runtime`, `ports`, `healthCheck`,
  `identity`, `commands` (install/test/run/lint), `rename` (placeholder
  `workbench-app` and the files that contain it), `requiredApis`,
  `runtimeServiceAccount` roles.
- **`Makefile`** - `make install | test | lint | run`, identical verbs across
  stacks, so the builder never needs stack-specific knowledge to verify.
- **Health endpoint** - liveness with no dependencies and no sign-in.
- **IAP identity** - the signed-in user's email from `X-Goog-Authenticated-User-Email`,
  or a verified `X-Goog-IAP-JWT-Assertion` when `IAP_AUDIENCE` is set (recommended
  in production). `LOCAL_DEV_USER` works only off Cloud Run (`K_SERVICE` unset).
- **Structured logging** - one JSON object per stdout line with `severity`,
  `message`, `time`, request `httpRequest` fields and
  `logging.googleapis.com/trace` (from `X-Cloud-Trace-Context` or `traceparent`
  when `GOOGLE_CLOUD_PROJECT` is set), so Cloud Logging parses levels and
  groups a request's lines. No request bodies or personal data in logs.
- **Security headers** - CSP `default-src 'self'` (no inline scripts, no CDNs),
  `nosniff`, `X-Frame-Options: DENY` (web scaffolds); XSRF on and error details
  hidden (Streamlit).
- **`Dockerfile`** - multi-stage, locked dependencies only (`uv sync --frozen`
  / `npm ci`), non-root user, listens on `$PORT`.
- **`.gitlab-ci.yml`** - `test` (lint + tests with JUnit report) then
  `build-image` (Cloud Build → Artifact Registry
  `$GCP_REGION-docker.pkg.dev/$GCP_PROJECT_ID/$AR_REPOSITORY/$CI_PROJECT_NAME:$CI_COMMIT_SHA`,
  digest exported as `IMAGE_REF` dotenv). Credentials come from GitLab's Google
  Cloud IAM integration (`identity: google_cloud`, Workload Identity
  Federation) - no JSON keys. **No deploy job**: previews are deployed by the
  builder through `cloudrun-mcp`; production only by Workbench after approval.
- **`IAM.md`** - dedicated runtime service account `app-<name>@`, least
  privilege per feature, who may open the app (IAP), who may deploy where.
- **Tests that pass** on a fresh checkout with no cloud credentials.

## Verified locally

| Scaffold | Command | Result |
| --- | --- | --- |
| python-fastapi-cloudrun | `make test`, `make lint`, uvicorn smoke run (`/healthz`, 401 without IAP, POST with IAP header, JSON log lines with trace) | 22 passed; ruff clean |
| streamlit-bigquery | `make test`, `make lint`, `streamlit run` smoke (`/_stcore/health` = ok) | 17 passed; ruff clean |
| node-react-cloudrun | `make test`, `npm run check` (incl. production build), `node server/index.js` smoke (`/healthz`, `/api/me`, SPA) | 22 passed; build ok |

Also verified: renaming the placeholder (`using-scaffolds` skill steps) and
refreshing the lockfile keeps the suites green. Not verified here: Docker
image builds (no daemon available), CI on a GitLab runner, and the BigQuery /
IAP paths against real Google services (covered by fakes in tests).
