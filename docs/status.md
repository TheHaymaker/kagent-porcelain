# Status against the PRD

As of 14 Nov 2026, built against kagent v1.0.1 (558e3d7). "Tested" means automated
tests in this repository pass; nothing has run on a cluster yet, because
phase 1 (Substrate and kagent on GKE) has not happened.

## Requirements

| # | Requirement | Where | State |
|---|---|---|---|
| 1 | Ask in plain language, watch it stream | web: conversation screen, A2A over gRPC-Web | Built; tested in a browser in mock mode |
| 2 | Company sign-in on every surface | `server/internal/identity`, `IdentityService` | Built, tested (multi-issuer JWKS, linking, tamper/expiry) |
| 3 | Roles decide agent and conversation access | `server/internal/authz` (kagent `CollectionAuthorizer`) | Built, tested |
| 4 | Approve or refuse on a card, with a reason | `ApprovalService.Decide`; web ApprovalCard | Built, tested |
| 5 | Always-ask kinds; no self-approved production deploy | `server/internal/policy`, DB constraint, `registry.Promote` | Built, tested; gap in ADR 0002 |
| 6 | Route approvals to someone else, on any surface | approval service: inbox, routing, reassign, delegated delivery | Built, tested |
| 7 | Readable audit trail | append-only `audit_event`, `AuditService`, BigQuery exporter | Built, tested (exporter against a fake BigQuery) |
| 8 | Activity list | web activity panel from the A2A stream | Built; tested in mock mode |
| 9 | Isolated computer per conversation | kagent + Substrate | kagent's; not exercised here |
| 10 | Credentials out of transcript and sandbox | tool servers read secrets from files; production creds only in the server; egress injection in the agent pack | Built; untested on a cluster |
| 11 | Native components on every surface | `catalogue/`, `server/internal/components`, `workbench_show`, web renderer | Built, tested; DataPart pass-through spike pending |
| 12 | Reusable skills | `agents/skills` (six skills, OCI plugin) | Authored; not run |
| 13 | Run on a schedule | kagent ScheduledRunService; web schedules screen | Built; no run history yet |
| 14 | Share a conversation | kagent session shares (`X-Share-Token`, which kagent reads natively); web share dialog | Built; tested in mock mode |
| 15 | Spend limits | `server/internal/usage`, `UsageService`, gateway usage hook; LiteLLM budgets | Built, tested; gateway → hook reporter not written |
| 16 | Notifications | `server/internal/notify`: in-app, email, Teams webhook, FCM | Built, tested; Teams bot receiver not built |
| 17 | Governed MCP | `tools/`: GitLab, Cloud Run (preview), Artifact Registry, BigQuery | Built, tested against fakes |
| 18 | Bring your own agent | kagent | Not started (by design) |
| 19 | App catalogue | `AppService`, registry, `workbench_register_app` | Built, tested |
| 20 | Preview, then approved promotion | `workbench_record_deployment`, `RequestPromotion`, Cloud Run deployer | Built, tested (deployer against a fake API) |
| 21 | Try again from an earlier point | kagent checkpoints/forks; web control | Built; tested in mock mode |
| 22 | Teams | server is ready (linked identities, Teams notifications, same APIs) | Bot not started (phase 5) |
| 23 | Mobile | server is ready (`RegisterDevice`, FCM) | Apps not started (phase 6) |

## Not yet done, in order of importance

1. **Phase 1 on a real cluster.** Install Substrate and kagent on GKE, then
   the Workbench image with `deploy/helm/values-workbench.yaml`, and take one
   scaffold from request to a Cloud Run preview. Nothing here has met a real
   kagent API server, Substrate, GitLab or Google API.
2. **The A2UI DataPart spike.** Does kagent pass an agent's DataParts through
   intact? The tool-result carrier works either way.
3. **Approval proof for preview-level always-ask tools** (ADR 0002 known gap).
4. **Gateway usage reporter.** LiteLLM callback → `POST /hooks/usage`, signed.
5. **Teams bot receiver** for `notify.Webhook`, and then the bot itself.
6. **Open PRD questions** still decide configuration: the directory (Entra or
   Workspace), who approves production (the approver group), and the first
   three apps (which shape the scaffolds).

## Web app

All four checks pass (typecheck, lint, 50 tests, build). Every screen was
run in a browser in mock mode at desktop and phone widths; screenshots are in
`web/docs/screenshots/`. Mock mode replaces only the transport, so it runs
the same code as live above the wire, including the real A2A client.

Not built in the web app yet:

- linking a Teams identity from Settings (listing and unlinking work);
- reassigning an approval, and a page that shows the approval policy;
- setting spend limits (platform engineers);
- attachments, renaming or deleting a conversation, and schedule run history;
- kagent's resend-and-reattach retry for interrupted chat streams.

The container image (`web/Dockerfile`, nginx) has not been built, and its
nginx configuration has not been syntax-checked.

## How to check

```
# server (needs PostgreSQL for the database tests)
cd server && WORKBENCH_TEST_DATABASE_URL=postgres://... go test -race ./...
cd tools && go test -race ./...
cd web && npm ci && npm run lint && npm run typecheck && npm test && npm run build
cd scaffolds/<name> && make lint test
```

CI (`.github/workflows/ci.yaml`) runs all of these.
