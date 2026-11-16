#!/usr/bin/env bash
# Deploy Workbench to a GKE test cluster and verify it, in phases:
#
#   up.sh check      tools, credentials and project
#   up.sh infra      APIs, Artifact Registry, GKE cluster (with the
#                    certificate APIs Substrate needs)
#   up.sh images     build every image with Cloud Build
#   up.sh kagent     stock kagent + Substrate via `kagent install` (PRD phase 1)
#   up.sh workbench  secrets, configuration, database grants, then kagent's
#                    chart again with the Workbench overlay
#   up.sh smoke      the end-to-end smoke test (server/cmd/workbench-smoke)
#   up.sh model      Gemini on Vertex AI as a real model, and a one-turn
#                    real-model check (workbench-smoke chat)
#   up.sh expose     public HTTPS address behind Google sign-in and a Cloud
#                    Armor allowlist (ADR 0005); needs WORKBENCH_ADMINS and
#                    WORKBENCH_ALLOWED_RANGES; with WORKBENCH_GOOGLE_CLIENT_ID
#                    the web app signs in with Google's own button
#   up.sh unexpose   back to private (port-forward) with the smoke test's issuer
#   up.sh gitlab-find  list GitLab connections Google Cloud holds (read-only)
#   up.sh gitlab-check what GITLAB_TOKEN acts as and can reach (read-only)
#   up.sh gitlab-setup GitLab group, apps/templates subgroups, scaffolds
#   up.sh gitlab-republish  the same, replacing the template projects' files with scaffolds/
#   up.sh tools      builds repo, buckets, service accounts and the builder's
#                    tool servers (needs gitlab-setup)
#   up.sh builder    the Gemini builder agent and its planner, tester and reviewer helpers
#   up.sh web-check  diagnose web sign-in: the /__/ handler proxy (read-only)
#   up.sh tools-check  tool servers' recent failed calls, preview services (read-only)
#   up.sh all        every phase in order
#   up.sh status     what is running
#   up.sh down       delete the cluster (the registry and images stay)
#
# Every phase is safe to re-run. Settings come from the environment:
#   PROJECT_ID (required)   REGION=us-central1   CLUSTER=workbench-dev
#   NAMESPACE=kagent        TAG=<git short sha>
#   MODEL_PROVIDER / its API key, for kagent's default model (optional; the
#   smoke test brings its own scripted model)
#   GEMINI_MODEL=<first of GEMINI_CANDIDATES that answers>
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
STATE="$ROOT/deploy/gcp/.state"
# kagent release v1.0.1, which server/go.mod pins. Its charts are published
# as 1.0.1 (kagent's release workflow strips the v) and pairs with Substrate 1.0.0.
KAGENT_COMMIT=558e3d761d8ed2b0d23bfef1867a9d1915210121
KAGENT_CHART_VERSION=1.0.1
SUBSTRATE_CHART_VERSION=1.0.0

: "${PROJECT_ID:?set PROJECT_ID to the GCP test project}"
REGION=${REGION:-us-central1}
ZONE=${ZONE:-${REGION}-a}
CLUSTER=${CLUSTER:-workbench-dev}
NAMESPACE=${NAMESPACE:-kagent}
TAG=${TAG:-$(git -C "$ROOT" rev-parse --short HEAD)}
REGISTRY="${REGION}-docker.pkg.dev/${PROJECT_ID}/workbench"
PUSH_ISSUER="https://workbench-dev.${PROJECT_ID}.invalid"
SMOKE_ISSUER="https://smoke.workbench.test"
export GOTOOLCHAIN=auto

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null || die "$1 is not installed${2:+ ($2)}"; }

gc() { gcloud --project "$PROJECT_ID" --quiet "$@"; }

phase_check() {
  say "Checking tools and credentials"
  need gcloud; need kubectl; need helm; need go; need openssl; need python3
  [[ -n $(gcloud auth list --filter=status:ACTIVE --format='value(account)') ]] || die "gcloud has no active account"
  # Projects created by some Google tools start with even these APIs off.
  # Turning them on changes nothing else, and needs serviceusage permission.
  gc services enable serviceusage.googleapis.com cloudresourcemanager.googleapis.com cloudbilling.googleapis.com ||
    die "cannot enable APIs in $PROJECT_ID. Either the Service Usage API is off (turn it on once in the console:
  https://console.cloud.google.com/apis/library/serviceusage.googleapis.com?project=$PROJECT_ID), or the
  service account lacks Owner or Service Usage Admin on the project. The gcloud error above says which."
  gc projects describe "$PROJECT_ID" --format='value(projectId)' >/dev/null || die "cannot read project $PROJECT_ID"
  local billing account
  billing=$(gcloud billing projects describe "$PROJECT_ID" --format='value(billingEnabled)' 2>/dev/null || echo unknown)
  case $billing in
    True) echo "billing: enabled" ;;
    False) die "billing is not enabled on $PROJECT_ID; GKE, Cloud Build and Artifact Registry need it" ;;
    *) echo "billing: could not check (the account may lack billing viewer); infra will fail if it is off" ;;
  esac
  account=$(gcloud auth list --filter=status:ACTIVE --format='value(account)')
  echo "roles held by $account on $PROJECT_ID:"
  gc projects get-iam-policy "$PROJECT_ID" --flatten='bindings[].members' \
    --filter="bindings.members:serviceAccount:$account" --format='value(bindings.role)' 2>/dev/null | sed 's/^/  /' ||
    echo "  (cannot read the project's IAM policy)"
  echo "project $PROJECT_ID, region $REGION, cluster $CLUSTER, namespace $NAMESPACE, tag $TAG"
  mkdir -p "$STATE"
}

phase_infra() {
  say "Enabling APIs"
  gc services enable container.googleapis.com artifactregistry.googleapis.com cloudbuild.googleapis.com \
    iam.googleapis.com run.googleapis.com bigquery.googleapis.com logging.googleapis.com

  say "Artifact Registry repository"
  gc artifacts repositories describe workbench --location "$REGION" >/dev/null 2>&1 ||
    gc artifacts repositories create workbench --location "$REGION" --repository-format docker \
      --description "Workbench images"

  say "GKE cluster $CLUSTER"
  if ! gc container clusters describe "$CLUSTER" --zone "$ZONE" >/dev/null 2>&1; then
    # Standard, not Autopilot: Substrate's node agent needs privileged pods.
    # Substrate projects pod identities with ClusterTrustBundle and
    # PodCertificateRequest, beta APIs GKE serves only when asked at creation.
    gc container clusters create "$CLUSTER" --zone "$ZONE" --release-channel rapid \
      --machine-type e2-standard-8 --num-nodes 3 --workload-pool "${PROJECT_ID}.svc.id.goog" \
      --enable-kubernetes-unstable-apis "certificates.k8s.io/v1beta1/clustertrustbundles,certificates.k8s.io/v1beta1/podcertificaterequests"
  fi
  kubeconfig
  say "Checking the certificate APIs Substrate needs"
  # Capture first: grep -q exits at its first match, and with pipefail the
  # SIGPIPE that kills kubectl would read as the resource being missing.
  local served
  served=$(kubectl api-resources --api-group certificates.k8s.io -o name) ||
    die "cannot list the cluster's certificates.k8s.io resources"
  for r in clustertrustbundles podcertificaterequests; do
    grep -q "^$r\." <<<"$served" ||
      die "the cluster does not serve $r.certificates.k8s.io; Substrate cannot run here (see deploy/gcp/README.md)"
  done
  echo "ok: $(tr '\n' ' ' <<<"$served")"
}

# kubeconfig writes a kubeconfig from a short-lived gcloud access token, so the
# GKE auth plugin is not needed. Re-run each phase; tokens last an hour.
kubeconfig() {
  local endpoint ca
  endpoint=$(gc container clusters describe "$CLUSTER" --zone "$ZONE" --format='value(endpoint)')
  ca=$(gc container clusters describe "$CLUSTER" --zone "$ZONE" --format='value(masterAuth.clusterCaCertificate)')
  mkdir -p "$STATE"
  export KUBECONFIG="$STATE/kubeconfig"
  cat > "$KUBECONFIG" <<KC
apiVersion: v1
kind: Config
clusters: [{name: $CLUSTER, cluster: {server: "https://$endpoint", certificate-authority-data: "$ca"}}]
users: [{name: gcloud, user: {token: "$(gcloud auth print-access-token)"}}]
contexts: [{name: $CLUSTER, context: {cluster: $CLUSTER, user: gcloud, namespace: $NAMESPACE}}]
current-context: $CLUSTER
KC
  chmod 600 "$KUBECONFIG"
}

