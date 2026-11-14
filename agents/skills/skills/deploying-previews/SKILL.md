---
name: deploying-previews
description: How to get an app from a pushed branch to a working preview on Cloud Run - waiting for CI to build the image, finding the image digest, calling cloudrun_deploy_preview, verifying health, recording the deployment in Workbench and showing it to the requester. Use whenever the requester should try a new version.
---

# Deploying previews

A preview is a Cloud Run service in the preview project, behind IAP, that the
requester (and people they share it with) can open. Previews are cheap and
yours to create; production is not (see `requesting-production`).

## Steps

1. **Push and open a merge request.** `git push -u origin <branch>`, then
   `gitlab_create_merge_request` (source = your branch, target = `main`, a
   plain-language title). Keep the MR iid.
2. **Wait for CI.** Poll the MR pipeline with the gitlab tools (every 20-30 s,
   not faster). The `test` job must pass. If it fails, read the job log, fix,
   push again; do not deploy a failing build. Update Progress: step
   "Checking it works" -> `running` / `done` / `failed`.
3. **Find the image.** The `build-image` job pushes
   `<region>-docker.pkg.dev/<project>/<repo>/<app>:<commit sha>` and prints
   `IMAGE_REF=...@sha256:...`. Confirm the digest with the artifactregistry
   tools and always deploy by digest, never by tag.
4. **Deploy.** Call `cloudrun_deploy_preview` with the app name, the image
   digest reference, the scaffold's container port and health path (from
   `scaffold.yaml`), and the app's runtime service account
   `app-<name>@...`. For `streamlit-bigquery`, also request session affinity and
   a 3600 s timeout, and pass `BQ_PROJECT`, `BQ_LOCATION`, `MAX_BYTES_BILLED`.
   Show a DeployPreview with `status: "deploying"` while you wait.
   cloudrun-mcp calls are approval-gated: the first deploy of an app creates a
   service (new monthly spend), so Workbench asks the requester to allow it.
   Before calling, tell them in one line: "You'll be asked to allow putting a
   test version online - that's expected." Later calls may also pause until the
   requester chooses to always allow them. Call cloudrun tools one at a time.
5. **Verify.** Read the service status until the new revision is ready. If the
   tool exposes logs, check the first lines for `ERROR` entries. A preview whose
   health check fails is a failed deploy: show DeployPreview `failed`, read the
   logs, fix, and go back to step 1.
6. **Record it.** `workbench_record_deployment` with
   `{appId, environment: "preview", url, revision, image}` - `revision` is the
   Cloud Run revision name the tool returned, `image` the digest reference.
7. **Show it.** Update the DeployPreview to `status: "ready"` with `url` and
   `revision`, mark the Progress step done, and tell the requester what to try.

## Rules

- One preview service per app; each deploy creates a new revision of it.
- Never pass secrets as environment variables. If the app needs one, stop and
  ask the platform team (it goes in Secret Manager, granted to the runtime
  account only).
- Never deploy an image that was not built by the app's own CI pipeline from a
  commit in its repository.
- Do not delete previews; Workbench cleans them up.
