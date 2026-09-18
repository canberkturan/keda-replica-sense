# ReplicaSense Helm chart

This chart installs ReplicaSense application components. It does not install
KEDA, Prometheus Operator, or PostgreSQL; those are intentional external
dependencies. For a new empty database, apply `migrations/schema.sql` once
with the database-owner role before installing the application. Existing
databases require a reviewed, release-specific upgrade procedure; do not rerun
the bootstrap schema against them.

Create a namespace and a Secret with the complete PostgreSQL URL:

```bash
kubectl create namespace replicasense-system
kubectl -n replicasense-system create secret generic replicasense-database \
  --from-literal=url='postgres://USER:PASSWORD@HOST:5432/replicasense?sslmode=require'
helm upgrade --install replicasense ./charts/replicasense \
  --namespace replicasense-system \
  --set clusterID=YOUR_CLUSTER_ID
```

The chart creates ServiceMonitors by default. If Prometheus Operator is not
installed, set `serviceMonitor.enabled=false`.

Published releases are available as an OCI chart. For example, after the
`v0.2.6` Git tag completes the release workflow:

```bash
helm upgrade --install replicasense oci://ghcr.io/canberkturan/charts/replicasense \
  --version 0.2.6 \
  --namespace replicasense-system \
  --set clusterID=YOUR_CLUSTER_ID
```

The chart defaults to matching immutable release tags, with native XGBoost
images for the forecaster and internally-created Trainer Jobs. A successful
training run stores an immutable candidate; it is activated only after the
promotion policy accepts its no-leakage validation results. Users do not create
or promote Jobs. For a private GHCR package, authenticate first with `helm
registry login ghcr.io` using a token that has `read:packages` permission.