phase_images() {
  say "Building images with Cloud Build ($REGISTRY, tag $TAG)"
  # Streaming build logs needs Viewer on Google's default logs bucket, which
  # an Editor account lacks; wait for the result instead and link the logs.
  local id status
  id=$(gc builds submit "$ROOT" --region "$REGION" --config "$ROOT/deploy/gcp/cloudbuild.yaml" \
    --substitutions "_REGISTRY=$REGISTRY,_TAG=$TAG" --async --format='value(id)')
  echo "build $id: https://console.cloud.google.com/cloud-build/builds;region=$REGION/$id?project=$PROJECT_ID"
  while :; do
    status=$(gc builds describe "$id" --region "$REGION" --format='value(status)')
    case $status in
      SUCCESS) echo "build succeeded"; break ;;
      QUEUED|PENDING|WORKING) sleep 15 ;;
      *) gc builds describe "$id" --region "$REGION" --format='yaml(status,statusDetail,failureInfo,steps[].id,steps[].status)'
         die "image build $id ended as $status" ;;
    esac
  done
}

# kagent_cli builds the kagent CLI at the commit Workbench is built against
# and prints its path; everything else it says goes to stderr, because the
# caller captures stdout.
kagent_cli() {
  local bin="$STATE/kagent"
  if [[ ! -x $bin ]]; then
    say "Building the kagent CLI at ${KAGENT_COMMIT:0:7}" >&2
    local src="$STATE/kagent-src"
    [[ -d $src ]] || git clone -q https://github.com/kagent-dev/kagent.git "$src"
    git -C "$src" fetch -q --depth 1 origin "$KAGENT_COMMIT" && git -C "$src" checkout -q "$KAGENT_COMMIT"
    # Stamp the release the way kagent's own release build does; the
    # installer refuses to run from a CLI that reports no release.
    local v=github.com/kagent-dev/kagent/go/core/internal/version
    (cd "$src/go" && go build -o "$bin" -ldflags "-X $v.Version=$KAGENT_CHART_VERSION \
      -X $v.GitCommit=${KAGENT_COMMIT:0:7} -X $v.SubstrateVersion=$SUBSTRATE_CHART_VERSION" ./core/cli/cmd/kagent) >&2
  fi
  echo "$bin"
}

kagent_install() {
  local cli; cli=$(kagent_cli)
  local provider=${MODEL_PROVIDER:-Ollama}
  # kagent install insists on a key for every provider. With no real model
  # configured, the default is an Ollama ModelConfig nobody calls (the smoke
  # test brings its own scripted model), and Ollama does not check keys.
  if [[ $provider == Ollama && -z ${OLLAMA_API_KEY:-} ]]; then
    export OLLAMA_API_KEY=placeholder-no-model-configured
  fi
  # The CLI config lists values files in order; later files win.
  # A CLI built from source has no release version, so the chart versions
  # are explicit.
  cat > "$STATE/kagent-cli.yaml" <<CFG
connection:
  namespace: $NAMESPACE
install:
  defaultModelProvider: $provider
  kagent:
    version: $KAGENT_CHART_VERSION
    valuesFiles: [$1]
  substrate:
    version: $SUBSTRATE_CHART_VERSION
CFG
  if ! "$cli" --config "$STATE/kagent-cli.yaml" install; then
    # kagent install hides helm's output in case it holds credentials. Our
    # values files hold none, so render the chart directly to see why.
    say "kagent install failed; rendering kagent's chart directly to show helm's error" >&2
    local args=() f
    IFS=, read -ra files <<<"$1"
    for f in "${files[@]}"; do args+=(-f "$f"); done
    helm template kagent oci://ghcr.io/kagent-dev/kagent/helm/kagent --version "$KAGENT_CHART_VERSION" \
      -n "$NAMESPACE" "${args[@]}" \
      --set database.postgres.readWriteConnectionStringSecretRef.name=diagnostic \
      --set database.postgres.ownerConnectionStringSecretRef.name=diagnostic >/dev/null || true
    # A chart that renders but never becomes ready means a pod is failing;
    # show what the cluster says about it.
    if kubectl -n "$NAMESPACE" get deploy kagent-controller >/dev/null 2>&1; then
      say "Pods in $NAMESPACE" >&2
      kubectl -n "$NAMESPACE" get pods -o wide >&2 || true
      say "Controller pod events and status" >&2
      kubectl -n "$NAMESPACE" describe pod -l app.kubernetes.io/component=controller >&2 | tail -60 || true
      say "Controller logs (current, then previous crash)" >&2
      kubectl -n "$NAMESPACE" logs deploy/kagent-controller --all-containers --tail=120 >&2 || true
      kubectl -n "$NAMESPACE" logs deploy/kagent-controller --all-containers --previous --tail=120 >&2 || true
    fi
    die "kagent install failed"
  fi
}

phase_kagent() {
  kubeconfig
  say "Installing stock kagent and Substrate (PRD phase 1)"
  cat > "$STATE/kagent-stock.yaml" <<V
config:
  push:
    issuer: $PUSH_ISSUER
V
  kagent_install "$STATE/kagent-stock.yaml"
  kubectl -n "$NAMESPACE" rollout status deploy -l app.kubernetes.io/component=controller --timeout 10m
  echo "Stock kagent is running. Its own UI: kubectl -n $NAMESPACE port-forward svc/kagent-ui 8080:8080"
}


phase_workbench() {
  kubeconfig
  local secrets="$STATE/secrets"; mkdir -p "$secrets"; chmod 700 "$secrets"
  say "Generating secrets (kept in $secrets, never committed)"
  [[ -s $secrets/internal-key ]] || openssl rand -base64 48 > "$secrets/internal-key"
  [[ -s $secrets/usage-secret ]] || openssl rand -hex 32 > "$secrets/usage-secret"
  [[ -s $secrets/push-private-key.pem ]] || openssl genpkey -algorithm ED25519 -out "$secrets/push-private-key.pem"
  if [[ ! -s $secrets/smoke/jwks.json ]]; then
    mkdir -p "$secrets/smoke"
    (cd "$ROOT/server" && go run ./cmd/workbench-smoke keys -dir "$secrets/smoke")
  fi
  kubectl -n "$NAMESPACE" create secret generic workbench-secrets --dry-run=client -o yaml \
    --from-file=internal-key="$secrets/internal-key" --from-file=usage-secret="$secrets/usage-secret" \
    --from-file=smoke-jwks.json="$secrets/smoke/jwks.json" | kubectl apply -f -
  kubectl -n "$NAMESPACE" create secret generic workbench-push-signing --dry-run=client -o yaml \
    --from-file=private-key.pem="$secrets/push-private-key.pem" | kubectl apply -f -

  say "Workbench configuration"
  load_exposure
  cat > "$STATE/workbench-values.yaml" <<V
kagent: {release: kagent, fullname: kagent}
web:
  image: $REGISTRY/workbench-web:$TAG
$(web_values)
config:
$(identity_values)
    internalKeyFile: /var/run/secrets/workbench/internal-key
  authorization:
    platformGroups: [workbench-platform]
    requesterGroups: [workbench-requesters]
    auditorGroups: [workbench-audit]
    agentNamespaces: [$NAMESPACE, workbench-builders]
  defaultApproverGroup: workbench-approvers
  hooks:
    bindAddress: ":8090"
    callbackUrl: http://workbench-hooks.$NAMESPACE.svc.cluster.local:8090/hooks/a2a
    pushIssuer: $PUSH_ISSUER
    usageSecretFile: /var/run/secrets/workbench/usage-secret
  production:
    # Promotions are recorded but nothing is deployed until a production
    # project is configured.
    deployer: log
V
  helm upgrade --install workbench "$ROOT/deploy/helm/workbench" -n "$NAMESPACE" -f "$STATE/workbench-values.yaml"

  say "Database grants for Workbench's schema"
  local pg; pg=$(kubectl -n "$NAMESPACE" get pod -l app.kubernetes.io/name=kagent,app.kubernetes.io/component=postgresql -o name 2>/dev/null | head -1)
  [[ -n $pg ]] || pg=$(kubectl -n "$NAMESPACE" get pod -o name | grep postgresql | head -1)
  [[ -n $pg ]] || die "cannot find kagent's bundled PostgreSQL pod"
  kubectl -n "$NAMESPACE" exec -i "$pg" -c postgresql -- sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' \
    < "$ROOT/deploy/database/grants.sql"

  say "kagent's chart with the Workbench overlay"
  cat > "$STATE/kagent-workbench.yaml" <<V
controller:
  image: {registry: "${REGION}-docker.pkg.dev", repository: "${PROJECT_ID}/workbench/workbench-server", tag: "$TAG"}
  serviceAccount: {annotations: {}}
config:
  push: {issuer: $PUSH_ISSUER, allowHttp: true, allowPrivateNetworks: true}
ui: {externalUrl: ""}
V
  kagent_install "$ROOT/deploy/helm/values-workbench.yaml,$STATE/kagent-workbench.yaml"
  kubectl -n "$NAMESPACE" rollout status deploy -l app.kubernetes.io/component=controller --timeout 10m
  kubectl -n "$NAMESPACE" exec -i "$pg" -c postgresql -- sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "REVOKE UPDATE, DELETE, TRUNCATE ON workbench.audit_event FROM kagent_readwrite"' || true
  echo "Workbench is running in place of kagent's controller."
}

