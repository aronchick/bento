#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${K3D_CLUSTER_NAME:-bento-test}"
K3S_VERSION="${K3S_VERSION:-v1.28.5-k3s1}"

echo "==> Creating k3d cluster: $CLUSTER_NAME"

# Check if cluster already exists
if k3d cluster list | grep -q "^$CLUSTER_NAME "; then
    echo "Cluster $CLUSTER_NAME already exists. Delete with: k3d cluster delete $CLUSTER_NAME"
    exit 0
fi

# Create cluster with metrics-server enabled
k3d cluster create "$CLUSTER_NAME" \
    --image "rancher/k3s:$K3S_VERSION" \
    --servers 1 \
    --agents 2 \
    --wait

# Wait for cluster to be ready
echo "==> Waiting for cluster to be ready..."
kubectl wait --for=condition=Ready nodes --all --timeout=120s

# Install metrics-server (for kubernetes_metrics input)
echo "==> Installing metrics-server..."
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml

# Patch metrics-server for k3d (disable TLS verification for kubelet)
kubectl patch deployment metrics-server -n kube-system \
    --type='json' \
    -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}]'

# Wait for metrics-server
echo "==> Waiting for metrics-server..."
kubectl wait --for=condition=Available deployment/metrics-server -n kube-system --timeout=120s

# Export kubeconfig
KUBECONFIG_PATH="$(dirname "$0")/kubeconfig.yaml"
k3d kubeconfig get "$CLUSTER_NAME" > "$KUBECONFIG_PATH"
echo "==> Kubeconfig exported to: $KUBECONFIG_PATH"

echo ""
echo "==> Cluster ready!"
echo ""
echo "To use:"
echo "  export KUBECONFIG=$KUBECONFIG_PATH"
echo ""
echo "Next steps:"
echo "  ./deploy-test-workloads.sh"
