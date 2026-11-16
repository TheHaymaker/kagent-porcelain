# Workbench

A stakeholder workspace built on [kagent](https://github.com/kagent-dev/kagent).
A person signs in with their company account, asks for an app in plain
language, watches an agent build it in a sandbox, approves the steps that
matter, and gets a running app with a record of everything that happened.

Start with the [PRD](docs/prd.md). [docs/status.md](docs/status.md) says what
is built, what is tested, and what is still missing.
[docs/architecture.md](docs/architecture.md) describes the whole stack: every
component, every Google Cloud product, the identities and their permissions,
and how a request flows from idea to running test version.

```
                ┌─────────── web (React) ── later: Teams bot, iOS/Android
                │  gRPC-Web + A2A, bearer token, same origin (/api)
                ▼
┌──────────────────────── Workbench server (one Go process) ────────────────────────┐
│ kagent core (app.Run) ─ sessions, A2A gateway, HITL, checkpoints, schedules,      │
│                         sandboxes, Substrate, kagent gRPC API, /mcp               │
│  + identity    bearer tokens, company OIDC + linked Microsoft identities          │
│  + authz       directory groups → requester / approver / platform / auditor       │
│  + workbench.v1 gRPC  Identity, Approval, App, Audit, Notification, Usage,        │
│                       Component                                                   │
│  + /mcp tools  workbench_show, _register_app, _record_deployment,                 │
│                _request_promotion, _get_app                                       │
│  + hooks :8090 kagent push notifications (JWT-verified), gateway usage            │
│  + workers     decision redelivery, BigQuery audit export                         │
└───────────────┬───────────────────────────────────────────────┬───────────────────┘
                │ PostgreSQL: kagent schema + workbench schema  │ production deploys
                ▼                                               ▼ (approved only)
   builder agents (Claude Code in Substrate sandboxes) ──► tools/: GitLab, Cloud Run
   via model gateway (LiteLLM → Vertex AI)                 preview, Artifact Registry,
                                                           BigQuery MCP servers
```

## Repository

| Path | What |
|---|---|
| [`proto/workbench/v1`](proto/workbench/v1) | The API every surface uses (buf, lint-clean) |
| [`catalogue/`](catalogue) | The A2UI v0.9.1 component catalogue shared by every surface, with examples |
| [`server/`](server) | The Workbench server: kagent's controller as a library, plus Workbench's parts |
| [`web/`](web) | The requester web app (React, Vite); runs fully in mock mode |
| [`tools/`](tools) | GCP and GitLab MCP tool servers, preview-only, with kagent manifests |
| [`agents/`](agents) | The Claude Code builder agent pack: templates, skills, sandbox image, egress, model gateway |
| [`scaffolds/`](scaffolds) | Golden paths: FastAPI on Cloud Run, Streamlit on BigQuery, React + Express |
| [`deploy/`](deploy) | Example configuration, Helm overlay on kagent's chart, Workbench chart, database grants |
| [`docs/`](docs) | PRD, architecture and tech stack, decision records, status |

## The approval model in one paragraph

An agent's risky tool call pauses as a kagent HITL request. Workbench
classifies it (production deploy, IAM change, data deletion, new spend, or
ordinary), routes it to whoever may decide, and shows it on every surface
through `ApprovalService`. When someone decides, Workbench sends the answer
into the paused task, recording who really decided. A requester can never
approve their own production deploy; that rule is enforced in code, in a
database constraint, and again at promotion. Production is only ever reached
by Workbench itself deploying an approved, previewed image digest. Details and
known gaps: [ADR 0002](docs/adr/0002-approvals.md).

## Develop

Go 1.27+ (`GOTOOLCHAIN=auto` fetches it), Node 22, PostgreSQL 16 for
database tests, `buf` for the API.

```sh
# Server
cd server
WORKBENCH_TEST_DATABASE_URL=postgres://postgres@localhost:5432/workbench_test go test ./...
go build -o bin/workbench ./cmd/workbench
bin/workbench --config ../deploy/controller.example.yaml \
              --workbench-config ../deploy/workbench.example.yaml --check-config

# Web app, no backend needed
cd web && npm ci && npm run dev        # mock mode: see web/README.md

# Regenerate API code after editing proto/
buf lint && buf generate
```

## Deploy

1. Provision PostgreSQL roles as kagent documents, then run
   [`deploy/database/grants.sql`](deploy/database/grants.sql).
2. Install kagent's own chart with
   [`deploy/helm/values-workbench.yaml`](deploy/helm/values-workbench.yaml):
   it swaps the controller image for `server/Dockerfile`'s and mounts the
   Workbench configuration.
3. Install [`deploy/helm/workbench`](deploy/helm/workbench) with your
   configuration (start from
   [`deploy/workbench.example.yaml`](deploy/workbench.example.yaml)).
4. Apply [`tools/deploy`](tools/deploy) and [`agents/`](agents) (see their
   READMEs).

Nothing here has run on a cluster yet. That is phase 1.
