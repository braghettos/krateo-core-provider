#!/usr/bin/env bash
# Real end-to-end test of the CDC apiRef chain against a live kind cluster running real authn
# and real snowplow. Proves: projected SA token -> authn /serviceaccount/login (TokenReview ->
# JWT) -> snowplow /call (Bearer JWT) resolving a RESTAction whose request echoes the extras
# (static + per-instance, request-wins) -> .api.echo.args.
#
# Requirements: docker, kind, kubectl, go, and local checkouts of authn + snowplow next to this
# repo (../authn on its main branch with the serviceaccount strategy; ../snowplow checked out at
# the snowplow tag under test, default 1.1.1).
#
# Usage: hack/apiref-e2e/run.sh            # full setup + test
#        SKIP_BUILD=1 hack/apiref-e2e/run.sh   # reuse already-built/loaded images
set -euo pipefail

CLUSTER=${CLUSTER:-apiref-e2e}
SNOWPLOW_TAG=${SNOWPLOW_TAG:-1.1.1}
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
AUTHN_SRC=${AUTHN_SRC:-$REPO/../authn}
SNOWPLOW_SRC=${SNOWPLOW_SRC:-$REPO/../snowplow}
KUBECONFIG_FILE="$HERE/.kubeconfig"
TOKEN_FILE="$HERE/.sa-token"

echo "==> kind cluster $CLUSTER"
kind get clusters | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --wait 90s
kind get kubeconfig --name "$CLUSTER" > "$KUBECONFIG_FILE"
export KUBECONFIG="$KUBECONFIG_FILE"

if [[ "${SKIP_BUILD:-}" != "1" ]]; then
  echo "==> build images (authn, snowplow:$SNOWPLOW_TAG)"
  ( cd "$SNOWPLOW_SRC" && git checkout -q "$SNOWPLOW_TAG" && docker build -q -t snowplow:e2e --build-arg COMMIT_HASH="$SNOWPLOW_TAG" . )
  ( cd "$AUTHN_SRC" && docker build -q -t authn:e2e . )
  docker pull -q ghcr.io/mccutchen/go-httpbin:latest
fi
echo "==> load images into kind"
kind load docker-image snowplow:e2e authn:e2e ghcr.io/mccutchen/go-httpbin:latest --name "$CLUSTER"

echo "==> CRDs + fixtures"
kubectl apply -f "$HERE/manifests/restaction-crd.yaml" -f "$HERE/manifests/sa-mapping-crd.yaml"
kubectl create namespace demo-system --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "$HERE/manifests/fixtures.yaml"

echo "==> deploy authn + snowplow"
kubectl apply -f "$HERE/manifests/authn-rbac.yaml"
kubectl apply -f "$HERE/manifests/authn-deploy.yaml"
kubectl apply -f "$HERE/manifests/snowplow-deploy.yaml"
kubectl -n demo-system rollout status deploy/echo --timeout=120s
kubectl -n demo-system rollout status deploy/authn --timeout=120s
kubectl -n demo-system rollout status deploy/snowplow --timeout=120s

echo "==> mint an audience-authn token for the CDC ServiceAccount"
kubectl -n demo-system create token cdc-e2e-sa --audience=authn --duration=3600s > "$TOKEN_FILE"

echo "==> port-forward authn:8082 and snowplow:8081"
kubectl -n demo-system port-forward svc/authn 18082:8082 >/tmp/pf-authn.log 2>&1 &
PF_AUTHN=$!
kubectl -n demo-system port-forward svc/snowplow 18081:8081 >/tmp/pf-snowplow.log 2>&1 &
PF_SNOWPLOW=$!
trap 'kill $PF_AUTHN $PF_SNOWPLOW 2>/dev/null || true' EXIT
sleep 3

echo "==> run the e2e test"
cd "$REPO"
APIREF_E2E_AUTHN_URL="http://127.0.0.1:18082" \
APIREF_E2E_SNOWPLOW_URL="http://127.0.0.1:18081" \
APIREF_E2E_TOKEN_PATH="$TOKEN_FILE" \
APIREF_E2E_APIREF_NAME="status-sources" \
APIREF_E2E_APIREF_NAMESPACE="demo-system" \
  go test -tags e2e ./internal/composition/ -run TestE2E_ApiRefChain -v -count=1
