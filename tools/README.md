# Workbench GCP tool servers

Four MCP servers that let Workbench builder agents (Claude Code in a kagent
sandbox) scaffold apps in GitLab and deploy them to a Google Cloud **preview**
environment. Each runs in-cluster, speaks MCP over Streamable HTTP, and holds
the credentials itself. Credentials never reach the conversation or the
sandbox.

| Binary | Upstream API | Tools |
| --- | --- | --- |
| `cmd/gitlab-mcp` | GitLab REST v4 | 9 |
| `cmd/cloudrun-mcp` | Cloud Run Admin v2, Cloud Logging v2 | 6 |
| `cmd/artifactregistry-mcp` | Artifact Registry v1 | 3 (read-only) |
| `cmd/bigquery-mcp` | BigQuery v2 | 7 |

Module: `github.com/thehaymaker/kagent-porcelain/tools` (Go 1.26). Dependencies
are kept small: `github.com/modelcontextprotocol/go-sdk` v1.8.0 (the version
kagent uses) and `golang.org/x/oauth2`. No Google Cloud client libraries are
used; the servers call the REST APIs through `net/http` with an ADC token
source.

```
cmd/<server>/main.go          flags/env -> config -> register tools -> serve
internal/mcpx                 shared MCP wrapper: identity, audit log, error redaction, annotations, /mcp + /healthz
internal/mcpx/mcpxtest        test harness: real MCP client over httptest Streamable HTTP
internal/httpx                Doer interface, bounded JSON calls, readable upstream errors
internal/validate             argument validation helpers
internal/{gitlab,cloudrun,artifactregistry,bigquery}   clients + tools + tests
policy/                       machine-readable always-ask classification (importable)
deploy/                       Kubernetes + kagent manifests
Dockerfile                    distroless, one image per server via --build-arg SERVER
```

## Always-ask classification

Tool names follow `<server>_<verb>_<noun>`; registration panics otherwise. The
approval classes are defined by name patterns in [`policy/policy.go`](policy/policy.go).
A test checks that every registered tool is in the catalog and that the
annotations match the classes.

| Class | Pattern | Tools |
| --- | --- | --- |
| Data deletion | `*_delete_*` | `cloudrun_delete_preview_service`, `bigquery_delete_table` |
| IAM change | `*_iam_*` | `cloudrun_share_preview` | give or remove one person, group or domain browser access to a preview through IAP (`roles/iap.httpsResourceAccessor`); never public | no | yes | yes | no | **IAM change** |
| `cloudrun_set_iam_invoker` |
| New spend | `*_create_dataset` | `bigquery_create_dataset` |
| New spend (conditional) | `cloudrun_deploy_preview` with `allow_create: true` | `cloudrun_deploy_preview` |

`cloudrun_deploy_preview` updates an existing preview service without any
flag. It refuses to create a service unless `allow_create` is `true`. A policy
that sees tool arguments should ask when `allow_create == true`. A policy that
sees only tool names must treat every `cloudrun_deploy_preview` call as
always-ask. `policy.Classify(tool, nil)` gives that conservative answer.

Every other tool is not always-ask. That includes `gitlab_commit_files`: its
`delete` actions remove files from a branch, but git history keeps them, and
the change reaches the default branch only through a merge request.

## Shared behaviour

- **Transport.** Streamable HTTP at `POST /mcp`. The server is stateless and
  returns plain JSON responses, so replicas need no session affinity.
  `GET /healthz` is the liveness endpoint. Listen address: `-listen` /
  `LISTEN_ADDR` (default `:8080`).
- **Caller identity.** The `X-User-Id` request header is sanitized (control
  characters stripped, max 128 chars) and becomes `Call.Requester`. If the
  header is absent the requester is `"unknown"`. It is used for:
  - the audit log;
  - the GitLab commit trailer `Workbench-Requester: <id>`, plus a note in
    merge request and project descriptions;
  - the `wb-requester` label on Cloud Run services and revisions, BigQuery
    datasets and query jobs. Label values are lower-cased with invalid
    characters replaced by `_`, so `alice@example.com` becomes
    `alice_example_com`.

  `-require-requester` / `REQUIRE_REQUESTER=true` makes non-read-only tools
  fail when the header is missing. The header is **asserted by the caller**:
  use it for attribution only, never for authorization.
