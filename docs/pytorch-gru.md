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
It uses a contiguous train/calibration/test split; the test segment is not used
for fitting or conformal calibration. Promotion requires:

- coverage at least 93%;
- underprediction at most 7%; and
- no worse P95 pinball loss than the active champion or deterministic baseline.

Failed criteria leave the artifact as a candidate. KEDA continues to use the
reactive trigger, so an unpromoted GRU cannot take control of replica count.

The Job prints one structured completion record containing its promotion result
and validation rates. Full metrics are stored with the `training_runs` and
`models` records and are available to ReplicaSense monitoring queries.

## Resource planning

The chart defaults Trainer Jobs to 500m CPU / 512Mi request and 2 CPU / 2Gi
limit. Start there, measure one 30-day training run in a representative
cluster, and set an active deadline that respects your batch workload budget.
Do not lower the limit below 1Gi for the default 120-step, 32-hidden-unit GRU.