phase_smoke() {
  kubeconfig
  load_exposure
  [[ -z $EXPOSED ]] || die "the cluster is public, so the smoke test's issuer is off (ADR 0005); run up.sh unexpose first"
  say "Smoke test agent"
  local cp
  cp=$(kubectl -n "$NAMESPACE" get configmap kagent-controller -o jsonpath='{.data.controller\.yaml}' |
    python3 -c 'import sys,yaml; print(yaml.safe_load(sys.stdin)["runtimeDefaults"]["controlPlaneUrl"].rstrip("/"))')
  NAMESPACE=$NAMESPACE MOCKLLM_IMAGE="$REGISTRY/workbench-mockllm:$TAG" CONTROL_PLANE_URL=$cp \
    python3 -c 'import os,string,sys; print(string.Template(sys.stdin.read()).substitute(os.environ))' \
    < "$ROOT/deploy/smoke/smoke.yaml.tmpl" | kubectl apply -f -
  kubectl -n "$NAMESPACE" rollout status deploy/workbench-mockllm --timeout 5m
  kubectl -n "$NAMESPACE" wait agent/workbench-smoke --for=condition=Ready --timeout 5m

  say "Running the smoke test through a port-forward"
  kubectl -n "$NAMESPACE" port-forward svc/kagent-controller 18083:8083 >/dev/null 2>&1 &
  local pf=$!; trap 'kill $pf 2>/dev/null' RETURN
  sleep 3
  if ! (cd "$ROOT/server" && go run ./cmd/workbench-smoke run -api http://localhost:18083 \
    -agent "$NAMESPACE/workbench-smoke" -key "$STATE/secrets/smoke/signing-key.json" -issuer "$SMOKE_ISSUER"); then
    say "Smoke test failed; recent controller warnings and errors"
    kubectl -n "$NAMESPACE" logs deploy/kagent-controller --all-containers --since=15m 2>&1 |
      grep -iE 'level=(warn|error)|"level":"(warn|error)"|panic' | tail -60 >&2 || true
    kubectl -n "$NAMESPACE" logs deploy/workbench-mockllm --tail=40 >&2 || true
    return 1
  fi
}

# smoke_api runs workbench-smoke against the controller through a
# port-forward. The first argument is the subcommand.
smoke_api() {
  kubectl -n "$NAMESPACE" port-forward svc/kagent-controller 18083:8083 >/dev/null 2>&1 &
  local pf=$! rc=0
  sleep 3
  (cd "$ROOT/server" && go run ./cmd/workbench-smoke "$@" -api http://localhost:18083 \
    -key "$STATE/secrets/smoke/signing-key.json" -issuer "$SMOKE_ISSUER") || rc=$?
  kill $pf 2>/dev/null || true
  return $rc
}

GEMINI_CANDIDATES=${GEMINI_CANDIDATES:-"gemini-3-flash gemini-3-flash-preview gemini-2.5-flash"}
VERTEX_SA="workbench-vertex@${PROJECT_ID}.iam.gserviceaccount.com"
VERTEX_KEY_NAME=workbench-vertex-key
# kagent's Gemini client appends /models/{model}:generateContent to this.
GEMINI_BASE_URL="https://aiplatform.googleapis.com/v1/projects/${PROJECT_ID}/locations/global/publishers/google"

# gemini_answers reports whether a model answers on Vertex AI with the API
# key in $1, which proves the key, its service account's role and the model.
gemini_answers() {
  local key=$1 model=$2 code
  code=$(curl -s -o "$STATE/gemini-probe.json" -w '%{http_code}' -X POST \
    -H "x-goog-api-key: $key" -H 'Content-Type: application/json' \
    "$GEMINI_BASE_URL/models/$model:generateContent" \
    -d '{"contents":[{"role":"user","parts":[{"text":"Reply with OK."}]}]}')
  [[ $code == 200 ]] && return 0
  echo "  $model: HTTP $code $(head -c 300 "$STATE/gemini-probe.json" | tr '\n' ' ')" >&2
  return 1
}

# revoke_keys deletes every API key with the given display name. Key
# operations never print their result: gcloud's includes the key string.
api_key_force_delete() {
  local code
  code=$(curl -s -o "$STATE/apikey-delete.json" -w '%{http_code}' -X DELETE \
    -H "Authorization: Bearer $(gcloud auth print-access-token)" \
    "https://apikeys.googleapis.com/v2/$1?checkExistingUsage=SKIP")
  if [[ $code != 200 ]]; then
    echo "  force delete: HTTP $code $(grep -vi keystring "$STATE/apikey-delete.json" | head -c 400 | tr '\n' ' ')" >&2
    rm -f "$STATE/apikey-delete.json"
    return 1
  fi
  rm -f "$STATE/apikey-delete.json"
}

revoke_keys() {
  local name names
  names=$(gc services api-keys list --filter "displayName=$1" --format 'value(name)' 2>"$STATE/apikey.err") || {
    grep -vi keystring "$STATE/apikey.err" >&2; die "cannot list API keys named $1"
  }
  for name in $names; do
    if gc services api-keys delete "$name" --format none 2>"$STATE/apikey.err"; then
      echo "  revoked API key $name ($1)"
    elif grep -q APIKEYS_DELETE_INCOMPATIBLE_WITH_USAGE "$STATE/apikey.err" && api_key_force_delete "$name"; then
      # Google refuses to delete a key used in the last week unless told to
      # skip that check; a key being revoked is exactly that case.
      echo "  revoked API key $name ($1), overriding the recent-usage check"
    else
      grep -vi keystring "$STATE/apikey.err" >&2
      die "cannot revoke API key $name ($1)"
    fi
  done
  rm -f "$STATE/apikey.err"
}

