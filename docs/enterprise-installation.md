# Install and operate ReplicaSense

ReplicaSense works with any Kubernetes environment that has KEDA, Prometheus,
and PostgreSQL. It can be used by a single application team, a platform team,
or a larger production environment. Keep KEDA's native reactive trigger, then
add ReplicaSense as a separate predictive trigger.

ReplicaSense does not replace KEDA. If a prediction is unavailable, stale, or
unsafe, its predictive metric returns zero while the native KEDA trigger keeps
reactive scaling available.

Read [how ReplicaSense fits into your platform](architecture.md) first if you
are deciding where to place it, which model to use, or how training and the
capacity guard work.

## Before you begin

You need:

- Kubernetes 1.28 or later and KEDA 2.x.
- Helm 3.13 or later.
- Prometheus reachable by KEDA and the ReplicaSense sampler.
- A PostgreSQL database for ReplicaSense. TLS is recommended for every
  networked deployment.
- Permission to create a namespace, Secrets, Deployments, Services, and KEDA
  `ScaledObject` resources.

The chart does not install KEDA, Prometheus, or PostgreSQL. This keeps those
shared platform services under the control of their normal operators.

## 1. Prepare PostgreSQL

Apply the bootstrap schema once to a new, empty database with a database-owner
role. It is not an idempotent upgrade tool; do not rerun it against an existing
ReplicaSense database.

```bash
psql "$REPLICASENSE_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/schema.sql
```

Create a separate runtime account with the least privileges needed by the
application. Use a PostgreSQL connection URL that enforces your TLS policy,
for example `sslmode=require`.

## 2. Create the namespace and database Secret

```bash
kubectl create namespace replicasense-system
kubectl -n replicasense-system create secret generic replicasense-database \
  --from-literal=url='postgres://USER:PASSWORD@HOST:5432/replicasense?sslmode=require'
```

If you use private container images, also create an image-pull Secret and pass
it through `imagePullSecrets`. Public GHCR images need no registry credentials.

```bash
kubectl -n replicasense-system create secret docker-registry registry-pull \
  --docker-server=REGISTRY_HOST \
  --docker-username=REGISTRY_USER \
  --docker-password=REGISTRY_TOKEN
```

## 3. Install the chart

Install the published OCI chart with a stable cluster identifier. The identifier
is a label value used to separate metrics and stored forecasts from other
clusters; it does not need to be globally public.

```bash
helm upgrade --install replicasense \
  oci://ghcr.io/canberkturan/charts/replicasense \
  --version 0.3.2 \
  --namespace replicasense-system \
  --set clusterID=cluster-east-1
```

For a private registry, add:

```bash
--set imagePullSecrets[0].name=registry-pull
```

The chart creates Prometheus Operator `ServiceMonitor` resources by default.
If your Prometheus installation does not use that operator, disable them with
`--set serviceMonitor.enabled=false` and configure scraping for the controller,
sampler, forecaster, and scaler Services yourself.

Check the rollout:

```bash
kubectl -n replicasense-system get deploy,pod,svc
kubectl -n replicasense-system rollout status deployment/replicasense-controller
kubectl -n replicasense-system rollout status deployment/replicasense-scaler
```

### Production values worth reviewing

The chart has deliberately modest resource defaults and two replicas for the
controller and external scaler. Treat them as a safe starting point, then tune
them from real Prometheus and database measurements. Do not remove CPU or
memory requests from your application workloads: ReplicaSense needs them to
decide whether speculative replicas fit in cluster headroom.

For a production change review, keep a values file in your own configuration
repository. This is a useful minimum:

```yaml
clusterID: production-eu-1
imagePullPolicy: IfNotPresent

components:
  scaler:
    replicas: 2
    image:
      repository: ghcr.io/canberkturan/replicasense-scaler
      # A digest takes precedence over tag and is preferred after approval.
      digest: sha256:REPLACE_WITH_APPROVED_DIGEST
  forecaster:
    resources:
      requests: {cpu: 500m, memory: 1Gi}
      limits: {cpu: "2", memory: 2Gi}
    env:
      REPLICASENSE_CLUSTER_SPECULATIVE_REPLICA_BUDGET: "40"
      REPLICASENSE_SPECULATIVE_HEADROOM_FRACTION: "0.20"
  sampler:
    env:
      # Limits post-outage history repair. Keep it within Prometheus query
      # capacity; if history cannot be recovered, forecasts wait for a clean
      # continuous segment instead.
      REPLICASENSE_SAMPLING_RECOVERY_WINDOW: 24h
```

Use `helm upgrade --install ... -f production-values.yaml`. Pinning an image
digest protects against an unexpected tag change; update it through your normal
image-review process. The chart disables Kubernetes API token mounting for the
sampler and scaler because those pods do not need it.

## 4. Add a predictive trigger to a workload

