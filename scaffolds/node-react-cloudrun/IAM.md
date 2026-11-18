# IAM for a node-react-cloudrun app

## 1. Runtime service account (one per app)

`app-<name>@<project>.iam.gserviceaccount.com`, set as the Cloud Run service
identity. Never the Compute Engine default account. By default it needs **no
roles**: Cloud Run collects stdout logs without any grant, and the React
bundle is served by the same process.

Add only what a feature needs, on the narrowest resource:

| Feature | Role | Scope |
| --- | --- | --- |
| Persistent data in Firestore | `roles/datastore.user` | the app's own database (IAM condition) |
| Call another internal Cloud Run service | `roles/run.invoker` | that service only |
| Read a secret | `roles/secretmanager.secretAccessor` | that secret only |
| Read BigQuery | `roles/bigquery.jobUser` on the billing project + `roles/bigquery.dataViewer` on the dataset; always set `maximumBytesBilled` | as streamlit-bigquery |

## 2. Who can open the app (IAP)

IAP enabled on the service, no unauthenticated invoker,
`roles/iap.httpsResourceAccessor` for the audience group, `roles/run.invoker`
for the IAP service agent. Set `IAP_AUDIENCE` so the server verifies the IAP
JWT (`google-auth-library`, IAP public keys) instead of trusting the plain
email header. The browser never handles tokens: API calls are same-origin and
carry IAP's session cookie.

## 3. Deployers

| Identity | Can | Roles |
| --- | --- | --- |
| GitLab CI via Workload Identity Federation | build and push images | `roles/cloudbuild.builds.editor`; `roles/artifactregistry.writer` on the repository; its Cloud Build source bucket only |
| cloudrun-mcp | deploy **preview** | `roles/run.developer` on the preview project; `roles/iam.serviceAccountUser` on this runtime account |
| Workbench (after approval) | deploy **production** | `roles/run.developer` on the production project; `roles/iam.serviceAccountUser` on this runtime account |

## Required APIs

`run.googleapis.com`, `iap.googleapis.com`, `artifactregistry.googleapis.com`,
`cloudbuild.googleapis.com`, `logging.googleapis.com`.
