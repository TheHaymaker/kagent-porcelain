# IAM for a streamlit-bigquery app

## 1. Runtime service account: read-only, one per app

`app-<name>@<project>.iam.gserviceaccount.com`, used as the Cloud Run service
identity. It can read exactly the data the app shows and run query jobs; it
cannot write anywhere.

| Role | Granted on | Why |
| --- | --- | --- |
| `roles/bigquery.jobUser` | the `BQ_PROJECT` project | create query jobs (billed there) |
| `roles/bigquery.dataViewer` | each dataset the app reads (or individual tables / an authorized view) | read rows |

Never grant `roles/bigquery.dataEditor`, `roles/bigquery.user` (it can create
datasets), `roles/bigquery.admin`, or `dataViewer` at project level.

For data with row- or column-level sensitivity, prefer an **authorized view**
in a dataset owned by the data team that exposes only what the app needs, and
grant `dataViewer` on that view's dataset only.

The app does not use the BigQuery Storage Read API
(`create_bqstorage_client=False`), so `roles/bigquery.readSessionUser` is not
needed.

## 2. Cost guard rails

- Every query sets `maximum_bytes_billed` (`MAX_BYTES_BILLED`, default 1 GiB):
  BigQuery fails a query that would scan more, instead of billing it. The app
  also dry-runs first (free) and shows a friendly message.
- Queries carry the label `workbench-app=<APP_NAME>`, so billing exports and
  `INFORMATION_SCHEMA.JOBS` attribute cost per app.
- Recommended at project level (platform team): a custom quota on
  "Query usage per day per user" for app service accounts.

## 3. Who can open the app (IAP)

As for every Workbench app: IAP enabled on the Cloud Run service, no
unauthenticated invoker, `roles/iap.httpsResourceAccessor` for the audience's
Google group, `roles/run.invoker` for the IAP service agent, and `IAP_AUDIENCE`
set so the app verifies the IAP JWT.

## 4. Deployers

Same as other golden paths: GitLab CI builds images through Cloud Build into
Artifact Registry (`roles/cloudbuild.builds.editor`,
`roles/artifactregistry.writer` on the repository), cloudrun-mcp deploys
previews, Workbench deploys production after approval. Deployers need
`roles/iam.serviceAccountUser` on this runtime account only.

## Required APIs

`run.googleapis.com`, `iap.googleapis.com`, `bigquery.googleapis.com`,
`artifactregistry.googleapis.com`, `cloudbuild.googleapis.com`,
`logging.googleapis.com`.
