# ADR 0001: The Workbench server is kagent's controller, built as a library

Status: accepted (Nov 2026). Answers the PRD's open question "Does the
Workbench server replace kagent's controller, or run beside a stock one?"

## Decision

`server/cmd/workbench` calls kagent's `app.Run` (go/core/pkg/app, kagent
v1.0.1) with every documented hook filled in:

| `app.Options` field | Workbench supplies |
|---|---|
| `Authenticator` | `internal/identity`: bearer tokens from company OIDC issuers, linked Microsoft identities for Teams, short-lived HMAC delegation tokens for loopback calls |
| `Authorizer` | `internal/authz`: directory groups → requester / approver / platform / auditor |
| `ExtraMigrations` | `server/migrations`: the `workbench` schema, applied by kagent's runner after kagent's own tracks |
| `GRPCServices` + `MethodPolicies` | `internal/api`: the seven `workbench.v1` services, on kagent's gRPC/gRPC-Web server |
| `MCPExtensions` | `internal/mcptools`: `workbench_*` tools on kagent's `/mcp` endpoint |
| `SetupWithManager` | leader-elected workers: decision redelivery, BigQuery audit export |

It replaces kagent's controller in kagent's own Helm chart by swapping the
controller image (`deploy/helm/values-workbench.yaml`); kagent's chart is not
forked.

## Why

One process, one API, one database, one audit stream. Clients speak kagent's
protocols (A2A, kagent gRPC) plus Workbench's services on the same origin, and
nothing bridges between them.

## Consequences

- Workbench's own services reach kagent only through kagent's public API over
  loopback (`internal/kagent`), never through kagent's internals. Every such
  call passes kagent's authentication, authorization and ownership checks
  exactly as a client's would.
- Workbench needs one listener kagent's options do not offer: an HTTP
  endpoint for push notifications and gateway usage. It runs in the same
  process on an internal port (`internal/hooks`, `:8090`), never exposed.
- kagent's library surface is alpha. The version is pinned in `server/go.mod`
  to a kagent release (v1.0.1), so the Go library, the published Helm charts
  and the Substrate version (1.0.0) all match; upgrades go through a test
  cluster first. The code touches only the
  documented options and `go/core/pkg/*`.
- Verified locally: the binary builds against kagent v1.0.1 (558e3d7), and against a
  real PostgreSQL it applies kagent's 19 tables and Workbench's 17 in one
  migration run before stopping for lack of a Kubernetes cluster.