- **Audit log.** One JSON line per call on stdout with
  `audit=workbench.tool`, `tool`, `requester`, `read_only`, `outcome`,
  `duration` and the redacted error. Arguments are not logged.
- **Errors.** Tool failures come back as MCP tool errors (`isError: true`) with
  readable text:
  - `invalid argument: <field>: <reason>` for validation failures, which never
    reach the upstream API;
  - `<tool> failed: upstream returned HTTP 404 NOT_FOUND: ...` for upstream
    errors. Upstream messages are flattened to one line and truncated to 600
    bytes.

  Panics become `internal error`.
- **Secret redaction.** Before any error text leaves the server or is logged,
  it is scrubbed of:
  - the current GitLab token (re-read from its file);
  - GitLab token shapes (`glpat-…` and similar);
  - Google access tokens (`ya29.…`);
  - `Bearer …` and `PRIVATE-TOKEN: …` values.

  No tool takes a credential argument; a test asserts that no input schema
  mentions a token.
- **Structured output.** Every tool has a typed output schema and returns
  `structuredContent`; the SDK mirrors it as JSON text. Unknown arguments are
  rejected by the input schema (`additionalProperties: false`). That rejection
  is how passing `project: <prod>` to Cloud Run fails.
- **Bounds.** Every server limits:
  - page sizes;
  - log entries: 200 entries, 2 000 bytes per entry, 64 KiB total;
  - query results: rows, 2 000-byte cells, total bytes;
  - schema columns: 500;
  - pipeline jobs: 50;
  - commit payloads: 100 actions, 5 MiB;
  - upstream response bodies: 8 MiB.
- **Annotations.** Every tool sets `readOnlyHint`, `destructiveHint`,
  `idempotentHint` and `openWorldHint` explicitly. `openWorldHint` is `false`
  except on the two BigQuery query tools. Those can read any table the service
  account can see, including public datasets, and return arbitrary data. Every
  other tool works inside one configured group, project or repository.

## gitlab-mcp

Projects must sit under one configured group. A `project` argument is either a
bare name (`my-app`, which means `<group>/my-app`) or a full path inside the
group. The server resolves every project through the API and re-checks its
canonical `path_with_namespace`, which catches renames and redirects out of
the group. Writes then use the numeric id.

| Tool | Does | readOnly | destructive | idempotent | openWorld | Always-ask |
| --- | --- | --- | --- | --- | --- | --- |
| `gitlab_create_project` | Fork `<template-group>/<template>` into the group as a private project (`mr_default_target_self`); returns `import_status` | no | no | no | no | no |
| `gitlab_get_project` | id, path, URL, default branch, import status, fork origin | yes | no | yes | no | no |
| `gitlab_list_branches` | branches with `next_page` pagination (max 100/page) | yes | no | yes | no | no |
| `gitlab_list_files` | file paths at a ref, recursive, optionally under a directory, paginated | yes | no | yes | no | no |
| `gitlab_get_file` | one text file at a ref (≤256 KiB; binary refused) | yes | no | yes | no | no |
| `gitlab_create_branch` | branch from ref (default: default branch) | no | no | no | no | no |
| `gitlab_commit_files` | one commit with ≤100 create/update/delete/move actions, ≤5 MiB; text or base64; appends `Workbench-Requester` trailer | no | yes | no | no | no |
| `gitlab_restore_from_template` | puts ≤20 files (≤4 MiB) back as the project's template has them, in one commit; the template is the project the app was forked from and must be in the template group; for lockfiles and build files an agent cannot regenerate | no | yes | no | no | no |
| `gitlab_create_merge_request` | MR within the same project (`target_project_id` pinned, never the template upstream); optional draft | no | no | no | no | no |
| `gitlab_get_pipeline_status` | latest pipeline for a ref (or by id) + ≤50 jobs with failure reasons | yes | no | yes | no | no |

Validation:
- paths are relative, with no `..`, empty segments, `.git` or control
  characters;
- branch names follow git's rules;
- actions are unique per path, and `delete` actions carry no content.

