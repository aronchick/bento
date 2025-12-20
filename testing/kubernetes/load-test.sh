#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
KUBECONFIG="${KUBECONFIG:-$SCRIPT_DIR/kubeconfig.yaml}"
DURATION="${1:-60}"
INTERVAL="${2:-1}"

export KUBECONFIG

echo "==> Running load test for ${DURATION}s (creating pods every ${INTERVAL}s)"
echo "    Press Ctrl+C to stop"
echo ""

cleanup() {
    echo ""
    echo "==> Cleaning up load test pods..."
    kubectl delete pods -n bento-test -l load-test=true --ignore-not-found
}
trap cleanup EXIT

start_time=$(date +%s)
pod_count=0

while true; do
    current_time=$(date +%s)
    elapsed=$((current_time - start_time))

    if [ "$elapsed" -ge "$DURATION" ]; then
        break
    fi

    # Create a pod
    pod_name="load-test-${pod_count}"
    cat <<EOF | kubectl apply -f - > /dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_name}
  namespace: bento-test
  labels:
    load-test: "true"
    iteration: "${pod_count}"
spec:
  containers:
  - name: test
    image: busybox
    command: ["sleep", "30"]
    resources:
      requests:
        memory: "8Mi"
        cpu: "5m"
  restartPolicy: Never
EOF

    echo "Created pod: ${pod_name} (${elapsed}s / ${DURATION}s)"
    pod_count=$((pod_count + 1))

    # Also delete old pods to generate DELETE events
    if [ "$pod_count" -gt 10 ]; then
        old_pod="load-test-$((pod_count - 10))"
        kubectl delete pod "$old_pod" -n bento-test --ignore-not-found > /dev/null 2>&1 &
    fi

    sleep "$INTERVAL"
done

echo ""
echo "==> Load test complete!"
echo "    Created $pod_count pods in ${DURATION}s"
echo "    Rate: $(echo "scale=2; $pod_count / $DURATION" | bc) pods/sec"
