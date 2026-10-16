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
- Automatically requests KEDA reconciliation when restart-time discovery leaves
  a managed HPA missing its predictive metric.
- Bridges tightly bounded short gaps with clearly marked, inference-only
  estimates and conservative scaling safeguards; observed/training data remain
  unchanged, and stale or unreliable inputs stay withheld.
- Stores/evaluates snapshots; compares exact model artifacts on identical
  held-out, fully observed horizons, including coverage, underprediction
  severity, quantile loss, and peak-period behavior.
- Schedules, validates, stores, and promotes per-workload XGBoost models
  through Kubernetes Trainer Jobs and PostgreSQL model artifacts. A shared Go
  evaluator controls promotion, supports shadow mode and evidence-gated
  rollback, and keeps rejected candidates auditable. See
  [model promotion](docs/model-promotion.md) for the policy and operations.

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

For a published-chart install with an externally managed PostgreSQL database,
use [charts/replicasense](charts/replicasense/README.md). The chart requires a
database URL Secret and intentionally does not install KEDA or PostgreSQL.

The complete database bootstrap, Helm installation, workload setup, and
operations flow is in the [installation guide](docs/enterprise-installation.md).
See the [metrics reference](docs/metrics.md) for Prometheus metric meanings,
labels, dashboard queries, and alerting guidance. The [architecture guide](docs/architecture.md)
explains topology, training, model selection, scaling semantics, and failure
behavior.

`Dockerfile.xgboost` is the reproducible image foundation for the default
forecaster and Trainer Job images. The native engine is enabled only in images
built with the explicit `xgboost` build tag; portable images remain available
for explicitly configured baseline and rolling-quantile engines.

The CI workflow verifies both the portable build and `go test -tags xgboost
./...` inside this reproducible native XGBoost build environment.

For `modelEngine: gru`, ReplicaSense can route only the short-lived Trainer
Job to the CPU-only PyTorch image. The artifact contains numeric tensors, not
pickle or TorchScript; the Go forecaster validates and evaluates it locally.
The chart routes GRU Jobs to the release-matched PyTorch image by default.
Override `trainer.images.gru` only to use a mirrored or digest-pinned image.
GRU candidates must still pass the same temporal validation and promotion gate
as every other model.

See [the PyTorch GRU guide](docs/pytorch-gru.md) for the CPU image, resource
budget, temporal validation, and promotion behaviour.

## Safety boundary

Native KEDA Prometheus triggers remain the reactive safety path. Predictive
snapshots fail closed when their generation time or underlying source sample is
stale or unsafe. After a source query recovers, ReplicaSense repairs a bounded
recent gap from Prometheus range-query data. A short, stable gap may instead be
bridged in a disposable inference buffer, with explicit estimated-input markers
and tighter scaling limits; it never becomes observed or training data. Long or
unreliable gaps require valid real post-outage context.

ReplicaSense deploys two scaler replicas, a PDB, and preferred cross-node
anti-affinity. If every scaler endpoint is unavailable, predictive scaling is
unavailable and KEDA's metric discovery can fail or leave a reactive-only HPA.
After endpoint restoration, the elected ReplicaSense controller requests KEDA
reconciliation for missing predictive metrics. See the
[architecture guide](docs/architecture.md) for the recovery boundaries.
