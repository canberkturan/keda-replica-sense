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
- Bounded, source-authoritative recovery after a Prometheus source outage.

## Install

The Helm chart does not install KEDA, Prometheus Operator, or PostgreSQL. Create
the PostgreSQL URL Secret, then install the published OCI chart:

```bash
kubectl -n replicasense-system create secret generic replicasense-database \
  --from-literal=url='postgres://USER:PASSWORD@HOST:5432/replicasense?sslmode=require'

helm upgrade --install replicasense oci://ghcr.io/canberkturan/charts/replicasense \
  --version 0.3.1 \
  --namespace replicasense-system --create-namespace \
  --set clusterID=YOUR_CLUSTER_ID
```

Apply [`migrations/schema.sql`](https://github.com/canberkturan/keda-replica-sense/blob/main/migrations/schema.sql)
once to a new empty PostgreSQL database before the application starts.

## Use with a KEDA ScaledObject

Add a named native Prometheus trigger and the ReplicaSense external trigger to
the same `ScaledObject`. `modelEngine: xgboost` remains the default; set
`modelEngine: gru` for the GRU model. See the [enterprise installation guide](https://github.com/canberkturan/keda-replica-sense/blob/main/docs/enterprise-installation.md)
for configuration, operations, and safety limits.