phase_model() {
  kubeconfig
  mkdir -p "$STATE/secrets"
  gc services enable apikeys.googleapis.com aiplatform.googleapis.com iam.googleapis.com >/dev/null

  # An earlier version created a Gemini API key and its gcloud output put the
  # key in a job log. Revoke any such key; Vertex AI replaces it.
  revoke_keys workbench-gemini
  rm -f "$STATE/secrets/gemini-api-key"
  kubectl -n "$NAMESPACE" delete secret workbench-vertex --ignore-not-found >/dev/null

  # Substrate actors have neither the GKE metadata server nor support for a
  # mounted key file, so Vertex AI's usual credentials are out. A Vertex AI
  # API key bound to a service account works instead: Substrate's egress
  # proxy adds it to the agent's requests as x-goog-api-key, so it never
  # enters the actor, and it bills through the project's Cloud Billing.
  say "Service account for agents' model calls (Vertex AI User only)"
  gc iam service-accounts describe "$VERTEX_SA" >/dev/null 2>&1 ||
    gc iam service-accounts create workbench-vertex --display-name "Workbench agents: Vertex AI only" ||
    die "cannot create $VERTEX_SA: the deploying account needs Service Account Admin (roles/iam.serviceAccountAdmin)"
  local try granted=
  for try in 1 2 3 4 5 6; do
    if gc projects add-iam-policy-binding "$PROJECT_ID" --member "serviceAccount:$VERTEX_SA" \
      --role roles/aiplatform.user --condition None >/dev/null 2>&1; then
      granted=1; break
    fi
    sleep $((try * 5))
  done
  [[ -n $granted ]] || die "cannot grant Vertex AI User to $VERTEX_SA (the deploying account needs Project IAM Admin)"

  say "Vertex AI API key bound to $VERTEX_SA"
  local keyfile="$STATE/secrets/vertex-api-key" name
  if [[ ! -s $keyfile ]]; then
    revoke_keys "$VERTEX_KEY_NAME"
    name=$(gc services api-keys create --display-name "$VERTEX_KEY_NAME" \
      --api-target service=aiplatform.googleapis.com --service-account "$VERTEX_SA" \
      --format 'value(response.name)' 2>"$STATE/apikey.err") || {
      grep -v -i keystring "$STATE/apikey.err" >&2; rm -f "$STATE/apikey.err"
      die "cannot create a Vertex AI API key bound to $VERTEX_SA (error above). The deploying account needs
  API Keys Admin, and the organization policy must allow API keys bound to service accounts."
    }
    rm -f "$STATE/apikey.err"
    (umask 077; gc services api-keys get-key-string "$name" --format 'value(keyString)' > "$keyfile" 2>/dev/null)
  fi
  local key
  key=$(<"$keyfile")
  [[ -n ${GITHUB_ACTIONS:-} ]] && echo "::add-mask::$key"

  if [[ -z ${GEMINI_MODEL:-} ]]; then
    local m
    # A new key, and a new role binding, can take a minute or two to work.
    for try in 1 2 3 4 5 6; do
      for m in $GEMINI_CANDIDATES; do
        if gemini_answers "$key" "$m"; then GEMINI_MODEL=$m; break 2; fi
      done
      sleep 20
    done
    [[ -n ${GEMINI_MODEL:-} ]] || die "none of [$GEMINI_CANDIDATES] answered on Vertex AI (errors above); set GEMINI_MODEL"
  else
    gemini_answers "$key" "$GEMINI_MODEL" || die "$GEMINI_MODEL did not answer on Vertex AI (error above)"
  fi
  echo "  using $GEMINI_MODEL"
  kubectl -n "$NAMESPACE" delete secret workbench-gemini --ignore-not-found >/dev/null
  kubectl -n "$NAMESPACE" create secret generic workbench-gemini --from-file=GOOGLE_API_KEY="$keyfile" \
    --dry-run=client -o yaml | kubectl apply -f -

  say "Model and check agent"
  NAMESPACE=$NAMESPACE GEMINI_MODEL=$GEMINI_MODEL GEMINI_BASE_URL=$GEMINI_BASE_URL \
    python3 -c 'import os,string,sys; print(string.Template(sys.stdin.read()).substitute(os.environ))' \
    < "$ROOT/deploy/model/gemini.yaml.tmpl" | kubectl apply -f -
  if ! kubectl -n "$NAMESPACE" wait agent/workbench-gemini-check --for=condition=Ready --timeout 5m; then
    say "The check agent is not Ready; its status"
    local kind
    for kind in modelconfig/workbench-gemini agenttemplate/workbench-gemini-check agent/workbench-gemini-check; do
      echo "--- $kind" >&2
      kubectl -n "$NAMESPACE" get "$kind" -o jsonpath='{.status}' >&2 || true; echo >&2
    done
    return 1
  fi

  load_exposure
  if [[ -n $EXPOSED ]]; then
    echo "The cluster is public, so the test issuer is off; try the model at https://$EXPOSE_HOST instead."
    return 0
  fi
  say "Real-model check"
  if ! smoke_api chat -agent "$NAMESPACE/workbench-gemini-check"; then
    say "Real-model check failed; recent controller warnings and errors"
    kubectl -n "$NAMESPACE" logs deploy/kagent-controller --all-containers --since=15m 2>&1 |
      grep -iE 'level=(warn|error)|"level":"(warn|error)"|panic|gemini|credential' | grep -v remotemcpserver | tail -60 >&2 || true
    return 1
  fi
}

# --- Public exposure (ADR 0005) -------------------------------------------
# Settings persist in the private state directory so every later phase
# renders the same public configuration. up.sh unexpose removes them.
EXPOSE_ENV="$STATE/secrets/expose.env"
EXPOSED=
load_exposure() {
  EXPOSED=
  if [[ -s $EXPOSE_ENV ]]; then
    # shellcheck disable=SC1090
    source "$EXPOSE_ENV"
    EXPOSED=1
  fi
}

