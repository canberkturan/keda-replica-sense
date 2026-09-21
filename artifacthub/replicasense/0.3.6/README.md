# ReplicaSense KEDA External Scaler

ReplicaSense adds a safety-approved predictive replica recommendation to a
named native Prometheus trigger while retaining KEDA's reactive path.

Install the published OCI chart after applying `migrations/schema.sql` to an
empty PostgreSQL database:

```bash
helm upgrade --install replicasense oci://ghcr.io/canberkturan/charts/replicasense \
  --version 0.3.6 --namespace replicasense-system --create-namespace \
  --set clusterID=YOUR_CLUSTER_ID
```

GRU training Jobs use the release-matched CPU PyTorch Trainer by default.
See the enterprise installation guide for configuration, operations, and
safety limits.
