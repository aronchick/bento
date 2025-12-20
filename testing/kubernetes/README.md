# Kubernetes Integration Testing

This directory contains scripts and configurations for testing the Bento Kubernetes inputs locally.

## Quick Start

```bash
# 1. Set up a local k3d cluster
./setup-k3d.sh

# 2. Deploy test workloads
./deploy-test-workloads.sh

# 3. Run Bento with a test config
../bin/bento -c configs/watch-pods.yaml

# 4. Run load tests (optional)
./load-test.sh

# 5. Cleanup
./cleanup.sh
```

## Prerequisites

- [k3d](https://k3d.io/) - Lightweight Kubernetes in Docker
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- Docker
- Go 1.21+ (for building Bento)

## Available Inputs

| Input | Description | Config Example |
|-------|-------------|----------------|
| `kubernetes_watch` | Watch any resource for changes | `configs/watch-pods.yaml` |
| `kubernetes_events` | Stream cluster events | `configs/events.yaml` |
| `kubernetes_logs` | Stream pod logs | `configs/logs.yaml` |
| `kubernetes_metrics` | Collect metrics from metrics-server | `configs/metrics.yaml` |

## Scripts

| Script | Description |
|--------|-------------|
| `setup-k3d.sh` | Create a local k3d cluster with metrics-server |
| `deploy-test-workloads.sh` | Deploy sample pods, deployments, and CRDs |
| `load-test.sh` | Generate load by creating/deleting pods rapidly |
| `cleanup.sh` | Delete the k3d cluster |

## Test Scenarios

### Basic Watch Test
```bash
# Watch all pods in default namespace
bento -c configs/watch-pods.yaml
```

### Events Monitoring
```bash
# Stream Warning events only
bento -c configs/events-warnings.yaml
```

### Log Aggregation
```bash
# Tail logs from all pods with app=test label
bento -c configs/logs-by-label.yaml
```

### Metrics Collection
```bash
# Collect pod CPU/memory metrics every 30s
bento -c configs/metrics.yaml
```

## Building Bento

```bash
# From repo root
make

# Or with kubernetes inputs only
go build -o ./testing/kubernetes/bin/bento ./cmd/bento
```
