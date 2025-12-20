#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
KUBECONFIG="${KUBECONFIG:-$SCRIPT_DIR/kubeconfig.yaml}"

export KUBECONFIG

echo "==> Deploying test workloads..."

# Create test namespace
kubectl create namespace bento-test --dry-run=client -o yaml | kubectl apply -f -

# Deploy a simple nginx deployment
cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx-test
  namespace: bento-test
  labels:
    app: nginx
    tier: frontend
spec:
  replicas: 3
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
        tier: frontend
    spec:
      containers:
      - name: nginx
        image: nginx:alpine
        ports:
        - containerPort: 80
        resources:
          requests:
            memory: "64Mi"
            cpu: "50m"
          limits:
            memory: "128Mi"
            cpu: "100m"
EOF

# Deploy a logging workload (generates logs)
cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: log-generator
  namespace: bento-test
  labels:
    app: log-generator
spec:
  replicas: 2
  selector:
    matchLabels:
      app: log-generator
  template:
    metadata:
      labels:
        app: log-generator
    spec:
      containers:
      - name: logger
        image: busybox
        command: ["/bin/sh", "-c"]
        args:
          - |
            i=0
            while true; do
              echo "{\"level\":\"info\",\"msg\":\"Log entry \$i\",\"timestamp\":\"\$(date -Iseconds)\"}"
              i=\$((i+1))
              sleep 2
            done
        resources:
          requests:
            memory: "16Mi"
            cpu: "10m"
          limits:
            memory: "32Mi"
            cpu: "50m"
EOF

# Deploy a CronJob (generates events periodically)
cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: CronJob
metadata:
  name: event-generator
  namespace: bento-test
spec:
  schedule: "*/1 * * * *"
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 1
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: job
            image: busybox
            command: ["echo", "Periodic job completed"]
          restartPolicy: Never
EOF

# Create a failing pod to generate Warning events
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: failing-pod
  namespace: bento-test
  labels:
    app: failing
spec:
  containers:
  - name: fail
    image: busybox
    command: ["sh", "-c", "exit 1"]
  restartPolicy: Always
EOF

# Wait for deployments
echo "==> Waiting for deployments to be ready..."
kubectl wait --for=condition=Available deployment/nginx-test -n bento-test --timeout=120s
kubectl wait --for=condition=Available deployment/log-generator -n bento-test --timeout=120s

echo ""
echo "==> Test workloads deployed!"
echo ""
echo "Workloads:"
kubectl get pods -n bento-test
echo ""
echo "To view events:"
echo "  kubectl get events -n bento-test --watch"
