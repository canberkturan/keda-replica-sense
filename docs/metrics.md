# ReplicaSense metrics reference

ReplicaSense exposes Prometheus metrics on `/metrics`, port `8080`. The Helm
chart creates Services and `ServiceMonitor` resources for the controller,
sampler, forecaster, and external scaler by default. If Prometheus Operator is
not used, scrape those Services with your existing Prometheus configuration.

The controller also exposes the standard controller-runtime metrics, while all
long-running Go services expose the standard `go_*` and `process_*` runtime
metrics. The ReplicaSense metrics below answer the operational questions that
matter during autoscaling: did we sample, did we forecast, did KEDA receive a
fresh value, and did cluster capacity allow speculation?

All workload metrics use the same stable identity labels:

| Label | Meaning |
| --- | --- |
| `cluster_id` | The Helm `clusterID` value. |
| `namespace` | Namespace of the KEDA `ScaledObject`. |
| `scaled_object` | Name of the KEDA `ScaledObject`. |
| `predictive_trigger` | Name of the ReplicaSense external trigger. |

`replicasense_forecast_demand` also has `quantile`; `replicasense_model_info`
also has `engine`. Raw PromQL is intentionally not a label, avoiding
high-cardinality metrics and accidental query disclosure.

## Metrics

| Metric | Emitted by | Unit | Meaning |
| --- | --- | --- | --- |
| `replicasense_observed_demand` | sampler | Source-metric unit | Most recent source demand read from Prometheus and persisted by ReplicaSense. For an RPS trigger, this is RPS. |
| `replicasense_forecast_demand` | forecaster | Source-metric unit | Maximum predicted demand within the configured decision horizon. `quantile="0.50"` is the median; the other value is the configured upper operational quantile, commonly `0.95`. |
| `replicasense_surge_projection_demand` | forecaster | Source-metric unit | Deterministic short-term surge projection. `0` means surge protection is inactive. It is kept separate from the ML forecast. |
| `replicasense_predictive_metric` | forecaster | Replicas | Safety-approved desired replica recommendation returned through the KEDA external scaler. It has an HPA target of one, so do not compare it directly with source-demand values. |
| `replicasense_snapshot_age_seconds` | forecaster | Seconds | Age of the latest forecast snapshot. A growing value means no fresh forecast is being produced. |
| `replicasense_forecast_source_age_seconds` | forecaster | Seconds | Age of the source observation used by the latest forecast. This detects a frozen input even when a process is still producing snapshots. The scaler withholds a snapshot if either this source age or snapshot age exceeds its freshness limit. |
| `replicasense_model_info` | forecaster | Info gauge | `1` for the active model engine for a workload. Use the `engine` label to identify it. |
| `replicasense_sampler_cycles_total` | sampler | Counter | Sampling loops by `success` or `error`. A rising error counter means at least one active workload or the persistence step failed in that cycle. |
| `replicasense_sampler_last_success_unixtime` | sampler | Unix seconds | Time of the latest fully successful sampler cycle. Unlike process uptime, it proves useful work completed. |
| `replicasense_forecaster_cycles_total` | forecaster | Counter | Forecast/evaluation loops by `success` or `error`. |
| `replicasense_forecaster_last_success_unixtime` | forecaster | Unix seconds | Time of the latest fully successful forecast and evaluation cycle. |
| `replicasense_cluster_resource_capacity` | forecaster | CPU cores or bytes | Cluster capacity accounting used by the speculative guard. Labels: `resource` (`cpu` or `memory`) and `state` (`allocatable`, `requested`, `available`, `speculative_budget`). |
| `replicasense_scaler_requests_total` | external scaler | Counter | KEDA gRPC requests by `method` and whether a fresh snapshot was `served` or `withheld`. A withheld value is fail-closed, not necessarily an incident. |
| `replicasense_scaler_served_snapshot_age_seconds` | external scaler | Seconds | Age of the snapshot on the most recent served scaler request. It becomes `0` when the latest request was withheld. |

The persisted compatibility name `ForecastP95` represents the configured upper
operational quantile. It is not necessarily literal P95 when a workload sets a
different valid `quantile` value.

## Useful PromQL

Replace the example label filters with the workload being investigated.

```promql
# Observed demand against the selected upper forecast.
replicasense_observed_demand{namespace="shop", scaled_object="checkout"}
or
replicasense_forecast_demand{namespace="shop", scaled_object="checkout", quantile="0.95"}
```

```promql
# The predictive replica recommendation and an active surge projection.
replicasense_predictive_metric{namespace="shop", scaled_object="checkout"}
or
replicasense_surge_projection_demand{namespace="shop", scaled_object="checkout"} > 0
```

```promql
# Forecasts older than three minutes. Tune the threshold to your
# sampling/forecast interval and operational policy.
replicasense_snapshot_age_seconds > 180
or
replicasense_forecast_source_age_seconds > 180
```

```promql
# Active model engine per workload.
replicasense_model_info == 1
```

```promql
# Did sampling and forecasting complete recently? The alert threshold should
# be larger than the configured interval to allow a little scheduling delay.
time() - replicasense_sampler_last_success_unixtime > 180
or
time() - replicasense_forecaster_last_success_unixtime > 180
```

```promql
# Rate of scaler requests that had no fresh predictive answer during the last
# five minutes. Inspect snapshot age, model status, and forecaster errors
# before treating this as an application incident.
sum(rate(replicasense_scaler_requests_total{outcome="withheld"}[5m]))
```

```promql
# Available CPU and the portion ReplicaSense is willing to spend on predictive
# scale-up. Memory uses the same query with resource="memory".
replicasense_cluster_resource_capacity{resource="cpu",state="available"}
or
replicasense_cluster_resource_capacity{resource="cpu",state="speculative_budget"}
```

## Dashboard and alerting guidance

For one workload, graph observed demand, both forecast quantiles, surge
projection, and predictive replicas in separate panels or axes: source demand
and replicas are different units. Pair the predictive-replica panel with the
KEDA HPA's current replica count and the native trigger's current metric.

Alert on an absent scrape as usual, then separately alert when the sampler or
forecaster last-success timestamp is old. The latter catches a live process
that has stopped doing useful work. Alert on a sustained scaler `withheld` rate
only after choosing a threshold that matches your normal model warm-up and
staleness policy.

A nonzero surge projection is an event worth annotating, not automatically an
incident. During an investigation, compare it with observed demand, the upper
forecast, and current replicas before changing safety limits. If the cluster
budget is regularly below the desired speculative capacity, add real cluster
headroom or lower workload limits; do not simply disable the guardrail.

Metrics describe the current operational path. Historical forecast accuracy,
validation, promotion decisions, and safety reasons are stored in PostgreSQL
for workloads that ReplicaSense manages.