The token goes only into the `PRIVATE-TOKEN` header. It is re-read from its
file on every call, so Secret rotation needs no restart.

| Flag | Env | Default | |
| --- | --- | --- | --- |
| `-gitlab-url` | `GITLAB_BASE_URL` | – | required |
| `-allowed-group` | `GITLAB_ALLOWED_GROUP` | – | required, e.g. `workbench/apps` |
| `-template-group` | `GITLAB_TEMPLATE_GROUP` | – | required, e.g. `workbench/templates` |
| `-token-file` | `GITLAB_TOKEN_FILE` | `/var/run/secrets/workbench/gitlab/token` | mounted Secret; the process exits at start if it is missing |

The token needs the `api` scope. Use a group access token on the apps group
with Developer or Maintainer role, plus Reporter on the template group (enough
to fork).

## cloudrun-mcp (preview only)

The server works only on the preview environment. It enforces this itself and
does not rely on tool arguments:

- The project and region come from configuration only. No tool has a
  `project` or `region` argument, and unknown arguments are rejected.
- Service names are forced into `wb-preview-<name>`. You may pass `my-app` or
  `wb-preview-my-app`. Anything other than a plain DNS label of up to 49
  characters in total is rejected (`/`, `..`, `%`, uppercase), so arguments
  cannot inject resource paths.
- Startup fails if the preview project is listed in `WB_PRODUCTION_PROJECTS`.
- Images must start with the configured builds-repository prefix and be
  pinned by digest.
- Deploy and delete refuse services that lack the `wb-managed=true` label.
  The deploy tool sets that label.
- There is no production deploy tool. Production is reached only through
  Workbench's approval-gated promotion, which runs in the Workbench server
  with credentials that neither this server nor the sandbox has.

| Tool | Does | readOnly | destructive | idempotent | openWorld | Always-ask |
| --- | --- | --- | --- | --- | --- | --- |
| `cloudrun_deploy_preview` | Create (needs `allow_create: true`) or update a preview service; waits for the operation (`wait: false` to skip); labels `wb-managed`, `wb-requester`; min 0 / max ≤ cap instances; memory 256Mi–2Gi, cpu 1–2; plain env only (reserved `PORT`, `K_*`, `GOOGLE_*` rejected) | no | yes (replaces config/traffic) | no | no | **new spend when `allow_create: true`** |
| `cloudrun_get_service` | URL, ready, message, image, latest ready/created revision, traffic, requester | yes | no | yes | no | no |
| `cloudrun_list_revisions` | revisions with image, ready, message; `page_token` pagination | yes | no | yes | no | no |
| `cloudrun_read_logs` | Logging entries for the service (since ≤ 7 days, min severity, revision, `contains` → `SEARCH("…")` safely quoted), newest first, bounded | yes | no | yes | no | no |
| `cloudrun_delete_preview_service` | delete a managed preview service and wait | no | yes | yes | no | **data deletion** |
| `cloudrun_set_iam_invoker` | add/remove `roles/run.invoker` for one member (`user:`/`group:`/`serviceAccount:`/`domain:`); policy v3, etag round-trip; no-op if unchanged | no | yes | yes | no | **IAM change** |

`cloudrun_set_iam_invoker` also has limits:
- `allUsers` and `allAuthenticatedUsers` are refused unless
  `CLOUDRUN_ALLOW_PUBLIC_INVOKER=true`;
- if `CLOUDRUN_INVOKER_DOMAINS` is set, members must belong to those domains.
  The preview project's own service accounts are always allowed.