Keep a named native Prometheus trigger and add a ReplicaSense external trigger
that names it through `sourceTrigger`. The reactive trigger and its threshold
remain the application's availability path; ReplicaSense derives its forecast
contract from the same metric.

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
      quantile: "0.95"
      businessTimezone: UTC
      modelEngine: xgboost
      startupLatency: 90s
      safetyBuffer: 30s
```

Use a timezone that matches the workload's business cycle. XGBoost is the
default model engine. After a workload is changed to XGBoost, prediction stays
fail-closed until its first validated model is active; the native KEDA trigger
continues to work during that period.

Set `modelEngine: gru` when a workload has a stable repeating pattern but the
most recent demand sequence also carries useful shape information. GRU uses
the last 60 samples and the configured business-time clock, day-of-week, day-of-month,
month, and weekend signals. It predicts a future-horizon maximum, not an
unpredictable incident; retain the native trigger and surge protection. GRU is
included in the standard Go runtime—no separate Trainer or Forecaster image is
needed.

For common model choices, training behavior, and the meaning of a
horizon-maximum forecast, see [architecture.md](architecture.md). A forecast
that barely changes from one minute to the next is not automatically stuck: it
is one estimate of the maximum over the configured horizon, and stable inputs
often lead to the same model region and a similar answer.

### Train now or replace a model deliberately

Training normally runs once per workload per day. For a new workload, a model
engine change, or a controlled benchmark, add either of these boolean metadata
fields to the ReplicaSense external trigger:

```yaml
      immediateTraining: "true"
      clearOldModels: "true"
```

`immediateTraining` creates one Trainer Job for this exact configuration
revision without waiting for the daily slot. The scheduler first waits until
the sampler has backfilled a complete training window for the current metric
contract; this avoids spending the one-time request on an undersized dataset.
It checks again every minute and starts as soon as that history is available.
The request is idempotent: controller reconciliations and scheduler restarts
do not create duplicate Jobs.

Each Trainer Job is bound to the policy revision that created it. If the
ScaledObject changes while a Job is pending or running, that stale Job fails
before it can publish a candidate or reactivate a model for the replacement
contract.

`clearOldModels` is deliberately destructive. When the configuration revision
changes, ReplicaSense removes all model artifacts for the same durable workload
identity and detaches them from retained training-run audit records. Use it
when you intentionally want a completely fresh model after changing the signal,
feature assumptions, or engine. If `immediateTraining` is false, the old model
is cleared now and the normal daily schedule trains the replacement.

Do not leave `clearOldModels: "true"` in a frequently edited ScaledObject
without understanding that every meaningful predictive configuration change is
a fresh-start request. A changed query also changes the source fingerprint, so
old samples are not mixed into a new data set.

### Schema update for existing installations

New databases created from `migrations/schema.sql` already contain the fields
used by this feature. Before running a release that includes it against an
existing ReplicaSense database, apply this reviewed, additive schema update
with the database-owner role:

```sql
ALTER TABLE workloads
  ADD COLUMN IF NOT EXISTS immediate_training BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS clear_old_models BOOLEAN NOT NULL DEFAULT FALSE;
```

## 5. Observe and operate

Start with the [metrics reference](metrics.md). It explains the source-demand,
forecast, surge, safe-replica, snapshot-age, model, scaler, cycle-health, and
cluster-budget metrics.

```bash
kubectl -n replicasense-system get scaledobjects,keda,svc
kubectl -n replicasense-system logs deployment/replicasense-sampler --tail=100
kubectl -n replicasense-system logs deployment/replicasense-forecaster --tail=100
```

For production hardening, use image digests after release approval, restrict
who can change `ScaledObject` resources, apply NetworkPolicies appropriate to
your cluster, and monitor database capacity, scaler request outcomes, and
forecast/sampler last-success timestamps.
The chart runs containers as non-root with a read-only root filesystem, dropped
Linux capabilities, and RuntimeDefault seccomp.

The training scheduler creates and cleans up ReplicaSense-labeled Trainer Jobs.
Users do not need to create or promote Jobs manually. Successful candidates are
validated before promotion; rejected candidates stay auditable in PostgreSQL.

## Safety behavior and limits

- Keep the native Prometheus trigger on every predictive `ScaledObject`.
- The external metric uses a target of one, so the safe predictive value is a
  desired replica recommendation, subject to KEDA/HPA bounds and behavior.
- Surge detection is deterministic and independent of the ML forecast. It acts
  only for the time a new pod needs to become useful, not for the whole ML
  horizon.
- If Prometheus or the source series is unavailable, ReplicaSense withholds
  predictive metrics once their input ages past the freshness limit; the native
  KEDA trigger remains the reactive path. After recovery it range-backfills a
  bounded recent gap when Prometheus can supply authoritative history. If that
  is not possible, it resumes only after collecting a valid continuous segment
  and never fabricates missing demand values.
- If every external-scaler Service endpoint is unavailable, KEDA cannot obtain
  that external metric. Restore a scaler endpoint; the combined `ScaledObject`
  cannot use its reactive path during this KEDA discovery failure.
