# Deploy to a GKE test cluster

`up.sh` takes an empty GCP project to a running, verified Workbench. Run it
phase by phase the first time, so a failure points at one layer:

```sh
export PROJECT_ID=my-workbench-test   # a throwaway project with billing
deploy/gcp/up.sh check       # tools, credentials, project
deploy/gcp/up.sh infra       # APIs, Artifact Registry, GKE cluster
deploy/gcp/up.sh images      # Cloud Build: server, web, scripted model, tool servers
deploy/gcp/up.sh kagent      # stock kagent + Substrate (PRD phase 1)
deploy/gcp/up.sh workbench   # Workbench in place of kagent's controller
deploy/gcp/up.sh smoke       # end-to-end smoke test
```

`up.sh all` runs them in order. `up.sh down` deletes the cluster.

## Needs

- `gcloud` signed in to an account that can create resources in the project
  (Owner on a test project is simplest), `kubectl`, `helm`, Go 1.27+
  (`GOTOOLCHAIN=auto` fetches it), `openssl`, `python3` with PyYAML.
- No Docker: images are built by Cloud Build.
- No GKE auth plugin: the script writes its kubeconfig from a short-lived
  `gcloud` access token, refreshed at the start of every phase.

## What each phase does

| Phase | Creates | Checks |
|---|---|---|
| `infra` | APIs; Artifact Registry repo `workbench`; GKE Standard cluster on the rapid channel, 3 × e2-standard-8, Workload Identity, with `certificates.k8s.io/v1beta1` ClusterTrustBundle and PodCertificateRequest | the cluster serves both certificate APIs Substrate needs |
| `images` | seven images tagged with the git commit | Cloud Build succeeds |
| `kagent` | kagent CLI built at the pinned commit; `kagent install`: CRDs, PodCertificate controller, bundled PostgreSQL, Substrate, kagent | the controller rolls out |
| `workbench` | generated secrets (in `.state/secrets`, git-ignored); the Workbench chart (config, hooks Service, web app); database grants; kagent's chart again with `deploy/helm/values-workbench.yaml` | the Workbench controller rolls out, migrations apply |
| `smoke` | the scripted model and smoke agent (`deploy/smoke`) | the eight checks in `server/cmd/workbench-smoke` |

## The smoke test

It signs in as two test users through a test issuer whose public key is
mounted from a Secret (`identity.issuers[].jwksFile`): alice, a requester,
and bob, an approver. Alice asks the smoke agent to promote an app. kagent
pauses the call, Workbench turns the pause into an approval, alice is
refused, bob approves, the decision reaches the task and it completes. The
audit trail must name bob deciding for alice, and kagent's push notification
must reach Workbench, which then tells alice the task finished.

The production deployer is `log` here: the promotion is recorded but nothing
is deployed. The real Cloud Run promotion is exercised by the full builder
run, which needs a production project, GitLab and a model.

## If `infra` stops at the certificate APIs

Substrate needs `ClusterTrustBundle` and `PodCertificateRequest`. If the
cluster's Kubernetes version does not serve them as `v1beta1`, list what it
offers with `kubectl api-resources --api-group certificates.k8s.io`, and see
kagent's `scripts/kind/kind-config.yaml` for the feature gates its
development clusters enable.

## Afterwards

```sh
export KUBECONFIG=deploy/gcp/.state/kubeconfig
kubectl -n kagent port-forward svc/workbench-web 8080:80    # the web app
kubectl -n kagent port-forward svc/kagent-ui 8081:8080      # kagent's own UI
```

The web app needs `OIDC_CLIENT_ID` (and `OIDC_ISSUER`, default Google) set
before `up.sh workbench` for real sign-in.
