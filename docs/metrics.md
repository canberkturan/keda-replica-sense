# ReplicaSense metrics reference

ReplicaSense exposes Prometheus metrics from the sampler and forecaster on
`/metrics` port `8080`. The Helm chart creates Services and `ServiceMonitor`
resources for both components by default. If Prometheus Operator is not used,
scrape those Services with your existing Prometheus configuration.

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
| `replicasense_model_info` | forecaster | Info gauge | `1` for the active model engine for a workload. Use the `engine` label to identify it. |

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
```

```promql
# Active model engine per workload.
replicasense_model_info == 1
```

## Dashboard and alerting guidance

For one workload, graph observed demand, both forecast quantiles, surge
projection, and predictive replicas in separate panels or axes: source demand
and replicas are different units. Pair the predictive-replica panel with the
KEDA HPA's current replica count and the native trigger's current metric.

Alert on snapshot age based on the workload's update interval and on an absent
sampler/forecaster scrape according to your Prometheus policy. A nonzero surge
projection is an event worth annotating, not automatically an incident. During
an investigation, compare it with observed demand and the current replica
count before changing safety limits.

Metrics describe the current operational path. Historical forecast accuracy,
validation, promotion decisions, and safety reasons are stored in PostgreSQL
for workloads that ReplicaSense manages.