web_values() {
  if [[ -z $EXPOSED ]]; then
    cat <<W
  host: localhost
  ingress: {enabled: false}
  runtimeConfig:
    apiBaseUrl: /api
    defaultAgent: $NAMESPACE/workbench-smoke
    oidcIssuer: ${OIDC_ISSUER:-https://accounts.google.com}
    oidcClientId: ${OIDC_CLIENT_ID:-unset}
W
    return
  fi
  cat <<W
  host: $EXPOSE_HOST
  ingress: {enabled: true}
  authHandlerUpstream: https://${PROJECT_ID}.firebaseapp.com
  gke:
    staticIPName: workbench-web
    managedCertificate: true
    backendConfig: {securityPolicy: workbench-allowlist}
  runtimeConfig:
    apiBaseUrl: /api
    defaultAgent: $NAMESPACE/workbench-builder
$(if [[ -n ${WORKBENCH_GOOGLE_CLIENT_ID:-} ]]; then
  printf '    authProvider: google\n    googleClientId: %s\n' "$WORKBENCH_GOOGLE_CLIENT_ID"
else
  printf '    authProvider: identity-platform\n'
fi)
    identityPlatform:
      apiKey: $(<"$STATE/secrets/browser-api-key")
      # The app's own host: nginx proxies the sign-in handler at /__/.
      authDomain: $EXPOSE_HOST
      projectId: $PROJECT_ID
W
}

identity_values() {
  if [[ -z $EXPOSED ]]; then
    cat <<W
  publicUrl: http://localhost:8080
  identity:
    issuers:
      # Test users for the smoke test; tokens are signed with the key in
      # deploy/gcp/.state/secrets/smoke. Never on a public cluster.
      - kind: company
        url: $SMOKE_ISSUER
        audiences: [workbench]
        jwksFile: /var/run/secrets/workbench/smoke-jwks.json
        surface: web
$( [[ -n ${OIDC_CLIENT_ID:-} ]] && printf '      - kind: company\n        url: %s\n        audiences: [%s]\n        surface: web\n' "${OIDC_ISSUER:-https://accounts.google.com}" "$OIDC_CLIENT_ID" )
W
    return
  fi
  local email
  cat <<W
  publicUrl: https://$EXPOSE_HOST
  publicExposure: true
  identity:
    issuers:
      # Google Identity Platform. Its tokens carry no groups, so the admins
      # listed at expose time get every Workbench role by verified email.
      - kind: company
        url: https://securetoken.google.com/$PROJECT_ID
        audiences: [$PROJECT_ID]
        jwksUrl: https://www.googleapis.com/service_accounts/v1/jwk/securetoken@system.gserviceaccount.com
        surface: web
        emailGroups:
W
  for email in ${WORKBENCH_ADMINS//,/ }; do
    printf '          "%s": [workbench-requesters, workbench-approvers, workbench-platform, workbench-audit]\n' "$email"
  done
  [[ -n ${WORKBENCH_GOOGLE_CLIENT_ID:-} ]] || return 0
  cat <<W
      # Google Identity Services ("Sign in with Google"): Google ID tokens for
      # the web app's OAuth client. Same roles by verified email.
      - kind: company
        url: https://accounts.google.com
        audiences: [$WORKBENCH_GOOGLE_CLIENT_ID]
        surface: web
        emailGroups:
W
  for email in ${WORKBENCH_ADMINS//,/ }; do
    printf '          "%s": [workbench-requesters, workbench-approvers, workbench-platform, workbench-audit]\n' "$email"
  done
}

# idp calls the Identity Platform admin API as the deploying account.
idp() {
  local method=$1 path=$2 body=${3:-}
  curl -s -o "$STATE/idp.json" -w '%{http_code}' -X "$method" \
    -H "Authorization: Bearer $(gcloud auth print-access-token)" -H "X-Goog-User-Project: $PROJECT_ID" \
    -H 'Content-Type: application/json' ${body:+-d "$body"} \
    "https://identitytoolkit.googleapis.com/$path"
}

phase_expose() {
  kubeconfig
  [[ -n ${WORKBENCH_ADMINS:-} ]] || die "set WORKBENCH_ADMINS to the comma-separated Google accounts that may sign in"
  [[ -n ${WORKBENCH_ALLOWED_RANGES:-} ]] || die "set WORKBENCH_ALLOWED_RANGES to the comma-separated source CIDRs allowed through Cloud Armor
  (your public IP as a.b.c.d/32; 0.0.0.0/0 deliberately opens it to everyone)"
  mkdir -p "$STATE/secrets"
  gc services enable compute.googleapis.com identitytoolkit.googleapis.com apikeys.googleapis.com >/dev/null

  say "Static IP and hostname"
  gc compute addresses describe workbench-web --global >/dev/null 2>&1 ||
    gc compute addresses create workbench-web --global >/dev/null
  local ip host
  ip=$(gc compute addresses describe workbench-web --global --format 'value(address)')
  host="${ip//./-}.sslip.io"
  echo "  $ip -> https://$host"

  say "Cloud Armor allowlist"
  gc compute security-policies describe workbench-allowlist >/dev/null 2>&1 ||
    gc compute security-policies create workbench-allowlist --description "Workbench: allowed source ranges (ADR 0005)" >/dev/null
  gc compute security-policies rules update 2147483647 --security-policy workbench-allowlist --action deny-403 >/dev/null
  if gc compute security-policies rules describe 1000 --security-policy workbench-allowlist >/dev/null 2>&1; then
    gc compute security-policies rules update 1000 --security-policy workbench-allowlist \
      --src-ip-ranges "$WORKBENCH_ALLOWED_RANGES" --action allow >/dev/null
  else
    gc compute security-policies rules create 1000 --security-policy workbench-allowlist \
      --src-ip-ranges "$WORKBENCH_ALLOWED_RANGES" --action allow >/dev/null
  fi
  echo "  allowing $(tr ',' '\n' <<<"$WORKBENCH_ALLOWED_RANGES" | wc -l | tr -d ' ') source range(s); everything else gets 403"

  say "Identity Platform"
  local code
  code=$(idp POST "v2/projects/$PROJECT_ID/identityPlatform:initializeAuth" '{}')
  [[ $code == 200 ]] || grep -qi "already" "$STATE/idp.json" ||
    echo "  initializeAuth: HTTP $code $(head -c 300 "$STATE/idp.json" | tr '\n' ' ')" >&2
  code=$(idp GET "admin/v2/projects/$PROJECT_ID/config")
  [[ $code == 200 ]] || die "cannot read Identity Platform config: HTTP $code $(head -c 300 "$STATE/idp.json")"
  local domains
  domains=$(python3 -c 'import json,sys; c=json.load(open(sys.argv[1])); d=c.get("authorizedDomains",[]);
for x in sys.argv[2:]:
    d.append(x) if x not in d else None
print(json.dumps({"authorizedDomains": d}))' "$STATE/idp.json" "$host" "${PROJECT_ID}.firebaseapp.com" localhost)
  code=$(idp PATCH "admin/v2/projects/$PROJECT_ID/config?updateMask=authorizedDomains" "$domains")
  [[ $code == 200 ]] || die "cannot set authorized domains: HTTP $code $(head -c 300 "$STATE/idp.json")"
  echo "  authorized domains include $host"
  local google_ready=
  code=$(idp GET "admin/v2/projects/$PROJECT_ID/defaultSupportedIdpConfigs/google.com")
  [[ $code == 200 ]] && python3 -c 'import json,sys; sys.exit(0 if json.load(open(sys.argv[1])).get("enabled") else 1)' "$STATE/idp.json" && google_ready=1
  rm -f "$STATE/idp.json"

  say "Browser API key (public by design: Identity Toolkit only, from $host and the auth domain)"
  local keyfile="$STATE/secrets/browser-api-key" name
  if [[ ! -s $keyfile ]]; then
    revoke_keys workbench-web-browser
    name=$(gc services api-keys create --display-name workbench-web-browser \
      --api-target service=identitytoolkit.googleapis.com --api-target service=securetoken.googleapis.com \
      --allowed-referrers "https://$host/*,https://${PROJECT_ID}.firebaseapp.com/*" --format 'value(response.name)' 2>"$STATE/apikey.err") || {
      grep -vi keystring "$STATE/apikey.err" >&2; rm -f "$STATE/apikey.err"; die "cannot create the browser API key"
    }
    rm -f "$STATE/apikey.err"
    (umask 077; gc services api-keys get-key-string "$name" --format 'value(keyString)' > "$keyfile" 2>/dev/null)
  fi
  # The sign-in popup runs on the auth domain's handler page, which calls
  # Identity Toolkit with the same key, so both sites must be allowed.
  for name in $(gc services api-keys list --filter "displayName=workbench-web-browser" --format 'value(name)'); do
    gc services api-keys update "$name" --format none \
      --allowed-referrers "https://$host/*,https://${PROJECT_ID}.firebaseapp.com/*" 2>"$STATE/apikey.err" || {
      grep -vi keystring "$STATE/apikey.err" >&2; rm -f "$STATE/apikey.err"; die "cannot update the browser API key's allowed sites"
    }
  done
  rm -f "$STATE/apikey.err"

  # Keep a previously configured client ID if this run was not given one.
  if [[ -z ${WORKBENCH_GOOGLE_CLIENT_ID:-} && -s $EXPOSE_ENV ]]; then
    WORKBENCH_GOOGLE_CLIENT_ID=$(sed -n "s/^WORKBENCH_GOOGLE_CLIENT_ID=//p" "$EXPOSE_ENV")
  fi
  (umask 077; printf 'EXPOSE_HOST=%q\nWORKBENCH_ADMINS=%q\nWORKBENCH_GOOGLE_CLIENT_ID=%q\n' \
    "$host" "$WORKBENCH_ADMINS" "${WORKBENCH_GOOGLE_CLIENT_ID:-}" > "$EXPOSE_ENV")
  phase_workbench

  say "Load balancer"
  local addr=
  for _ in $(seq 1 60); do
    addr=$(kubectl -n "$NAMESPACE" get ingress workbench-web -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || true)
    [[ -n $addr ]] && break
    sleep 10
  done
  [[ -n $addr ]] || die "the load balancer did not get an address in 10 minutes: kubectl -n $NAMESPACE describe ingress workbench-web"
  echo "  load balancer up at $addr"
  echo "  certificate: $(kubectl -n "$NAMESPACE" get managedcertificate workbench-web -o jsonpath='{.status.certificateStatus}' 2>/dev/null || echo unknown)"
  echo "  (a new Google-managed certificate takes 15-60 minutes to become Active)"
  echo
  echo "Workbench: https://$host"
  if [[ -z $google_ready ]]; then
    cat <<M

Sign-in needs one step in the console before it works:
  1. APIs & Services > OAuth consent screen: set it up (External, add yourself as a test user).
  2. APIs & Services > Credentials > Create OAuth client ID > Web application, with redirect URIs
       https://$host/__/auth/handler and https://${PROJECT_ID}.firebaseapp.com/__/auth/handler
  3. Identity Platform > Providers > Add a provider > Google: paste that client ID and secret, enable it.
Then open https://$host and sign in with an account in WORKBENCH_ADMINS.
M
  fi
}

phase_unexpose() {
  kubeconfig
  load_exposure
  [[ -n $EXPOSED ]] || { echo "not exposed"; return 0; }
  say "Removing public exposure (the static IP, allowlist and keys stay for next time)"
  rm -f "$EXPOSE_ENV"
  phase_workbench
}

# phase_gitlab_find lists Google Cloud's GitLab connections (Developer
# Connect and Cloud Build repositories) and the Secret Manager secrets they
# reference. Read-only: it enables nothing and reads no secret values.
phase_gitlab_find() {
  local enabled api tok
  enabled=$(gc services list --enabled --format 'value(config.name)')
  tok=$(gcloud auth print-access-token)
  say "GitLab connections in $PROJECT_ID"
  local found=
  for api in developerconnect cloudbuild; do
    if ! grep -qx "$api.googleapis.com" <<<"$enabled"; then
      echo "  $api.googleapis.com is not enabled; no connections there"
      continue
    fi
    local url="https://developerconnect.googleapis.com/v1/projects/$PROJECT_ID/locations/-/connections"
    [[ $api == cloudbuild ]] && url="https://cloudbuild.googleapis.com/v2/projects/$PROJECT_ID/locations/-/connections"
    local code
    code=$(curl -s -o "$STATE/conn.json" -w '%{http_code}' -H "Authorization: Bearer $tok" "$url")
    if [[ $code != 200 ]]; then
      echo "  $api: HTTP $code $(head -c 200 "$STATE/conn.json" | tr '\n' ' ')"
      continue
    fi
    python3 - "$STATE/conn.json" "$api" <<'PY' && found=1 || true
import json, sys
data = json.load(open(sys.argv[1]))
api = sys.argv[2]
hits = 0
for c in data.get("connections", []):
    for kind in ("gitlabConfig", "gitlabEnterpriseConfig"):
        g = c.get(kind)
        if not g:
            continue
        hits += 1
        print(f"  [{api}] {c['name']}")
        print(f"      kind: {kind}, host: {g.get('hostUri', 'https://gitlab.com')}")
        for role in ("authorizerCredential", "readAuthorizerCredential"):
            cred = g.get(role) or {}
            if cred:
                print(f"      {role}: user={cred.get('username', '?')} secret={cred.get('userTokenSecretVersion', '?')}")
        print(f"      state: {c.get('installationState', {}).get('stage', c.get('state', '?'))}, disabled: {c.get('disabled', False)}")
sys.exit(0 if hits else 1)
PY
  done
  rm -f "$STATE/conn.json"
  [[ -n $found ]] || echo "  no GitLab connections found"
  echo
  echo "authorizerCredential is the token with the api scope; that is the one the builder needs."
}

# phase_gitlab_check verifies GITLAB_TOKEN against GITLAB_BASE_URL and lists
# what it can reach. Read-only; the token is never printed.
GITLAB_BASE_URL=${GITLAB_BASE_URL:-https://gitlab.com}
gitlab_api() {
  curl -s -o "$STATE/gitlab.json" -w '%{http_code}' -H "PRIVATE-TOKEN: $GITLAB_TOKEN" "$GITLAB_BASE_URL/api/v4/$1"
}
phase_gitlab_check() {
  [[ -n ${GITLAB_TOKEN:-} ]] || die "GITLAB_TOKEN is not set (the deploy environment's GITLAB_TOKEN secret)"
  [[ -n ${GITHUB_ACTIONS:-} ]] && echo "::add-mask::$GITLAB_TOKEN"
  say "GitLab token at $GITLAB_BASE_URL"
  local code
  local out="$STATE/gitlab-report"; mkdir -p "$out"
  code=$(gitlab_api user); [[ $code == 200 ]] || die "GET /user: HTTP $code $(head -c 200 "$STATE/gitlab.json")"
  mv "$STATE/gitlab.json" "$out/user.json"
  gitlab_api personal_access_tokens/self >/dev/null; mv "$STATE/gitlab.json" "$out/token.json"
  gitlab_api "groups?min_access_level=30&per_page=100&order_by=path" >/dev/null; mv "$STATE/gitlab.json" "$out/groups.json"
  python3 - "$out" <<'PY'
import json, os, sys
d = sys.argv[1]
def load(name):
    try:
        return json.load(open(os.path.join(d, name)))
    except Exception:
        return None
u = load("user.json") or {}
print("  acts as: %s (bot: %s)" % (u.get("username"), u.get("bot", False)))
t = load("token.json")
if isinstance(t, dict) and "scopes" in t:
    print("  scopes: %s; expires: %s" % (",".join(t["scopes"]), t.get("expires_at")))
g = load("groups.json")
print("  groups with Developer or higher:")
if isinstance(g, list) and g:
    for x in g:
        print("    " + x["full_path"])
else:
    print("    (none)")
PY
  rm -rf "$out"
  rm -f "$STATE/gitlab.json"
}

# phase_gitlab_setup creates <GITLAB_GROUP>, its apps and templates
# subgroups, and publishes the scaffolds as template projects. The group
# paths (not secret) are kept in the state directory for later phases.
GITLAB_GROUP=${GITLAB_GROUP:-thehaymaker-workbench}
phase_gitlab_setup() {
  [[ -n ${GITLAB_TOKEN:-} ]] || die "GITLAB_TOKEN is not set (the deploy environment's GITLAB_TOKEN secret)"
  [[ -n ${GITHUB_ACTIONS:-} ]] && echo "::add-mask::$GITLAB_TOKEN"
  mkdir -p "$STATE/secrets"
  say "GitLab groups and scaffold templates"
  local out
  out=$(GITLAB_BASE_URL=$GITLAB_BASE_URL python3 "$ROOT/deploy/gitlab/setup.py" --group "$GITLAB_GROUP" ${GITLAB_REPUBLISH:+--republish})
  grep -v '^GITLAB_' <<<"$out"
  { echo "GITLAB_BASE_URL=$GITLAB_BASE_URL"; grep '^GITLAB_' <<<"$out"; } > "$STATE/secrets/gitlab.env"
  grep '^GITLAB_' <<<"$out" | sed 's/^/  /'
}

# phase_tools creates the Google Cloud resources the builder's tools use and
# deploys the tool servers (gitlab, build, cloudrun, artifactregistry) with
# their RemoteMCPServers in the agents' namespace. Needs gitlab-setup first.
phase_tools() {
  kubeconfig
  [[ -s $STATE/secrets/gitlab.env ]] || die "run up.sh gitlab-setup first"
  [[ -n ${GITLAB_TOKEN:-} ]] || die "GITLAB_TOKEN is not set"
  [[ -n ${GITHUB_ACTIONS:-} ]] && echo "::add-mask::$GITLAB_TOKEN"
  # shellcheck disable=SC1091
  source "$STATE/secrets/gitlab.env"
  local number pool build_sa preview_sa bucket
  number=$(gc projects describe "$PROJECT_ID" --format 'value(projectNumber)')
  pool="principal://iam.googleapis.com/projects/$number/locations/global/workloadIdentityPools/$PROJECT_ID.svc.id.goog/subject/ns/workbench-tools/sa"
  build_sa="workbench-builds@$PROJECT_ID.iam.gserviceaccount.com"
  preview_sa="workbench-preview@$PROJECT_ID.iam.gserviceaccount.com"
  bucket="$PROJECT_ID-workbench-build-sources"

  say "Google Cloud resources for builds and previews"
  gc services enable cloudbuild.googleapis.com run.googleapis.com artifactregistry.googleapis.com logging.googleapis.com >/dev/null
  gc artifacts repositories describe builds --location "$REGION" >/dev/null 2>&1 ||
    gc artifacts repositories create builds --location "$REGION" --repository-format docker \
      --description "Workbench app images built by build-mcp" >/dev/null
  gcloud storage buckets describe "gs://$bucket" >/dev/null 2>&1 ||
    gcloud storage buckets create "gs://$bucket" --project "$PROJECT_ID" --location "$REGION" \
      --uniform-bucket-level-access --public-access-prevention >/dev/null
  echo '{"rule":[{"action":{"type":"Delete"},"condition":{"age":7}}]}' > "$STATE/lifecycle.json"
  gcloud storage buckets update "gs://$bucket" --lifecycle-file "$STATE/lifecycle.json" >/dev/null
  rm -f "$STATE/lifecycle.json"
  local sa
  for sa in workbench-builds:"Workbench app image builds" workbench-preview:"Workbench preview apps (Firestore only)"; do
    gc iam service-accounts describe "${sa%%:*}@$PROJECT_ID.iam.gserviceaccount.com" >/dev/null 2>&1 ||
      gc iam service-accounts create "${sa%%:*}" --display-name "${sa#*:}" >/dev/null
  done
  echo "  builds repository, staging bucket (7-day lifecycle), build and preview service accounts"

  say "Roles (Workload Identity for the tool servers)"
  # New service accounts take a little while to be visible to IAM policy.
  retry() {
    local try
    for try in 1 2 3 4 5 6 7 8; do
      "$@" >/dev/null 2>"$STATE/iam.err" && { rm -f "$STATE/iam.err"; return 0; }
      sleep $((try * 5))
    done
    cat "$STATE/iam.err" >&2
    die "failed after retries: $*"
  }
  grant_project() { retry gc projects add-iam-policy-binding "$PROJECT_ID" --member "$1" --role "$2" --condition None; }
  grant_project "serviceAccount:$build_sa" roles/logging.logWriter
  retry gc artifacts repositories add-iam-policy-binding builds --location "$REGION" \
    --member "serviceAccount:$build_sa" --role roles/artifactregistry.writer
  retry gcloud storage buckets add-iam-policy-binding "gs://$bucket" --member "serviceAccount:$build_sa" \
    --role roles/storage.objectViewer
  # build-mcp: start builds as workbench-builds, write sources.
  grant_project "$pool/build-mcp" roles/cloudbuild.builds.editor
  # Read its builds' logs, so a failed build or a check run can say why.
  grant_project "$pool/build-mcp" roles/logging.viewer
  retry gcloud storage buckets add-iam-policy-binding "gs://$bucket" --member "$pool/build-mcp" \
    --role roles/storage.objectCreator
  retry gc iam service-accounts add-iam-policy-binding "$build_sa" --member "$pool/build-mcp" \
    --role roles/iam.serviceAccountUser
  # cloudrun-mcp: manage preview services running as workbench-preview, read their logs.
  grant_project "$pool/cloudrun-mcp" roles/run.admin
  grant_project "$pool/cloudrun-mcp" roles/logging.viewer
  retry gc iam service-accounts add-iam-policy-binding "$preview_sa" --member "$pool/cloudrun-mcp" \
    --role roles/iam.serviceAccountUser
  # Cloud Run checks that whoever deploys an image may download it.
  retry gc artifacts repositories add-iam-policy-binding builds --location "$REGION" \
    --member "$pool/cloudrun-mcp" --role roles/artifactregistry.reader
  # artifactregistry-mcp: read the builds repository.
  retry gc artifacts repositories add-iam-policy-binding builds --location "$REGION" \
    --member "$pool/artifactregistry-mcp" --role roles/artifactregistry.reader
  echo "  granted"

  # Test versions that keep data use Firestore (Native mode): one shared
  # (default) database, each app in collections prefixed with its name. The
  # preview identity may read and write it, and nothing else in the project.
  say "Firestore for test versions"
  gc services enable firestore.googleapis.com >/dev/null
  if ! gc firestore databases describe --database='(default)' >/dev/null 2>&1; then
    retry gc firestore databases create --database='(default)' --location="$REGION" --type=firestore-native >/dev/null
    echo "  created the (default) database in $REGION"
  else
    echo "  the (default) database exists"
  fi
  grant_project "serviceAccount:$preview_sa" roles/datastore.user

  # Test versions open in a browser behind IAP (Google sign-in, access per
  # person or group). IAP calls Cloud Run as its service agent; cloudrun-mcp
  # grants access on each test version.
  say "IAP for test versions"
  gc services enable iap.googleapis.com >/dev/null
  # Create IAP's service agent (gcloud's command for this is beta-only).
  local code
  code=$(curl -s -o "$STATE/iap-identity.json" -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $(gcloud auth print-access-token)" -H 'Content-Type: application/json' -d '{}' \
    "https://serviceusage.googleapis.com/v1beta1/projects/$number/services/iap.googleapis.com:generateServiceIdentity")
  [[ $code == 200 ]] || { head -c 300 "$STATE/iap-identity.json" >&2; die "cannot create IAP's service agent (HTTP $code)"; }
  rm -f "$STATE/iap-identity.json"
  grant_project "serviceAccount:service-$number@gcp-sa-iap.iam.gserviceaccount.com" roles/run.invoker
  grant_project "$pool/cloudrun-mcp" roles/iap.admin
  # Without a Google Workspace organization, IAP needs its own OAuth client
  # for personal Google accounts. The client secret comes from the deploy
  # environment and is written only to a private temporary file.
  if [[ -n ${IAP_OAUTH_CLIENT_SECRET:-} && -n ${WORKBENCH_GOOGLE_CLIENT_ID:-} ]]; then
    [[ -n ${GITHUB_ACTIONS:-} ]] && echo "::add-mask::$IAP_OAUTH_CLIENT_SECRET"
    local iapfile="$STATE/iap-settings.yaml"
    (umask 077; printf 'access_settings:\n  oauth_settings:\n    client_id: %s\n    client_secret: %s\n' \
      "$WORKBENCH_GOOGLE_CLIENT_ID" "$IAP_OAUTH_CLIENT_SECRET" > "$iapfile")
    if gc iap settings set "$iapfile" --resource-type=iap_web >/dev/null 2>"$STATE/iap.err"; then
      echo "  IAP uses the web app's OAuth client (personal Google accounts can sign in)"
    else
      grep -vi secret "$STATE/iap.err" >&2 || true
      echo "  warning: could not set IAP's OAuth client; personal Google accounts may be refused" >&2
    fi
    rm -f "$iapfile" "$STATE/iap.err"
  else
    echo "  note: IAP_OAUTH_CLIENT_SECRET is not set; without a Workspace organization, IAP sign-in may fail"
  fi

  say "Tool servers"
  kubectl create namespace workbench-tools --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n workbench-tools create secret generic gitlab-mcp-token --from-literal=token="$GITLAB_TOKEN" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  NAMESPACE=$NAMESPACE REGISTRY=$REGISTRY TAG=$TAG PROJECT_ID=$PROJECT_ID REGION=$REGION \
    GITLAB_BASE_URL=$GITLAB_BASE_URL GITLAB_ALLOWED_GROUP=$GITLAB_ALLOWED_GROUP GITLAB_TEMPLATE_GROUP=$GITLAB_TEMPLATE_GROUP \
    BUILD_BUCKET=$bucket BUILD_SA=$build_sa PREVIEW_SA=$preview_sa \
    python3 -c 'import os,string,sys; print(string.Template(sys.stdin.read()).substitute(os.environ))' \
    < "$ROOT/deploy/tools/tools.yaml.tmpl" | kubectl apply -f -
  local d ok=1
  for d in gitlab-mcp build-mcp cloudrun-mcp artifactregistry-mcp; do
    if ! kubectl -n workbench-tools rollout status "deploy/$d" --timeout 3m; then
      ok=
      kubectl -n workbench-tools logs "deploy/$d" --tail=30 >&2 || true
    fi
  done
  [[ -n $ok ]] || die "a tool server did not start (logs above)"
}

# phase_builder deploys the Gemini builder agent (needs model and tools).
phase_builder() {
  kubeconfig
  kubectl -n "$NAMESPACE" get modelconfig workbench-gemini >/dev/null 2>&1 || die "run up.sh model first"
  kubectl -n "$NAMESPACE" get remotemcpserver gitlab-mcp >/dev/null 2>&1 || die "run up.sh tools first"
  say "Builder agent and its helpers (Gemini)"
  local a args=()
  for a in builder planner tester reviewer; do
    args+=(--from-file="$a.md=$ROOT/deploy/agents/$a-gemini.md")
  done
  kubectl -n "$NAMESPACE" create configmap workbench-builder-prompt "${args[@]}" --dry-run=client -o yaml | kubectl apply -f -
  NAMESPACE=$NAMESPACE python3 -c 'import os,string,sys; print(string.Template(sys.stdin.read()).substitute(os.environ))' \
    < "$ROOT/deploy/agents/builder-gemini.yaml.tmpl" | kubectl apply -f -
  if ! kubectl -n "$NAMESPACE" wait agent/workbench-builder --for=condition=Ready --timeout 5m; then
    kubectl -n "$NAMESPACE" get agent workbench-builder -o jsonpath='{.status}' >&2; echo >&2
    for a in builder planner tester reviewer; do
      echo "--- agenttemplate/workbench-$a" >&2
      kubectl -n "$NAMESPACE" get agenttemplate "workbench-$a" -o jsonpath='{.status}' >&2 || true; echo >&2
    done
    local r
    for r in workbench-api gitlab-mcp build-mcp artifactregistry-mcp cloudrun-mcp; do
      echo "--- remotemcpserver/$r" >&2
      kubectl -n "$NAMESPACE" get remotemcpserver "$r" -o jsonpath='{.status.conditions}' >&2 || true; echo >&2
    done
    return 1
  fi
  load_exposure
  [[ -n $EXPOSED ]] && echo "The builder is the web app's default agent at https://$EXPOSE_HOST (after the next workbench or expose run)."
}

# phase_web_check fetches the sign-in handler's pages through the web
# service (as the browser would, via nginx's /__/ proxy) and directly from
# Google, and shows nginx's recent errors. Read-only.
phase_web_check() {
  kubeconfig
  local up="https://${PROJECT_ID}.firebaseapp.com"
  say "Sign-in handler through workbench-web and directly"
  kubectl -n "$NAMESPACE" run web-check --rm -i --restart=Never --quiet \
    --image=curlimages/curl:8.11.1 --command -- sh -c '
      for base in "http://workbench-web" "'"$up"'"; do
        for p in /__/auth/handler /__/auth/iframe /__/auth/iframe.js /__/firebase/init.json; do
          printf "%-45s " "$base$p"
          curl -s -o /tmp/b -D /tmp/h -w "%{http_code} %{content_type} %{size_download}B\n" "$base$p"
          grep -iE "^(x-frame-options|content-security-policy|location):" /tmp/h | sed "s/^/    /"
          case "$p" in *init.json) head -c 300 /tmp/b | sed "s/^/    /"; echo;; esac
        done
      done' || true
  say "workbench-web nginx errors (last 30; access lines are never printed)"
  kubectl -n "$NAMESPACE" logs -l app.kubernetes.io/name=workbench-web --tail=200 2>&1 |
    grep -E "\[(error|warn|crit)\]" | tail -30 || true
  say "Rejected sign-in tokens (last 15; reasons only)"
  kubectl -n "$NAMESPACE" logs deploy/kagent-controller --all-containers --since=2h 2>&1 |
    grep '"token rejected"' | sed -E 's/"(email|sub|ip|remote_addr)":"[^"]*"/"\1":"…"/g' | tail -15 || true
  kubectl -n "$NAMESPACE" get configmap workbench-web-config -o jsonpath='{.data.config\.json}' |
    python3 -c 'import json,sys; c=json.load(sys.stdin); ip=c.get("identityPlatform",{}); print("config: authProvider=%s authDomain=%s projectId=%s" % (c.get("authProvider"), ip.get("authDomain"), ip.get("projectId")))'
}

# phase_tools_check shows the tool servers' recent failed calls (their
# tool_call audit lines, already redacted) and the preview services' state.
# Read-only.
phase_tools_check() {
  kubeconfig
  local d
  for d in cloudrun-mcp build-mcp gitlab-mcp artifactregistry-mcp; do
    say "$d: failed calls in the last 3 hours (newest last)"
    kubectl -n workbench-tools logs "deploy/$d" --since=3h 2>&1 |
      grep '"outcome":"error"' | python3 -c '
import json, sys
for line in sys.stdin:
    try:
        e = json.loads(line)
    except ValueError:
        continue
    print("  %s %s: %s" % (e.get("time", "")[11:19], e.get("tool"), e.get("error", "")[:600]))
' | tail -12 || true
  done
  say "cloudrun-mcp: calls that changed something in the last 3 hours"
  kubectl -n workbench-tools logs deploy/cloudrun-mcp --since=3h 2>&1 |
    grep '"read_only":false' | python3 -c '
import json, sys
for line in sys.stdin:
    try:
        e = json.loads(line)
    except ValueError:
        continue
    print("  %s %s %s" % (e.get("time", "")[11:19], e.get("tool"), e.get("outcome")))
' | tail -12 || true
  say "Preview services"
  gc run services list --region "$REGION" --format 'table(metadata.name,status.conditions[0].status,status.conditions[0].message)' 2>&1 | head -20 || true
  # Who may open each preview through IAP. Emails are masked (the Actions log
  # is public); an admin from WORKBENCH_ADMINS is named as such.
  local svc
  for svc in $(gc run services list --region "$REGION" --format 'value(metadata.name)' 2>/dev/null | grep '^wb-preview-'); do
    echo "  $svc: iap=$(gc run services describe "$svc" --region "$REGION" --format 'value(metadata.annotations["run.googleapis.com/iap-enabled"])' 2>/dev/null || echo '?')"
    gc iap web get-iam-policy --resource-type=cloud-run --service="$svc" --region="$REGION" --format=json 2>&1 |
      ADMINS="${WORKBENCH_ADMINS:-}" python3 -c '
import json, os, sys
raw = sys.stdin.read()
try:
    pol = json.loads(raw)
except ValueError:
    print("    could not read the IAP policy: " + raw.strip().splitlines()[-1][:300] if raw.strip() else "    empty answer")
    sys.exit(0)
admins = {a.strip().lower() for a in os.environ.get("ADMINS", "").split(",") if a.strip()}
def mask(m):
    kind, _, who = m.partition(":")
    if who.lower() in admins:
        return kind + ":<a workbench admin>"
    user, _, dom = who.partition("@")
    return "%s:%s…@%s" % (kind, user[:2], dom) if dom else m
found = False
for b in pol.get("bindings", []):
    print("    %s%s: %s" % (b.get("role"), " (conditional)" if b.get("condition") else "", ", ".join(mask(m) for m in b.get("members", []))))
    found = True
if not found:
    print("    no bindings: nobody may open it")
' || true
    echo "    recent errors (emails masked):"
    gc logging read "resource.type=cloud_run_revision AND resource.labels.service_name=$svc AND severity>=ERROR" \
      --freshness=3h --limit=3 --format=json 2>&1 | python3 -c '
import json, re, sys
raw = sys.stdin.read()
try:
    entries = json.loads(raw or "[]")
except ValueError:
    print(raw[-400:]); sys.exit(0)
for e in entries:
    jp = e.get("jsonPayload") or {}
    text = e.get("textPayload") or jp.get("message", "")
    st = jp.get("stack_trace", "")
    out = (e.get("timestamp", "")[11:19] + " " + text + ("\n" + "\n".join(st.splitlines()[-12:]) if st else ""))
    print(re.sub(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+", "<email>", out))
' | sed 's/^/      /' || true
  done
}

phase_status() {
  kubeconfig
  kubectl -n "$NAMESPACE" get pods,svc
  kubectl get agents -A 2>/dev/null || true
}

phase_down() {
  say "Deleting cluster $CLUSTER (images and the registry stay)"
  gc container clusters delete "$CLUSTER" --zone "$ZONE"
}

case ${1:-} in
  check) phase_check ;;
  infra) phase_check; phase_infra ;;
  images) phase_check; phase_images ;;
  kagent) phase_check; phase_kagent ;;
  workbench) phase_check; phase_workbench ;;
  smoke) phase_check; phase_smoke ;;
  model) phase_check; phase_model ;;
  gitlab-find) phase_check; phase_gitlab_find ;;
  gitlab-check) phase_gitlab_check ;;
  gitlab-setup) phase_gitlab_setup ;;
  gitlab-republish) GITLAB_REPUBLISH=1 phase_gitlab_setup ;;
  tools) phase_check; phase_tools ;;
  builder) phase_check; phase_builder ;;
  web-check) phase_web_check ;;
  tools-check) phase_check >/dev/null; phase_tools_check ;;
  expose) phase_check; phase_expose ;;
  unexpose) phase_check; phase_unexpose ;;
  all) phase_check; phase_infra; phase_images; phase_kagent; phase_workbench; phase_smoke ;;
  status) phase_status ;;
  down) phase_down ;;
  *) sed -n '2,37p' "$0"; exit 2 ;;
esac
