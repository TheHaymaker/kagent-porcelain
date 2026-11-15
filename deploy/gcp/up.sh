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
#   up.sh model      Gemini (API key) as a real model, and a one-turn
#                    real-model check (workbench-smoke chat)
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
  cat > "$STATE/workbench-values.yaml" <<V
kagent: {release: kagent, fullname: kagent}
web:
  image: $REGISTRY/workbench-web:$TAG
  host: localhost
  ingress: {enabled: false}
  runtimeConfig:
    apiBaseUrl: /api
    defaultAgent: $NAMESPACE/workbench-smoke
    oidcIssuer: ${OIDC_ISSUER:-https://accounts.google.com}
    oidcClientId: ${OIDC_CLIENT_ID:-unset}
config:
  publicUrl: http://localhost:8080
  identity:
    issuers:
      # Test users for the smoke test; tokens are signed with the key in
      # deploy/gcp/.state/secrets/smoke. Remove for anything but a test cluster.
      - kind: company
        url: $SMOKE_ISSUER
        audiences: [workbench]
        jwksFile: /var/run/secrets/workbench/smoke-jwks.json
        surface: web
$( [[ -n ${OIDC_CLIENT_ID:-} ]] && printf '      - kind: company\n        url: %s\n        audiences: [%s]\n        surface: web\n' "${OIDC_ISSUER:-https://accounts.google.com}" "$OIDC_CLIENT_ID" )
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
GEMINI_KEY_NAME=workbench-gemini

# gemini_answers reports whether a model answers on the Gemini API with the
# key in $1, which proves the key works and the model exists.
gemini_answers() {
  local key=$1 model=$2 code
  code=$(curl -s -o "$STATE/gemini-probe.json" -w '%{http_code}' -X POST \
    -H "x-goog-api-key: $key" -H 'Content-Type: application/json' \
    "https://generativelanguage.googleapis.com/v1beta/models/$model:generateContent" \
    -d '{"contents":[{"role":"user","parts":[{"text":"Reply with OK."}]}]}')
  [[ $code == 200 ]] && return 0
  echo "  $model: HTTP $code $(head -c 300 "$STATE/gemini-probe.json" | tr '\n' ' ')" >&2
  return 1
}

# remove_vertex_sa undoes an earlier attempt that gave agents a Vertex AI
# service account key: kagent cannot mount key files into Substrate actors.
remove_vertex_sa() {
  local sa="workbench-vertex@${PROJECT_ID}.iam.gserviceaccount.com"
  gc iam service-accounts describe "$sa" >/dev/null 2>&1 || return 0
  say "Removing the unused workbench-vertex service account and its key"
  gc projects remove-iam-policy-binding "$PROJECT_ID" --member "serviceAccount:$sa" \
    --role roles/aiplatform.user --condition None >/dev/null 2>&1 || true
  gc iam service-accounts delete "$sa"
  rm -f "$STATE/secrets/vertex-key.json"
  kubectl -n "$NAMESPACE" delete secret workbench-vertex --ignore-not-found
}

phase_model() {
  kubeconfig
  mkdir -p "$STATE/secrets"
  remove_vertex_sa

  # Substrate injects the key into the agent's model requests at its egress
  # proxy (x-goog-api-key to generativelanguage.googleapis.com), so the key
  # never enters the actor. Vertex AI would need a mounted key file or the
  # GKE metadata server, neither of which a Substrate actor has.
  say "Gemini API key (restricted to the Gemini API)"
  gc services enable apikeys.googleapis.com generativelanguage.googleapis.com
  local keyfile="$STATE/secrets/gemini-api-key" name
  if [[ ! -s $keyfile ]]; then
    name=$(gc services api-keys list --filter "displayName=$GEMINI_KEY_NAME" --format 'value(name)' | head -1)
    if [[ -z $name ]]; then
      name=$(gc services api-keys create --display-name "$GEMINI_KEY_NAME" \
        --api-target service=generativelanguage.googleapis.com --format 'value(response.name)') ||
        die "cannot create an API key: the deploying account needs API Keys Admin (roles/serviceusage.apiKeysAdmin)"
    fi
    (umask 077; gc services api-keys get-key-string "$name" --format 'value(keyString)' > "$keyfile")
  fi
  local key
  key=$(<"$keyfile")

  if [[ -z ${GEMINI_MODEL:-} ]]; then
    local m try
    # A new key can take a minute to work.
    for try in 1 2 3 4; do
      for m in $GEMINI_CANDIDATES; do
        if gemini_answers "$key" "$m"; then GEMINI_MODEL=$m; break 2; fi
      done
      sleep 20
    done
    [[ -n ${GEMINI_MODEL:-} ]] || die "none of [$GEMINI_CANDIDATES] answered on the Gemini API (errors above); set GEMINI_MODEL"
  else
    gemini_answers "$key" "$GEMINI_MODEL" || die "$GEMINI_MODEL did not answer on the Gemini API (error above)"
  fi
  echo "  using $GEMINI_MODEL"
  kubectl -n "$NAMESPACE" create secret generic workbench-gemini --from-file=GOOGLE_API_KEY="$keyfile" \
    --dry-run=client -o yaml | kubectl apply -f -

  say "Model and check agent"
  NAMESPACE=$NAMESPACE GEMINI_MODEL=$GEMINI_MODEL \
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

  say "Real-model check"
  if ! smoke_api chat -agent "$NAMESPACE/workbench-gemini-check"; then
    say "Real-model check failed; recent controller warnings and errors"
    kubectl -n "$NAMESPACE" logs deploy/kagent-controller --all-containers --since=15m 2>&1 |
      grep -iE 'level=(warn|error)|"level":"(warn|error)"|panic|gemini|credential' | grep -v remotemcpserver | tail -60 >&2 || true
    return 1
  fi
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
  all) phase_check; phase_infra; phase_images; phase_kagent; phase_workbench; phase_smoke ;;
  status) phase_status ;;
  down) phase_down ;;
  *) sed -n '2,25p' "$0"; exit 2 ;;
esac
