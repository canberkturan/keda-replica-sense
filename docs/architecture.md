# How ReplicaSense fits into your platform

ReplicaSense is deliberately a companion to KEDA, not a second autoscaling
system. KEDA still owns the `ScaledObject`, creates the HPA, and reacts to the
current Prometheus metric. ReplicaSense adds one more KEDA trigger: a cautious
recommendation based on the likely peak during the time it takes a new pod to
be useful.

That split matters. A missing model, a stale forecast, a PostgreSQL problem, or
an unsafe prediction makes the ReplicaSense trigger return zero. The named
native trigger is still able to react to real load.

## The path from metric to replica

```text
Prometheus
    |  historical range query + scheduled point samples
Sampler ------------------> PostgreSQL <---------------- Trainer Job
                                |                         (daily, per workload)
                                |
Forecaster <--------------------+---- Kubernetes capacity and health checks
    | fresh, safety-approved snapshot
External scaler ---- gRPC ----> KEDA ----> HPA ----> your Deployment
```

The controller watches `ScaledObject` resources. A predictive trigger points
at a named native Prometheus trigger using `sourceTrigger`; the controller
stores that contract, including its query, threshold, forecast horizon, and
business timezone. It does not ask application teams to create ReplicaSense
custom resources or training Jobs.

The sampler owns the data set. When it first sees a workload it backfills the
requested training window from Prometheus, then records point samples at the
configured interval. Samples are tied to the contract fingerprint, so a query
change starts a clean data lineage instead of mixing unrelated metrics.

### Short interruptions and estimated inference inputs

A few missed minutes should not force a trained model to wait for a new day of
history. The forecaster can bridge a short gap between two actual observations
with linear interpolation in a disposable input buffer. These values never go
into the `samples` table, Trainer Jobs, validation targets, or surge evidence.
The real source timestamp remains the freshness check; there is no tail
extrapolation and an old forecast is never made fresh by changing its timestamp.

Recovery is enabled by default. The actual bounding observations must be at most
five minutes apart, with sampling-alignment jitter allowance. Their values must
be reasonably close (relative difference at most 25% of the larger anchor, with
a unit floor), and at most 5% of model inputs and the freshest 60 input slots may
be estimated. At least two distinct real sampling slots must follow the gap.
Backfill/live overlap is reduced to the latest real observation per slot only
in this disposable buffer; the persisted observations are left unchanged.

Trained XGBoost uses its current point plus 60 lag samples. GRU uses the sequence
length recorded in its artifact, including CPU PyTorch artifacts. This inference
lookback is separate from the training window and validation requirements.
Statistical models retain their normal history requirements. A long gap, unstable
anchors, or excessive missingness is not bridged: forecasting remains withheld
until there is enough fresh, valid post-gap context for the selected model.

When estimated inputs are used, the forecast audit reason includes
`inference_interpolated_points=N;conservative_gap_recovery`. The operational
recommendation cannot request downscale from estimated inputs, and predictive
scale-up is capped at one additional replica per decision, with a 25% fractional
guard before HPA rounding. Existing cluster-health, capacity, replica-bound, and
speculative-budget checks still apply. This is a cautious recovery estimate,
not a claim that the missing demand was observed. Native KEDA remains the
reactive path, and surge detection consumes distinct real post-gap observations.

Set `REPLICASENSE_SHORT_GAP_INTERPOLATION_ENABLED=false` to disable bridging,
or lower `REPLICASENSE_INFERENCE_MAX_GAP` from its `5m` default. Both are
available under `components.forecaster.env` in Helm. Look at
`replicasense_forecast_ready`, `replicasense_forecast_status`, and
`replicasense_forecast_input_estimated_samples` when diagnosing recovery.

The forecaster asks, “what is the highest demand likely to occur in the next
decision horizon?” It returns one horizon-maximum value, not a minute-by-minute
trajectory. This is why several consecutive forecasts can be close together
when the signal and calendar features are stable. The decision horizon is:

```text
forecastHorizon + startupLatency + safetyBuffer
```

The external scaler translates the safety-approved result into a KEDA metric
with a target of one. In other words, its numeric value is already a desired
replica count. KEDA combines it with the native trigger and respects the
ScaledObject bounds and HPA behavior.

## Topology and failure behavior

The chart uses a small, purposeful topology:

| Component | Default replicas | Why it exists | Failure behavior |
| --- | ---: | --- | --- |
| Controller | 2 | Watches ScaledObjects and keeps contracts current. | Leader election allows one active reconciler; the other is standby. |
| Sampler | 1 | Backfills and persists Prometheus samples. | Sampling pauses; old forecasts eventually become stale and are withheld. When a source query recovers, ReplicaSense attempts a bounded, authoritative range repair of the recent gap. |
| Forecaster | 1 | Generates forecasts, runs guardrails, and evaluates mature snapshots. | No new prediction is served after the freshness window. A valid continuous post-outage segment can resume inference without waiting for a historical gap to age out of the whole training window. |
| External scaler | 2 | KEDA gRPC endpoint. | A PDB and preferred cross-node placement protect one-pod loss. |
| Training scheduler | 1 | Creates daily per-workload Trainer Jobs. | Existing active models continue until their maximum age. |
| Trainer Job | on demand | Trains and validates one workload model. | A failed candidate is not promoted. |

The scaler is the availability-sensitive component because KEDA must discover
every trigger on a combined ScaledObject. Keep two replicas, keep the PDB, and
place them on separate nodes when the cluster has more than one node. If every
scaler endpoint is unavailable, KEDA cannot read the predictive trigger's metric
specification. During discovery it can leave a combined HPA configured with
only the native metric, even after the scaler becomes reachable again.

The elected ReplicaSense controller checks existing KEDA-owned HPAs every 30
seconds. If a valid ReplicaSense trigger is missing from an HPA, it first checks
that metric through KEDA's external metrics API, then changes only the HPA's
`replicasense.keda.sh/predictive-metric-recovery` annotation. KEDA watches HPA
annotation changes and rebuilds the metric configuration itself. ReplicaSense
does not write HPA metric specifications, replica counts, or ScaledObject
training settings. Discovery can recover with a zero predictive value, so it
does not need to wait for training or an active prediction.

Failed probes and reconciliation requests back off from 30 seconds to at most
five minutes. A restored metric resets the backoff. Recovery respects custom
HPA names and verifies the ScaledObject owner's UID; unrelated, deleting, or
paused resources are left alone. ScaledObjects using `scalingModifiers` are
excluded because their HPAs intentionally contain a composite metric, not the
individual predictive trigger metric. Missing HPAs are left for KEDA to create.

The controller needs HPA `get`/`patch` and external-metric `get`/`list` permissions;
the Helm chart supplies these. It needs no scaler authentication Secrets or
direct controller-to-scaler network access. Monitor
`replicasense_hpa_missing_predictive_metrics` and
`replicasense_hpa_recovery_total` to distinguish a missing HPA configuration
from a forecast that is safely withheld.

The sampler and forecaster are intentionally singletons by default. Their work
is persisted in PostgreSQL and duplicate writers do not make forecasts safer.
Scale them only after measuring database, Prometheus, and reconciliation load.

## Choosing and training models

`xgboost` is the default and is the usual choice for business traffic with
repeating calendar patterns, lags, and non-linear shape. It trains a median and
an upper operational quantile. A new or materially changed XGBoost workload
does not provide predictive scale-up until a validated model is active; the
native trigger remains active during that period.

The other engines are useful comparisons or simpler operational choices:

| Engine | Best fit | What it predicts |
| --- | --- | --- |
| `seasonal-baseline` | A strong repeating daily pattern | Same point in the previous season, adjusted to the decision horizon. |
| `rolling-quantile` | A conservative, transparent fallback | Quantile of historical horizon maxima. |
| `holt-winters` | Smooth level, trend, and seasonality | Additive trend/seasonal projection. |
| `xgboost` | Calendar-driven, non-linear recurring patterns | Learned horizon-maximum quantiles. |
| `gru` | Repeating patterns with short-term sequence dynamics | A gated recurrent network trained on 60 causal demand samples plus business-time calendar features. |

The GRU input sequence contains the observed demand, clock time, day of week,
day of month, month, and weekend status in `businessTimezone`. It predicts the
same horizon maximum used by the scaler; it does not attempt to predict a truly
unseen surge. Keep surge protection and the native reactive trigger enabled for
that case. GRU artifacts are numeric JSON and run in the normal Go runtime, so
the existing trainer and forecaster images are sufficient.

Training is automatic. The scheduler considers each active workload once per
`REPLICASENSE_TRAINING_INTERVAL` (24 hours by default), spreads jobs within the
slot, and writes a candidate into PostgreSQL. The candidate is evaluated with
walk-forward windows and is promoted only if it meets the policy. A completed
Job is cleaned up by the controller; the run and model records remain for
audit.

An external trigger can request one idempotent immediate run with
`immediateTraining: "true"`. Pair it with `clearOldModels: "true"` only when
you explicitly want to discard the current model artifacts for that durable
workload identity. The reset is keyed to the full policy revision, so ordinary
controller reconciliations cannot repeatedly delete models or enqueue Jobs.
An immediate Job waits for sampler history, and its policy revision is verified
again by the trainer before it can publish a model. A Job from a replaced
configuration therefore cannot reintroduce an obsolete model after a reset.

Do not retrain every hour just because data arrives every minute. The
day-to-day traffic shape normally changes more slowly than that, while frequent
native-model training can create avoidable CPU, memory, and database pressure.
Use a long enough window to cover several repeating cycles—30 days is a good
first production setting for a daily pattern.

Trainer Jobs have their own CPU/memory requests and limits and a one-hour
active deadline by default. They are configured under the chart's `trainer`
values, independently from the lightweight scheduler Deployment. Size those
limits from a representative model run; the scheduler will reject invalid
resource quantities rather than silently launching an unbounded Job.

## Configuration that changes outcomes

The metric and threshold should describe a unit of work that a replica can
handle. For example, if one pod safely handles 20 requests per second, use an
RPS query and set `threshold: "20"`. Avoid a query that returns multiple series:
the sampler deliberately rejects ambiguous results.

Set `businessTimezone` to the timezone where the workload’s behavior happens,
not necessarily the cluster timezone. For a shop whose traffic follows Istanbul
business hours, use `Europe/Istanbul`. Calendar features are otherwise shifted
and the model will learn the wrong hour-of-day relationship.

Use PromQL `offset` only when you intentionally accept delayed telemetry. It
selects older values while Prometheus still evaluates the expression at the
current timestamp. That can shift calendar features relative to demand; it is
useful for delay testing, but it is not a normal production setting.

`startupLatency` and `safetyBuffer` should reflect measured reality: image pull,
scheduling, readiness, cache warm-up, and a small operational margin. They are
part of the forecasted decision horizon, so overstating them makes predictions
more conservative.

## Capacity guardrails

Before approving speculative scale-up, the forecaster checks both the absolute
cluster replica budget and a fraction of available allocatable CPU and memory.
It accounts for container requests, init-container peaks, and pod overhead.
If it cannot read a deployment, nodes, or pods—or if a target has no CPU or
memory requests—it withholds speculative scale-up. That is deliberate: set
requests on every workload managed by ReplicaSense.

Use `REPLICASENSE_CLUSTER_SPECULATIVE_REPLICA_BUDGET` to cap the total extra
replicas caused by prediction, and
`REPLICASENSE_SPECULATIVE_HEADROOM_FRACTION` to reserve a portion of currently
free cluster capacity. See the capacity metrics in [metrics.md](metrics.md)
before increasing either value.
