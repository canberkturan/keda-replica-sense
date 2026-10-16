# ReplicaSense KEDA External Scaler

ReplicaSense adds a safety-approved predictive replica recommendation to a
named native Prometheus trigger while retaining KEDA's reactive path.

Install the published OCI chart after applying `migrations/schema.sql` once to
a new, empty PostgreSQL database:

```bash
helm upgrade --install replicasense oci://ghcr.io/canberkturan/charts/replicasense \
  --version 0.4.0 --namespace replicasense-system --create-namespace \
  --set clusterID=YOUR_CLUSTER_ID
```

An existing 0.3.9 database needs no schema migration or model reset for this
release. Upgrade with the new chart to receive the HPA recovery permissions.

The controller repairs missing predictive HPA metrics after scaler discovery
recovers. The forecaster can bridge tightly bounded short gaps using marked,
inference-only estimates; these never become observed or training data. Long,
stale, or unreliable inputs remain withheld. Recovery settings and state are
visible through Prometheus metrics.

GRU training Jobs use the release-matched CPU PyTorch Trainer by default.
Both trainers repair missing PostgreSQL history from the configured Prometheus
source before fitting; unresolved source gaps fail training without fabricated
observations. Trained artifacts become candidates and must pass shared
holdout comparisons before replacing an active model. Upgrade the forecaster,
scheduler, and trainer images together to use this flow.

The Go dependency is upgraded to KEDA 2.21.0 for CVE-2026-77524. The separately
installed KEDA operator must also be patched; the ReplicaSense chart does not
upgrade it.

Every release image must pass a vulnerability scan before publication. See
the installation, architecture, and metrics guides for operational details.