| Flag | Env | Default | |
| --- | --- | --- | --- |
| `-preview-project` | `CLOUDRUN_PREVIEW_PROJECT` | – | required |
| `-region` | `CLOUDRUN_REGION` | – | required |
| `-production-projects` | `WB_PRODUCTION_PROJECTS` | – | comma list, guard only |
| `-image-prefix` | `CLOUDRUN_IMAGE_PREFIX` | – | required, e.g. `europe-west1-docker.pkg.dev/wb-builds/builds/` |
| `-runtime-service-account` | `CLOUDRUN_RUNTIME_SERVICE_ACCOUNT` | – | runtime SA of preview revisions |
| `-max-instances` | `CLOUDRUN_MAX_INSTANCES` | `2` | cap |
| `-allow-public-invoker` | `CLOUDRUN_ALLOW_PUBLIC_INVOKER` | `false` | |
| `-invoker-domains` | `CLOUDRUN_INVOKER_DOMAINS` | – | comma list |
| `-iap` | `CLOUDRUN_IAP` | `false` | deploy previews with IAP on; `viewers` on deploy and `cloudrun_share_preview` grant browser access |
| `-deploy-wait` | `CLOUDRUN_DEPLOY_WAIT` | `3m` | keep below the RemoteMCPServer `timeout` |

## artifactregistry-mcp (read-only)

Covers one repository, the builds repository. There is no delete tool.

| Tool | Does | readOnly | destructive | idempotent | openWorld | Always-ask |
| --- | --- | --- | --- | --- | --- | --- |
| `artifactregistry_list_images` | images (packages), newest first, paginated | yes | no | yes | no | no |
| `artifactregistry_list_tags` | tags of an image with the digest each points to | yes | no | yes | no | no |
| `artifactregistry_get_digest` | tag → `sha256:` digest and the digest-pinned `ref` to pass to `cloudrun_deploy_preview` | yes | no | yes | no | no |

An image is named by its bare name (`my-app`, `team/svc`) or by its full
reference inside the repository. Tags and digests in the image name, and
references to other repositories, are rejected.

| Flag | Env | Default |
| --- | --- | --- |
| `-project` | `AR_PROJECT` | – |
| `-location` | `AR_LOCATION` | – |
| `-repository` | `AR_REPOSITORY` | `builds` |

## build-mcp

Builds a Workbench app's container image from its GitLab project with Cloud
Build, in place of a GitLab CI pipeline that would need Google credentials.
The source is fetched through the same GitLab client as gitlab-mcp, so only
projects in the allowed group can be built; the tree at one commit is uploaded
to a staging bucket and built as a plain `docker build` of the repository
root, as a dedicated service account, into the builds repository only.
Reading build logs needs `roles/logging.viewer` for the server's own identity.

| Tool | Does | readOnly | destructive | idempotent | openWorld | Always-ask |
| --- | --- | --- | --- | --- | --- | --- |
| `build_start_image` | resolve ref → commit, upload the tree, start the build; returns `build_id` and the `<repo>/<project>:<commit12>` tag | no | no | no | no | no |
| `build_run_checks` | the project's CI checks at a commit, one Cloud Build step each: `uv sync`, `ruff check`, `ruff format --check`, `pytest` for a uv project; `npm ci`, `npm run check`, `npm test` for an npm project; no image | no | no | no | no | no |
| `build_get_status` | status of a Workbench build (tagged `workbench`; others are refused); on an image build's SUCCESS the digest-pinned `image_ref` for `cloudrun_deploy_preview`; for a finished check run or any failure, `output` (the end of the log, from Cloud Logging) and `failed_step` | yes | no | yes | no | no |

| Flag | Env | Default |
| --- | --- | --- |
| `-gitlab-url`, `-allowed-group`, `-template-group`, `-token-file` | as gitlab-mcp | |
| `-project` | `BUILD_PROJECT` | – |
| `-location` | `BUILD_LOCATION` | – |
| `-repository` | `BUILD_REPOSITORY` | `builds` |
| `-bucket` | `BUILD_BUCKET` | – |
| `-service-account` | `BUILD_SERVICE_ACCOUNT` | – |

## bigquery-mcp

All jobs run, and are billed, in one configured project and location.

