#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${K3D_CLUSTER_NAME:-bento-test}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "==> Deleting k3d cluster: $CLUSTER_NAME"
k3d cluster delete "$CLUSTER_NAME" 2>/dev/null || true

# Remove kubeconfig
rm -f "$SCRIPT_DIR/kubeconfig.yaml"

echo "==> Cleanup complete!"
