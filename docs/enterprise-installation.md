# ReplicaSense Enterprise Installation

ReplicaSense does not replace KEDA's native reactive triggers. The Prometheus
trigger remains the availability path; the predictive metric recommends
additional capacity only when forecast data is fresh and has passed safety
checks.

## Prerequisites

- Kubernetes 1.28 or later and KEDA 2.x.
- Prometheus that collects application metrics and is reachable by both KEDA
  and the ReplicaSense sampler.
- A PostgreSQL database dedicated to ReplicaSense and protected with TLS.
- A registry token with `read:packages` permission if the GHCR packages are
  private.

## 1. Publish a release

GitHub Actions publishes to GHCR only when a Git tag matches the chart version.
If the `Chart.yaml` version is `0.2.2`, the tag must be `v0.2.2`.

```bash
git tag -a v0.2.2 -m "ReplicaSense 0.2.2"
git push origin v0.2.2
```

The workflow publishes runtime images under
`ghcr.io/canberkturan/replicasense-*` and the chart under
`oci://ghcr.io/canberkturan/charts/replicasense`.

## 2. Create the database schema

Apply the bootstrap schema once to a new, empty database with the database-owner
role. This file is not an idempotent upgrade tool and must not be run again
against an existing database.

```bash
psql "$REPLICASENSE_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/schema.sql
```

Use a separate, least-privileged PostgreSQL account for runtime components. Its
connection URL must include `sslmode=require`, or your organization's
equivalent TLS policy.

## 3. Create Kubernetes secrets

If the GHCR packages are private, create an image-pull Secret and a separate
Secret for the database.

```bash
kubectl create namespace replicasense-system
kubectl -n replicasense-system create secret generic replicasense-database \
  --from-literal=url='postgres://USER:PASSWORD@HOST:5432/replicasense?sslmode=require'

kubectl -n replicasense-system create secret docker-registry ghcr-pull \
  --docker-server=ghcr.io \
  --docker-username=GITHUB_USER \
  --docker-password=GHCR_READ_TOKEN
```

Add `ghcr-pull` as an image pull secret through your organization Helm values
overlay if the Kubernetes ServiceAccount policy requires it.

## 4. Install from GHCR

Use an immutable chart version and a stable cluster identifier.

```bash
helm registry login ghcr.io
helm upgrade --install replicasense \
  oci://ghcr.io/canberkturan/charts/replicasense \
  --version 0.2.2 \
  --namespace replicasense-system \
  --set clusterID=production-cluster-1 \
  --set imagePullSecrets[0].name=ghcr-pull
```

If Prometheus Operator is not installed, add
`--set serviceMonitor.enabled=false`.

## 5. Connect a workload

A workload keeps its native KEDA Prometheus trigger and adds a named
ReplicaSense external trigger that references it. The application team owns the
Prometheus query and reactive threshold; ReplicaSense stores the derived
forecast contract and samples only that metric.

```yaml
triggers:
  - type: prometheus
    name: reactive-rps
    metadata:
      serverAddress: https://prometheus.monitoring.svc:9090
      query: sum(rate(http_requests_total{app="checkout"}[2m]))
      threshold: "20"
  - type: external
    name: predictive-rps
    metadata:
      scalerAddress: replicasense-replicasense-scaler.replicasense-system.svc:6000
      sourceTrigger: reactive-rps
      forecastHorizon: 8m
      samplingInterval: 1m
      trainingWindow: 30d
      # Q0.50 is always trained as the median. This is the upper operational
      # quantile used for predictive capacity, and must be strictly (0, 1).
      quantile: "0.95"
      businessTimezone: Europe/Istanbul
      modelEngine: xgboost
      # Surge extrapolation reaches only until new pods are useful.
      startupLatency: 90s
      safetyBuffer: 30s
```

The controller accepts only HTTP(S) Prometheus URLs without embedded
credentials. Use Kubernetes network policy and namespace RBAC to restrict who
can create or alter ScaledObjects.

## Operations and security

- Keep KEDA's native Prometheus trigger in every predictive ScaledObject.
- Use immutable image digests in a production values overlay after the release
  is approved.
- The trainer Job reads the database URL from a Kubernetes Secret and does not
  mount a Kubernetes API token.
- The training scheduler creates Trainer Jobs, and each successful validated
  model is stored as an immutable candidate. It is activated only when its
  walk-forward coverage and underprediction meet safety limits and it is no
  worse than the active champion (or, with no champion, the deterministic
  seasonal baseline). Rejected candidates and their promotion reason remain
  in the model validation metadata; users do not create or promote Jobs.
- The chart runs components as non-root with a read-only root filesystem,
  dropped capabilities, RuntimeDefault seccomp, scaler anti-affinity, and a
  PodDisruptionBudget.
- Native XGBoost is the default. The chart uses matching `VERSION-xgboost`
  forecaster and trainer images; use another model engine only as an explicit
  workload configuration.
- A predictive trigger without `modelEngine` now defaults to `xgboost`.
  Existing workloads that must retain the prior behavior must explicitly set
  `modelEngine: seasonal-baseline`. After a workload changes to XGBoost, its
  predictive metric fails closed until the first validated model is active;
  its native KEDA reactive trigger continues to operate.
- Monitor sampler query errors, snapshot age, predictive metric values, KEDA
  HPA events, database capacity, and scaler endpoint availability with your
  existing monitoring system.
- `REPLICASENSE_SPECULATIVE_HEADROOM_FRACTION` remains a fraction strictly in
  `(0,1]`. `REPLICASENSE_CLUSTER_SPECULATIVE_REPLICA_BUDGET` is different: it
  is a positive absolute replica count, so values such as `100` are valid.

## Forecast and model semantics

- The `quantile` metadata value is the upper operational quantile. It defaults
  to `0.95`, must be strictly between zero and one, and is used consistently
  for XGBoost training and walk-forward pinball loss. The persisted field name
  `ForecastP95` / `forecast_p95` is retained for compatibility even when the
  configured value is not `0.95`.
- Native XGBoost trains P50 using `reg:quantileerror` with alpha `0.50`, and
  trains the upper bound with the configured alpha. It does not treat a
  squared-error estimate as a median.
- The native feature schema is `demand-calendar-lag-v3`. It adds causal
  acceleration and slope-ratio inputs; V2 model artifacts are intentionally
  rejected and the workload fails closed to its existing reactive KEDA path
  until a V3 candidate is promoted.
- Deterministic surge detection is independent of ML output. Its lead time is
  `startupLatency + safetyBuffer`; if both are omitted, ReplicaSense uses the
  documented conservative two-minute fallback. It never extrapolates over the
  full ML forecast horizon.
- CI verifies the portable Go build and runs `go test -tags xgboost ./...`
  inside the reproducible `Dockerfile.xgboost` native-library environment.
