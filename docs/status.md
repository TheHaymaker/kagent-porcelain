# Status against the PRD

As of 14 Nov 2026, built against kagent v1.0.1 (558e3d7). "Tested" means automated
tests in this repository pass.

## On a real cluster

`deploy/gcp/up.sh all` (run from the `deploy` workflow) brings up a GKE
Standard cluster, Substrate 1.0.0, stock kagent 1.0.1 and then Workbench in
place of kagent's controller, and runs `workbench-smoke` against it. On
14 Nov 2026 (commit 62d8435) all eight checks passed:

1. Alice and Bob sign in with company tokens and get their roles.
2. Alice's conversation with a scripted agent pauses on kagent's HITL
   extension when the agent asks to promote an app to production.
3. That pause is an approval Alice may not decide.
4. The server refuses Alice's own approval.
5. Bob finds it in his inbox and approves it.
6. The decision reaches the task, which resumes and completes.
7. The audit trail records the request, Bob's decision for Alice, and delivery.
8. kagent's signed push notification arrives and Alice is told the task finished.

That run uses a scripted model (`workbench-mockllm`) and the log deployer.

A real model works too: `up.sh model` (15 Nov 2026) gives agents Gemini
(`gemini-3-flash-preview`) on Vertex AI, and `workbench-smoke chat` got
the expected reply from it through kagent and Substrate. How it is wired,
and why:

- Substrate actors cannot reach the GKE metadata server, and kagent rejects
  a ModelConfig that needs a mounted key file under Substrate, so kagent's
  `GeminiVertexAI` provider has no usable credential there.
- Instead the `Gemini` provider points at Vertex AI's endpoint with an API
  key bound to the `workbench-vertex` service account (Vertex AI User
  only). Substrate's egress proxy adds the key as `x-goog-api-key`, so it
  never enters the actor, and usage bills through Cloud Billing.
- The plain Gemini API (AI Studio) is not used: it bills from prepaid
  credits, which this project has used up.

### A real app, end to end (16 Nov 2026)

A requester signed in at the public address and asked the Gemini builder
for "a cat themed todo app". The builder created
`thehaymaker-workbench/apps/cat-todo-app` from the FastAPI template, edited
it through the GitLab tools, built its image with Cloud Build (build-mcp),
and, after the requester approved the deploy card in Workbench, deployed
`wb-preview-cat-todo-app` to Cloud Run. With IAP on, the requester opened
the test version's link on a phone, signed in with Google and used the app.

What it took, in order:

- **Public Workbench** (ADR 0005): Ingress, managed certificate, Cloud Armor
  allowlist; sign-in with Google Identity Services ("Sign in with Google").
  The Identity Platform popup and redirect flows failed on mobile browsers
  (cross-site handler storage) and were replaced.
- **GitLab**: a private group with `apps` and `templates`, the three
  scaffolds published as templates (`up.sh gitlab-setup`), and gitlab-mcp
  with read tools so a builder without a shell can read before it writes.
- **Builds**: build-mcp turns a project's commit into a digest-pinned image
  in the builds repository with Cloud Build (in place of GitLab CI).
- **Previews**: cloudrun-mcp deploys with IAP on and grants the requester
  (from `workbench_get_requester`) access; `cloudrun_share_preview` adds
  others behind an approval. IAP uses the web app's OAuth client, since the
  project has no Workspace organization.
- **Conversation**: approvals now appear in the chat even when a resumed
  task's stream does not report the pause, sit where they happened, and are
  titled in plain words; read-only Cloud Run calls need no approval.

Known rough edges: the builder's progress card (`workbench_show`) is often
rejected (now left out of the prompts), and Gemini's prose sometimes needs
the tightened failure rules in `deploy/agents/builder-gemini.md`.

### Planning, testing and review (ADR 0006)

The builder now has three helpers, Shared subagents in its runtime: a
planner that agrees a phased plan with the requester, a tester that writes
tests for each phase's checks and runs them (`build_run_checks` in
build-mcp), and a read-only reviewer. The builder commits the plan as
`PLAN.md` and builds one phase at a time, each ending in a test version.

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

1. **The rest of the agent roster** (ADR 0006): a Verifier that opens the
   running test version, a Concierge (needs dedicated subagents or a web
   agent picker), and the others listed there.
2. **Preview panel inside Workbench** through a preview proxy on its own
   origin; designed, not started.
3. **Fix the builder's progress cards** (`workbench_show` rejections).
4. **The A2UI DataPart spike.** Does kagent pass an agent's DataParts through
   intact? The tool-result carrier works either way.
5. **Approval proof for preview-level always-ask tools** (ADR 0002 known gap).
6. **Gateway usage reporter.** LiteLLM callback → `POST /hooks/usage`, signed.
7. **Teams bot receiver** for `notify.Webhook`, and then the bot itself.
8. **Open PRD questions** still decide configuration: the directory (Entra or
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
