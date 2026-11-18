# IAM for a python-fastapi-cloudrun app

Three identities touch an app. Each gets the least it needs.

## 1. Runtime service account (one per app)

`app-<name>@<project>.iam.gserviceaccount.com`, used as the Cloud Run service
identity (`--service-account`). Never the Compute Engine default account.

| Needs | Role | Scope |
| --- | --- | --- |
| Write logs | none - Cloud Run collects stdout itself | - |
| Nothing else by default | - | - |

Add only what a feature needs, on the narrowest resource:

| Feature | Role | Scope |
| --- | --- | --- |
| Persistent data in Firestore | `roles/datastore.user` | the app's own database (IAM condition on `resource.name`) |
| Call another internal Cloud Run service | `roles/run.invoker` | that one service |
| Read one secret | `roles/secretmanager.secretAccessor` | that one secret |

Do not grant project-level `Editor`, `Owner`, or any `*Admin` role.

## 2. Who can open the app (IAP)

The service is deployed with IAP enabled and without unauthenticated access:

- `roles/iap.httpsResourceAccessor` on the service for the requester's Google
  group (Workbench sets this from the app's audience when it registers access).
- The IAP service agent `service-<project-number>@gcp-sa-iap.iam.gserviceaccount.com`
  gets `roles/run.invoker` on the service.
- Set `IAP_AUDIENCE` (`/projects/<project-number>/locations/<region>/services/<service>`)
  so the app verifies the signed `X-Goog-IAP-JWT-Assertion` instead of trusting
  the plain email header.

## 3. Deployers

| Identity | Can | Roles |
| --- | --- | --- |
| GitLab CI (`ci-workbench-apps@`) via Workload Identity Federation | build and push images | `roles/cloudbuild.builds.editor` on the build project; `roles/artifactregistry.writer` on the `workbench-apps` repository; `roles/storage.admin` on its own Cloud Build source bucket only |
| cloudrun-mcp tool server | deploy **preview** services | `roles/run.developer` on the preview project; `roles/iam.serviceAccountUser` on each app runtime account |
| Workbench promotion (after approval) | deploy **production** | `roles/run.developer` on the production project; `roles/iam.serviceAccountUser` on each app runtime account |

The builder agent itself has no Google identity at all.

## Required APIs

`run.googleapis.com`, `iap.googleapis.com`, `artifactregistry.googleapis.com`,
`cloudbuild.googleapis.com` (CI), `logging.googleapis.com`.
