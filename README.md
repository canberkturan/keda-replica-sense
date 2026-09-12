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
- Produces seasonal-baseline horizon-maximum forecasts, deterministic surge
  detection, rate limits, headroom, and cluster-wide speculative budget guards.
- Serves fresh, safety-approved predictive demand to KEDA through an
  in-memory external-scaler cache.
- Stores/evaluates snapshots; evaluates coverage, underprediction, MAE, and
  P95 pinball loss with walk-forward validation.
- Schedules per-workload Kubernetes Trainer Jobs with PostgreSQL claims and
  PostgreSQL model artifact lifecycle.

## Install

```bash
go test ./...
go vet ./...
helm upgrade --install replicasense ./charts/replicasense \
  --namespace replicasense-system --create-namespace \
  --set clusterID=YOUR_CLUSTER_ID
```

Create the required database URL Secret before installation; KEDA, Prometheus
Operator, and PostgreSQL are external production dependencies. The complete
product plan is in [docs/project-plan.md](docs/project-plan.md); current
evidence and known limitations are in
[docs/acceptance-criteria.md](docs/acceptance-criteria.md).

For a production-style install with an externally managed PostgreSQL database,
use [charts/replicasense](charts/replicasense/README.md). The chart requires a
database URL Secret and intentionally does not install KEDA or PostgreSQL.

The complete release, database bootstrap, Helm installation, workload, and
operations flow is in [docs/enterprise-installation.md](docs/enterprise-installation.md).

`Dockerfile.xgboost` is the reproducible optional image foundation for native
XGBoost components. The native engine is enabled only in images built with the
explicit `xgboost` build tag; portable images continue to support baseline and
rolling-quantile engines.

## Safety boundary

Native KEDA Prometheus triggers remain the reactive safety path. Predictive
snapshots fail closed when stale or unsafe. ReplicaSense deploys two scaler
replicas, a PDB, and preferred cross-node anti-affinity; a single scaler-pod
loss continues serving metrics. **Accepted catastrophic-outage limitation:**
if every external-scaler Service endpoint is lost, KEDA fails while discovering
the external metric, before it can evaluate fallback or scaling modifiers. That
event requires endpoint restoration; reactive scale-up is not available through
the combined ScaledObject during the outage.
