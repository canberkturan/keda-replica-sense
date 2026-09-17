[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/keda-replica-sense)](https://artifacthub.io/packages/search?repo=keda-replica-sense)
# ReplicaSense

ReplicaSense is a self-hosted, KEDA-native predictive autoscaling extension for
Kubernetes. It supplements native KEDA triggers; it never replaces the
reactive autoscaling path and introduces no custom resource definition.

Licensed under [Apache-2.0](LICENSE); attribution is in [NOTICE](NOTICE). See
[CONTRIBUTING.md](CONTRIBUTING.md) for the local verification and contribution
expectations.

## Current capabilities

- Watches KEDA `ScaledObject` resources and derives predictive contracts from
  named native Prometheus triggers.
- Samples and backfills Prometheus data into PostgreSQL with idempotent writes.
- Produces XGBoost horizon-maximum quantile forecasts (true Q0.50 and a
  configured upper operational quantile), deterministic surge detection,
  rate limits, headroom, and cluster-wide speculative budget guards.
- Serves fresh, safety-approved predictive demand to KEDA through an
  in-memory external-scaler cache.
- Stores/evaluates snapshots; evaluates coverage, underprediction, MAE, and
  P95 pinball loss with walk-forward validation.
- Schedules, validates, stores, and promotes per-workload XGBoost models
  through Kubernetes Trainer Jobs and PostgreSQL model artifacts. Promotion
  requires safety thresholds plus a comparison with an active champion or the
  deterministic baseline; rejected candidates remain auditable.

## Install

```bash
go test ./...
go vet ./...
helm upgrade --install replicasense ./charts/replicasense \
  --namespace replicasense-system --create-namespace \
  --set clusterID=YOUR_CLUSTER_ID
```

Create the required database URL Secret before installation; KEDA, Prometheus
Operator, and PostgreSQL are external production dependencies.

For a production-style install with an externally managed PostgreSQL database,
use [charts/replicasense](charts/replicasense/README.md). The chart requires a
database URL Secret and intentionally does not install KEDA or PostgreSQL.

The complete release, database bootstrap, Helm installation, workload, and
operations flow is in [docs/enterprise-installation.md](docs/enterprise-installation.md).

`Dockerfile.xgboost` is the reproducible image foundation for the default
forecaster and Trainer Job images. The native engine is enabled only in images
built with the explicit `xgboost` build tag; portable images remain available
for explicitly configured baseline and rolling-quantile engines.

The CI workflow verifies both the portable build and `go test -tags xgboost
./...` inside this reproducible native XGBoost build environment.

## Safety boundary

Native KEDA Prometheus triggers remain the reactive safety path. Predictive
snapshots fail closed when stale or unsafe. ReplicaSense deploys two scaler
replicas, a PDB, and preferred cross-node anti-affinity; a single scaler-pod
loss continues serving metrics. **Accepted catastrophic-outage limitation:**
if every external-scaler Service endpoint is lost, KEDA fails while discovering
the external metric, before it can evaluate fallback or scaling modifiers. That
event requires endpoint restoration; reactive scale-up is not available through
the combined ScaledObject during the outage.
