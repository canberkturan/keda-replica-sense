# ReplicaSense KEDA External Scaler

ReplicaSense is a self-hosted KEDA external scaler that augments a named native
Prometheus trigger with a safety-approved predictive replica recommendation.
It retains the native KEDA trigger as the reactive scaling path.

## What it provides

- XGBoost or GRU horizon-maximum p50 and upper-quantile forecasts from
  Prometheus history.
- A deterministic surge detector for sharp, unexpected increases.
- Guardrails for maximum replicas, incremental scale-up, cluster capacity, and
  stale or unsafe predictive data.
- Per-workload model training, walk-forward validation, promotion, and
  evaluation stored in PostgreSQL.

## Install

The Helm chart does not install KEDA, Prometheus Operator, or PostgreSQL. Create
the PostgreSQL URL Secret, then install the published OCI chart:

```bash
kubectl -n replicasense-system create secret generic replicasense-database \
  --from-literal=url='postgres://USER:PASSWORD@HOST:5432/replicasense?sslmode=require'

helm upgrade --install replicasense oci://ghcr.io/canberkturan/charts/replicasense \
  --version 0.3.0 \
  --namespace replicasense-system --create-namespace \
  --set clusterID=YOUR_CLUSTER_ID
```

Apply [`migrations/schema.sql`](https://github.com/canberkturan/keda-replica-sense/blob/main/migrations/schema.sql)
once to a new empty PostgreSQL database before the application starts.

## Use with a KEDA ScaledObject

Add a named native Prometheus trigger and the ReplicaSense external trigger to
the same `ScaledObject`:

```yaml
triggers:
  - type: prometheus
    name: reactive-rps
    metadata:
      serverAddress: http://prometheus.monitoring.svc:9090
      query: sum(rate(http_requests_total[2m]))
      threshold: "500"
  - type: external
    name: predictive-rps
    metadata:
      scalerAddress: replicasense-replicasense-scaler.replicasense-system.svc:6000
      sourceTrigger: reactive-rps
      forecastHorizon: 10m
      samplingInterval: 1m
      trainingWindow: 30d
      quantile: "0.95"
      modelEngine: gru
      businessTimezone: UTC
```

`modelEngine: xgboost` remains the default. GRU uses the last 60 causal demand
samples together with business-time calendar fields. Both models remain subject
to the same no-leakage validation and safety promotion policy.

See the [enterprise installation guide](https://github.com/canberkturan/keda-replica-sense/blob/main/docs/enterprise-installation.md)
for requirements, model choice, operations, and safety limitations.
