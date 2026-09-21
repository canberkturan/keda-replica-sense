# Changelog

This project follows semantic versioning. The notes below describe the
production-facing changes introduced after `v0.2.6` and delivered through the
`v0.3.6` release line.

## 0.3.6 — 2026-09-21

- Made the release-matched CPU PyTorch Trainer the default for `modelEngine:
  gru`. Private registries can override `trainer.images.gru` without affecting
  XGBoost or baseline Jobs.

## 0.3.5 — 2026-09-21

### Predictive models and training

- Added the `gru` model engine alongside `xgboost`, `seasonal-baseline`,
  `rolling-quantile`, and Holt-Winters. GRU uses causal demand history and
  business-time features: clock time, day of week, day of month, month, and
  weekend status.
- Added a CPU-only PyTorch GRU Trainer image. It exports validated numeric JSON
  tensors; the always-on Go forecaster evaluates the artifact without loading
  pickle or TorchScript.
- Added `immediateTraining` for an idempotent training Job when a predictive
  trigger is created or materially changed.
- Added `clearOldModels`. When paired with immediate training it removes the
  model lineage for the durable workload identity before the new run begins.
  Both operations are protected by the policy revision, so a normal reconcile
  cannot enqueue or delete repeatedly.
- Added workload-level seasonal-period and maximum-training-gap configuration.
  This makes the training contract explicit and rejects discontinuous time
  series instead of quietly fitting synthetic values across a source outage.

### Reliability and scaling safety

- Added bounded Prometheus history recovery for the sampler. A temporary source
  outage can be repaired from a configured recovery window without backfilling
  unbounded history.
- The forecaster now uses the newest contiguous sample segment after a source
  gap and requires freshness before issuing a prediction. The external scaler
  withholds stale snapshots rather than returning a misleading recommendation.
- Added cluster capacity and speculative-replica budget checks before predictive
  scale-up. CPU, memory, init-container peaks, pod overhead, and unschedulable
  pod state are included in the decision.
- Tightened surge detection. A surge now requires sustained evidence: the slope
  must exceed the historical P95 by the configured multiplier and demand must
  exceed the five-sample baseline by its configured multiplier for consecutive
  observations. The projection is capped at a configurable multiplier of
  current demand.
- Default conservative surge policy: `3x` historical slope, `1.5x` baseline,
  two confirmations, and a `1.5x` projection cap. Helm exposes all four
  `REPLICASENSE_SURGE_*` controls.

### Observability and operations

- Added health, cycle, snapshot-age, source-age, capacity, and external-scaler
  request metrics. These distinguish a live process from one that is unable to
  make fresh scaling decisions.
- Added `replicasense_scaledobject_trigger_info`, an info metric emitted for
  every managed ScaledObject trigger. It exposes predictive configuration,
  model engine, timing, training flags, and reactive linkage/threshold without
  exposing raw PromQL or endpoint values.
- Added chart-managed ServiceMonitors for controller, sampler, forecaster, and
  external scaler. Their labels are configurable for Prometheus Operator
  selectors such as kube-prometheus-stack's `release: monitoring`.
- Added leader-election and hardened default Kubernetes permissions/security
  contexts for control-plane components, plus resource guidance and probes in
  the chart.

### Packaging, CI, and documentation

- Added release builds for the CPU-only PyTorch GRU Trainer and expanded GHCR
  publishing for all runtime images and the OCI Helm chart.
- Added native-XGBoost image verification to CI.
- Added Artifact Hub metadata for releases through `0.3.6`.
- Reworked the public, enterprise-installation, architecture, metrics,
  PyTorch-GRU, chart, and contributing documentation. The guides now cover
  topology, trigger composition, training, model selection, capacity planning,
  outage behavior, metrics, and safe rollout practices.

### Upgrade notes

- Apply the current [`migrations/schema.sql`](migrations/schema.sql) before
  deploying the release. It contains the cumulative schema required by the
  training-policy and forecast-evaluation features.
- Retain a named native Prometheus trigger on every managed ScaledObject. It is
  the reactive availability path while models warm up, a forecast is withheld,
  or the external-scaler endpoint is unavailable.
- Start with the default surge policy. Tighten or relax it only from observed
  demand and replica-decision data, not from a single load test.