| Tool | Does | readOnly | destructive | idempotent | openWorld | Always-ask |
| --- | --- | --- | --- | --- | --- | --- |
| `bigquery_list_datasets` | datasets in the project, paginated | yes | no | yes | no | no |
| `bigquery_list_tables` | tables/views of a dataset, paginated | yes | no | yes | no | no |
| `bigquery_get_schema` | flattened columns (≤500), rows, bytes, partitioning | yes | no | yes | no | no |
| `bigquery_dry_run_query` | statement type, bytes estimate, whether it fits the cap and would be accepted by `run_query`, referenced tables | yes | no | yes | yes | no |
| `bigquery_run_query` | single read-only SELECT; `maximumBytesBilled` cap; ≤ `max-rows` rows, 2 000-byte cells, 256 KiB total; labels `wb-requester` | yes | no | yes | yes | no |
| `bigquery_create_dataset` | dataset `<prefix>…` in the configured location, labels `wb-managed`/`wb-requester`, optional default table expiration | no | no | no | no | **new spend** |
| `bigquery_delete_table` | delete a table, only from `<prefix>` datasets; no `$` decorators | no | yes | yes | no | **data deletion** |

`bigquery_run_query` rejects DML, DDL and scripts in three layers:
1. **Lexical check** (`CheckReadOnly`). String literals, comments and
   backtick-quoted identifiers are skipped. The statement must start with
   `SELECT`, `WITH` or `(`, contain at most one trailing `;`, and contain none
   of these keywords:
   `INSERT UPDATE DELETE MERGE TRUNCATE CREATE DROP ALTER UNDROP RENAME GRANT REVOKE EXPORT LOAD COPY CALL EXECUTE IMMEDIATE DECLARE SET BEGIN COMMIT ROLLBACK TRANSACTION ASSERT RAISE LOOP WHILE`.
   A column with one of these names must be backtick-quoted.
2. **Dry run.** BigQuery must report `statementType == "SELECT"`, and the
   estimated bytes must fit the cap. This check is authoritative.
3. **Job cap.** The job runs with `maximumBytesBilled` set, so BigQuery
   enforces the cap itself.

| Flag | Env | Default |
| --- | --- | --- |
| `-project` | `BQ_PROJECT` | – |
| `-location` | `BQ_LOCATION` | – |
| `-max-bytes-billed` | `BQ_MAX_BYTES_BILLED` | `1073741824` (1 GiB) |
| `-max-rows` | `BQ_MAX_ROWS` | `200` |
| `-dataset-prefix` | `BQ_DATASET_PREFIX` | `wb_` |
| `-query-timeout` | `BQ_QUERY_TIMEOUT` | `60s` |

## Common flags

| Flag | Env | Default |
| --- | --- | --- |
| `-listen` | `LISTEN_ADDR` | `:8080` |
| `-require-requester` | `REQUIRE_REQUESTER` | `false` |
| `-log-level` | `LOG_LEVEL` | `info` |

## Credentials and Google IAM

The Google servers use Application Default Credentials. In GKE that is
Workload Identity, and each server's Kubernetes ServiceAccount is annotated
with its own Google service account. Suggested least-privilege grants:

| GSA | Grants |
| --- | --- |
| `wb-cloudrun-mcp` | On the **preview project only**: `roles/run.admin`, needed for `setIamPolicy` on services (or a custom role with `run.services.{get,create,update,delete,getIamPolicy,setIamPolicy}`, `run.revisions.list`, `run.operations.get`); `roles/logging.viewer`; `roles/iam.serviceAccountUser` on the preview runtime SA. Nothing on production. |
| `wb-artifactregistry-mcp` | `roles/artifactregistry.reader` on the builds repository |
| `wb-bigquery-mcp` | `roles/bigquery.jobUser` and `roles/bigquery.user` on the data project. With `bigquery.user` it owns the datasets it creates, so it can delete tables only there. Add `roles/bigquery.dataViewer` on the source datasets agents may read. |

The preview project's Cloud Run service agent also needs
`roles/artifactregistry.reader` on the builds repository so it can pull
images.

The GitLab token lives in the Secret `gitlab-mcp-token` (key `token`), mounted
read-only at `/var/run/secrets/workbench/gitlab/token`. Create it out of band:
```sh
kubectl -n workbench-tools create secret generic gitlab-mcp-token --from-file=token=./token
```

## Build and deploy

