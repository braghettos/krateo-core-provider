#!/usr/bin/env bash
#
# End-to-end validation of remote (multi-cluster) deployment targeting.
#
# Provisions a local kind cluster (management) and a disposable single-node GKE
# cluster (remote target), mints a self-contained ServiceAccount-token kubeconfig for
# the target (the recipe documented in docs/how-to/remote-target-credentials.md), then
# runs the build-tagged e2e test in internal/tools/clusterkube. Both clusters are torn
# down on exit (success or failure).
#
# What it proves against real clusters:
#   * clusterkube.Remote builds working clients from a kubeconfig Secret in the
#     management cluster and reaches the remote target.
#   * a NoneConverter multi-version CRD (the shape emitted for a remote target without
#     CORE_PROVIDER_WEBHOOK_URL) is accepted and established by the real apiserver.
#   * resources land in the target and NOT in the management cluster (isolation).
#
# Requirements: gcloud (authenticated, project set, GKE API enabled), kubectl, kind, go.
# Usage: scripts/e2e-remote-targeting.sh
set -euo pipefail

ZONE="${GKE_ZONE:-us-central1-a}"
MACHINE="${GKE_MACHINE:-e2-medium}"
CLUSTER="cp-e2e-$(date +%s)"
KIND_NAME="cp-e2e-mgmt"
WORK="$(mktemp -d)"
MGMT_KUBECONFIG="$WORK/mgmt.kubeconfig"
TARGET_KUBECONFIG="$WORK/target.kubeconfig"
ADMIN_KUBECONFIG="$WORK/gke-admin.kubeconfig"

cleanup() {
  echo "==> Cleanup"
  kind delete cluster --name "$KIND_NAME" >/dev/null 2>&1 || true
  gcloud container clusters delete "$CLUSTER" --zone "$ZONE" --quiet >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "==> Creating GKE target cluster $CLUSTER ($ZONE) in the background"
gcloud container clusters create "$CLUSTER" --zone "$ZONE" \
  --num-nodes 1 --machine-type "$MACHINE" --disk-size 30 \
  --no-enable-autoupgrade --no-enable-autorepair --quiet &
GKE_PID=$!

echo "==> Creating kind management cluster $KIND_NAME"
kind delete cluster --name "$KIND_NAME" >/dev/null 2>&1 || true
kind create cluster --name "$KIND_NAME" --kubeconfig "$MGMT_KUBECONFIG"

echo "==> Waiting for GKE creation to finish"
wait "$GKE_PID"

echo "==> Minting self-contained SA-token kubeconfig for the target"
KUBECONFIG="$ADMIN_KUBECONFIG" gcloud container clusters get-credentials "$CLUSTER" --zone "$ZONE"
KUBECONFIG="$ADMIN_KUBECONFIG" kubectl create serviceaccount core-provider-remote -n kube-system
KUBECONFIG="$ADMIN_KUBECONFIG" kubectl create clusterrolebinding core-provider-remote \
  --clusterrole=cluster-admin --serviceaccount=kube-system:core-provider-remote
TOKEN="$(KUBECONFIG="$ADMIN_KUBECONFIG" kubectl create token core-provider-remote -n kube-system --duration=2h)"
ENDPOINT="$(gcloud container clusters describe "$CLUSTER" --zone "$ZONE" --format='value(endpoint)')"
CADATA="$(gcloud container clusters describe "$CLUSTER" --zone "$ZONE" --format='value(masterAuth.clusterCaCertificate)')"
cat > "$TARGET_KUBECONFIG" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: target
  cluster:
    server: https://${ENDPOINT}
    certificate-authority-data: ${CADATA}
contexts:
- name: target
  context: { cluster: target, user: core-provider-remote }
current-context: target
users:
- name: core-provider-remote
  user:
    token: ${TOKEN}
EOF

echo "==> Running e2e test"
MGMT_KUBECONFIG="$MGMT_KUBECONFIG" TARGET_KUBECONFIG="$TARGET_KUBECONFIG" \
  go test -tags e2e -run TestE2E_RemoteTargeting -v ./internal/tools/clusterkube/

echo "==> e2e validation passed"
