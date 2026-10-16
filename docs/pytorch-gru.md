# PyTorch GRU (CPU)

ReplicaSense uses PyTorch only in a short-lived Kubernetes Trainer Job. The
always-on controller, scaler, sampler, and forecaster stay in Go. A GRU model
is exported as validated JSON tensors, never as Python pickle or TorchScript,
then evaluated by the Go forecaster.

## When to choose it

Use `modelEngine: gru` for a metric with a stable time-of-day or day-of-week
shape when the recent sequence also carries useful information. XGBoost stays
the default and is generally the better first choice for one metric plus
calendar features. GRU has a larger CPU and memory footprint and should be
tested against XGBoost using the same ScaledObject settings and source data.

## Default CPU image and private registries

The chart already routes GRU training Jobs to the release-matched CPU PyTorch
image. Set `trainer.images.gru` only when your organization mirrors images into
a private registry or wants to pin a digest:

```bash
helm upgrade --install replicasense oci://ghcr.io/canberkturan/charts/replicasense \
  --version <version> --namespace replicasense-system \
  --set clusterID=<cluster-id> \
  --set trainer.images.gru=registry.example.com/platform/replicasense-trainer:<version>-pytorch-gru
```

Only Jobs for `modelEngine: gru` use this image. XGBoost and baseline engines
continue using the standard configured trainer image. The image is CPU-only,
runs as an unprivileged user with a read-only root filesystem, and receives no
Kubernetes API token.

## Training and safety

The trainer creates two one-layer GRUs: a P50 head and the configured upper
quantile head. Each input has 120 one-minute observations and the same demand,
clock, weekday, day-of-month, month, and weekend features used by ReplicaSense.
It uses a contiguous train/calibration/test split. Normalization is fitted on
training observations only; held-out data never fits either the weights or the
conformal correction. The image's shared Go preflight repairs missing PostgreSQL
slots from the original Prometheus query before Python loads samples. If source
retention or availability leaves gaps unresolved, training fails explicitly
instead of joining the outage or selecting an older fragment.

The Job always publishes a candidate, never an active model. Python holdout
scores are diagnostics, not promotion authority. The shared Go evaluator loads
the exported JSON tensors and compares their actual serving predictions with
the active artifact and baseline on identical held-out observations. See
[model promotion](model-promotion.md) for quantile-aware gates, shadow mode,
and rollback. A rejected candidate does not replace an existing active model.

The Job prints a structured completion record with
`promotion_reason=awaiting_shared_artifact_evaluation`. Training success does
not mean promotion. The shared evaluation evidence and decision are stored in
`models.validation_metrics.promotion_v2`; trainer diagnostics stay with the
training run and model.

## Resource planning

The chart defaults Trainer Jobs to 500m CPU / 512Mi request and 2 CPU / 2Gi
limit. Start there, measure one 30-day training run in a representative
cluster, and set an active deadline that respects your batch workload budget.
Do not lower the limit below 1Gi for the default 120-step, 32-hidden-unit GRU.