```sh
# one image per server
for s in gitlab-mcp cloudrun-mcp artifactregistry-mcp bigquery-mcp; do
  docker build --build-arg SERVER=$s --build-arg VERSION=$(git rev-parse --short HEAD) \
    -t REGISTRY/workbench/$s:TAG tools/
done
kubectl apply -k tools/deploy   # after replacing REGISTRY and WB_* placeholders (use an overlay)
gcloud iam service-accounts add-iam-policy-binding wb-cloudrun-mcp@WB_PLATFORM_PROJECT.iam.gserviceaccount.com \
  --role roles/iam.workloadIdentityUser \
  --member "serviceAccount:WB_PLATFORM_PROJECT.svc.id.goog[workbench-tools/cloudrun-mcp]"   # likewise for the others
```

For each server, `deploy/<server>.yaml` contains:
- a ServiceAccount (with the Workload Identity annotation for the Google
  servers);
- a Deployment: 2 replicas, distroless nonroot, read-only root filesystem, all
  capabilities dropped, `/healthz` probes;
- a ClusterIP Service on port 8080;
- a kagent `RemoteMCPServer` (`api.kagent.dev/v1alpha3`):

```yaml
apiVersion: api.kagent.dev/v1alpha3
kind: RemoteMCPServer
metadata: {name: cloudrun-mcp, namespace: workbench-tools}
spec:
  description: "Workbench Cloud Run PREVIEW deployments ..."
  protocol: STREAMABLE_HTTP
  url: http://cloudrun-mcp.workbench-tools.svc.cluster.local:8080/mcp
  timeout: 300s            # deploy waits up to CLOUDRUN_DEPLOY_WAIT (3m)
  allowedNamespaces:
    from: Selector
    selector: {matchLabels: {workbench.dev/builder-agents: "true"}}
```

The RemoteMCPServer `timeout` values are: gitlab 60s, cloudrun 300s,
artifactregistry 30s, bigquery 120s. Agent namespaces opt in with the label
`workbench.dev/builder-agents=true`. An AgentTemplate binds the tools across
namespaces. kagent's `MCPToolBinding` has `tools` and `requireApproval`, so the
always-ask tools can be split into their own approval-gated binding:

```yaml
spec:
  tools:
    - mcp:
        server: {kind: RemoteMCPServer, name: cloudrun-mcp, namespace: workbench-tools}
        tools: [cloudrun_get_service, cloudrun_list_revisions, cloudrun_read_logs, cloudrun_deploy_preview]
    - mcp:
        server: {kind: RemoteMCPServer, name: cloudrun-mcp, namespace: workbench-tools}
        tools: [cloudrun_delete_preview_service, cloudrun_set_iam_invoker]
        requireApproval: true
```

kagent cannot approve per argument. So with a binding like the one above,
`cloudrun_deploy_preview` with `allow_create: true` is gated only by
Workbench's own policy. Otherwise put `cloudrun_deploy_preview` in the
approval binding.

## Caller identity

kagent sends `X-User-Id` to MCP servers only from the Go kagent runtime with
`runtime.token_propagation.enabled=true` (`go/adk/pkg/mcp/registry.go`).
Claude and Codex harnesses may not forward it. In that case the requester is
`unknown`. The manifests ship with `REQUIRE_REQUESTER=false` for that reason.
Set it to `"true"` once identity is forwarded.

An alternative is a per-user or per-app RemoteMCPServer whose `headersFrom`
holds a literal `X-User-Id` value set by Workbench. kagent allows that for
literal headers; credential-injection rules apply only to Secret-backed ones.

## Tests

```sh
cd tools && go build ./... && go vet ./... && go test ./...
```

The tests run a real MCP client against each server over Streamable HTTP
(`mcpxtest.Connect`). Each upstream API is an `httptest` fake behind the
`httpx.Doer` interface. They cover:
- tool lists, annotations and output schemas;
- input validation, and that invalid input never reaches the API;
- GitLab group enforcement, including redirects out of the group;
- preview-only enforcement: every recorded Cloud Run or Logging request must
  target the preview project and region, `project`/`region` arguments are
  rejected, and the production-project guard holds;
- the `allow_create` gate and the unmanaged-service refusal;
- DML/DDL rejection at the lexical layer and at the dry-run layer, and the
  bytes cap;
- secret redaction (a fake GitLab echoes the token back in its error);
- pagination, output bounds, and IAM add/remove/no-op;
- that the `policy` catalog matches the servers and their annotations.
