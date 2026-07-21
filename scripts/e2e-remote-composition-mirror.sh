#!/usr/bin/env bash
#
# End-to-end validation of the remote composition mirror (PR: remote-composition-mirror).
#
# Provisions a local kind cluster (management/hub) and a disposable single-node GKE
# cluster (remote spoke), mints a self-contained ServiceAccount-token kubeconfig for the
# spoke (the recipe in docs/how-to/remote-target-credentials.md), then runs the
# build-tagged reflector e2e test in internal/controllers/compositionmirror. Both clusters
# are torn down on exit (success or failure).
#
# Neither cluster is the caller's active kubeconfig context: the hub is an isolated kind
# kubeconfig FILE and the spoke is reached only through the minted kubeconfig FILE. The
# caller's current-context is never read or modified.
#
# What it proves against real clusters:
#   * a hub Composition is mirrored onto the spoke (spec carried, stamped managed);
#   * spoke Composition status is read back onto the hub Composition;
#   * garbage collection removes only reflector-managed spoke mirrors with no hub
#     counterpart, and never an unmanaged spoke instance.
#
# Requirements: gcloud (authenticated, GKE API enabled on the project), kubectl, kind, go.
# Usage: scripts/e2e-remote-composition-mirror.sh
#   GKE_PROJECT (default: gcloud's configured project), GKE_ZONE (default us-central1-a),
#   GKE_MACHINE (default e2-medium) may be overridden.
set -euo pipefail

PROJECT="${GKE_PROJECT:-$(gcloud config get-value project 2>/dev/null)}"
ZONE="${GKE_ZONE:-us-central1-a}"
MACHINE="${GKE_MACHINE:-e2-medium}"
CLUSTER="cpmirror-e2e-$(date +%s)"
KIND_NAME="cpmirror-e2e-mgmt"
WORK="$(mktemp -d)"
MGMT_KUBECONFIG="$WORK/mgmt.kubeconfig"
TARGET_KUBECONFIG="$WORK/target.kubeconfig"
ADMIN_KUBECONFIG="$WORK/gke-admin.kubeconfig"

echo "==> Hub: kind ($KIND_NAME) | Spoke: disposable GKE $CLUSTER ($ZONE) in project $PROJECT"

cleanup() {
  echo "==> Cleanup"
  kind delete cluster --name "$KIND_NAME" --kubeconfig "$MGMT_KUBECONFIG" >/dev/null 2>&1 || true
  # KUBECONFIG override: `gcloud container clusters delete` otherwise prunes the cluster's context
  # from the caller's active ~/.kube/config. Keep it pinned to the throwaway admin kubeconfig.
  KUBECONFIG="$ADMIN_KUBECONFIG" gcloud container clusters delete "$CLUSTER" --zone "$ZONE" --project "$PROJECT" --quiet >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "==> Creating disposable GKE spoke $CLUSTER in the background"
# KUBECONFIG override is REQUIRED: `gcloud container clusters create` implicitly runs
# get-credentials on success, which writes the new cluster into the caller's active
# ~/.kube/config AND switches current-context to it. Pinning KUBECONFIG to the throwaway admin
# file keeps the caller's real context (e.g. a production cluster) completely untouched.
KUBECONFIG="$ADMIN_KUBECONFIG" gcloud container clusters create "$CLUSTER" --zone "$ZONE" --project "$PROJECT" \
  --num-nodes 1 --machine-type "$MACHINE" --disk-size 30 \
  --no-enable-autoupgrade --no-enable-autorepair --quiet &
GKE_PID=$!

echo "==> Creating kind management/hub cluster $KIND_NAME"
kind delete cluster --name "$KIND_NAME" --kubeconfig "$MGMT_KUBECONFIG" >/dev/null 2>&1 || true
kind create cluster --name "$KIND_NAME" --kubeconfig "$MGMT_KUBECONFIG"

echo "==> Waiting for GKE creation to finish"
wait "$GKE_PID"

echo "==> Minting self-contained SA-token kubeconfig for the spoke"
KUBECONFIG="$ADMIN_KUBECONFIG" gcloud container clusters get-credentials "$CLUSTER" --zone "$ZONE" --project "$PROJECT"
KUBECONFIG="$ADMIN_KUBECONFIG" kubectl create serviceaccount core-provider-remote -n kube-system
KUBECONFIG="$ADMIN_KUBECONFIG" kubectl create clusterrolebinding core-provider-remote \
  --clusterrole=cluster-admin --serviceaccount=kube-system:core-provider-remote
TOKEN="$(KUBECONFIG="$ADMIN_KUBECONFIG" kubectl create token core-provider-remote -n kube-system --duration=2h)"
ENDPOINT="$(gcloud container clusters describe "$CLUSTER" --zone "$ZONE" --project "$PROJECT" --format='value(endpoint)')"
CADATA="$(gcloud container clusters describe "$CLUSTER" --zone "$ZONE" --project "$PROJECT" --format='value(masterAuth.clusterCaCertificate)')"
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

echo "==> Running reflector e2e test"
MGMT_KUBECONFIG="$MGMT_KUBECONFIG" TARGET_KUBECONFIG="$TARGET_KUBECONFIG" \
  go test -tags e2e -run TestE2E_RemoteCompositionMirror -v ./internal/controllers/compositionmirror/

echo "==> e2e validation passed"
