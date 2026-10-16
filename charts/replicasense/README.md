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

The chart creates ServiceMonitors by default, labeled for the conventional
`kube-prometheus-stack` release name, `monitoring`. If your Prometheus release
uses another selector label, set it at install time; for example:

```bash
--set-string serviceMonitor.labels.release=observability
```

If Prometheus Operator is not installed, set `serviceMonitor.enabled=false`.

Every long-running component has resource requests and limits by default. The
controller and external scaler each have two replicas and a PDB. For a
production installation, put reviewed resource settings and image digests in a
values file. An image `digest`, when set for a component, takes precedence over
its `tag`.
`components.<name>.imagePullPolicy` can override the global `imagePullPolicy`
for one component, for example when testing a locally loaded controller image.

The elected controller automatically checks for missing predictive metrics on
KEDA-owned HPAs after startup failures. Recovery probes KEDA's metrics API and
patches only HPA metadata to request reconciliation. The controller role grants
HPA `get`/`patch` and external metric `get`/`list`, but no replica/scale-subresource
permissions. It does not change ScaledObject training settings or HPA metric
specifications. Paused ScaledObjects and composite `scalingModifiers` HPAs are
excluded. Configure or disable this check through
`components.controller.env.REPLICASENSE_HPA_RECOVERY_ENABLED` and the associated
`REPLICASENSE_HPA_RECOVERY_INTERVAL` / `REPLICASENSE_HPA_RECOVERY_PROBE_TIMEOUT`
settings.

Published releases are available as an OCI chart. For example, after the
`v0.4.0` Git tag completes the release workflow:

```bash
helm upgrade --install replicasense oci://ghcr.io/canberkturan/charts/replicasense \
  --version 0.4.0 \
  --namespace replicasense-system \
  --set clusterID=YOUR_CLUSTER_ID
```

The chart defaults to matching immutable release tags, with native XGBoost
images for the forecaster and internally-created Trainer Jobs. A successful
training run stores an immutable candidate; it is activated only after the
promotion policy accepts its no-leakage validation results. Users do not create
or promote Jobs. If you mirror the chart to a private registry, authenticate
with that registry before installation.

For topology, model behavior, automatic training, and capacity guardrails, see
the [architecture guide](../../docs/architecture.md). For available dashboard
and alert queries, see the [metrics reference](../../docs/metrics.md).
