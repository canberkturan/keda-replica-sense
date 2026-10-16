# Model promotion validation — 16 October 2026

This is a point-in-time functional check, not a claim that one engine is best
for every workload. The implementation and operational settings are described
in [model promotion](model-promotion.md).

## Lab setup

- Cluster: `keda-lab-v2`.
- ScaledObjects: `rs-xgboost-test-predictive` and `rs-gru-test-predictive` in
  `replicasense-test`.
- Unchanged source: `replicasense_load_test_metric{scenario="branch_general_v5"}`.
- Threshold: 500; replica bounds: 1–10; quantile: 0.97.
- Forecast horizon: 10 minutes plus one minute startup latency and one minute
  safety buffer, giving a 12-minute target horizon.
- Training window: 30 days. Existing observations and artifacts were preserved.

Both engines were retrained through the existing claim/Trainer Job mechanism
using local test images. The CPU PyTorch GRU Job completed in about 40 seconds;
the native XGBoost Job completed in about five seconds. Both published candidates
without activating them. Trainer wall times are not a controlled performance
benchmark.

An earlier 23-hour history gap was found during inspection. By live evaluation,
the sampler's history recovery had repaired it: the current 30-day source had
no gaps exceeding two minutes. No synthetic observations were inserted by the
promotion tests.

## Live shadow comparison

The shared Go worker compared the exact stored candidate, active champion, and
seasonal baseline for each workload on 239 complete, non-overlapping horizons.
The origins and observations covered approximately 14 October 19:26 UTC to
16 October 19:14 UTC. Separate sampler timestamps differ by seconds between
workloads; within each comparison every artifact used exactly the same inputs,
origins, and targets. No old champion training scores were used.

| Engine / artifact | Coverage | Quantile loss | Mean peak-period shortfall | Excess replica-minutes proxy |
| --- | ---: | ---: | ---: | ---: |
| XGBoost champion | 96.23% | 6.5715 | 20.4461 | 564 |
| XGBoost candidate | 96.65% | 6.3919 | 23.4392 | 576 |
| GRU champion | 97.49% | 10.0618 | 32.8972 | 972 |
| GRU candidate | 98.33% | 10.1480 | 25.3641 | 1152 |

The XGBoost candidate was rejected for peak underprediction regression despite
better average loss. Its loss improvement was also below the default 3% margin.
The GRU candidate was rejected because quantile loss did not improve. Better
coverage alone did not justify its extra predicted capacity. Both original
champions remained active; no candidate was forced through a gate.

A preliminary replay using a longer GRU period had produced a different
decision. The final verdict above uses the configured live 48-hour window.
This difference is why evaluation boundaries must accompany any reported score.

## Upgrade and end-to-end checks

The existing champions predated the new training-contract metadata. Both
successfully earned fresh serving-contract certificates on 239 complete recent
horizons. Their artifact IDs and original training metadata were unchanged.
The cache accepted the renewed validity timestamp without reloading a different
artifact or waiting for another training run.

After returning the lab to automatic mode:

- Both workloads exposed `replicasense_forecast_ready=1` and fresh upper-quantile
  demand forecasts.
- The external KEDA metrics API returned a predictive recommendation of six
  replicas during the check. This is a sampled observation, not a guarantee
  that every later recommendation remains six.
- Cluster Prometheus scraped the new model-evaluation status metrics.
- The evaluation interval was restored to five minutes; automatic rollback
  remained disabled by default.

The tested forecaster and trainers are local development images, not a new
published release. No new tag was created as part of this validation.

## Automated safety checks

The full Go suite, race detector, vet, command builds, native XGBoost tests,
Python temporal-isolation tests, Helm lint, and chart rendering passed.

Tests cover identical evaluation origins, disjoint target horizons, fitting
cutoffs, duplicate observations, missing windows, stale sources, nonfinite
outputs, quantile-aware gates, peak severity, meaningful improvement, shadow
mode, and cancellation.

An isolated PostgreSQL instance verified atomic promotion/rollback, actual
workload-policy changes, competing stale activation reports, advisory locking,
bounded audit history, pending-candidate queue order, legacy validity renewal,
and rejection of stale-window certificates. Rollback's positive and negative
paths were exercised in automated service and database tests; a model-quality
failure was not deliberately introduced into the live lab to force rollback.

## Trainer-owned history repair

The trainer-repair extension was also tested against the same current metric.
Forty-eight hours of actual Prometheus results were copied into a disposable
PostgreSQL database, omitting five slots for each test workload: leading,
interior, and trailing gaps. The lab database was not modified for this test.

| Trainer image | Missing before | Inserted from Prometheus | Missing after | Result |
| --- | ---: | ---: | ---: | --- |
| Native XGBoost | 5 | 5 | 0 | Candidate training succeeded |
| CPU PyTorch GRU, using shared Go helper | 5 | 5 | 0 | Candidate training succeeded |

Both containers ran non-root with read-only filesystems, no added capabilities,
two-CPU/two-GiB limits, and writable temporary storage. They did not activate
their candidates. This verifies history preparation and trainer execution, not
the predictive quality of models fitted to this shorter test window.

Automated HTTP/PostgreSQL tests additionally verified unavailable source slots,
policy changes during repair, preservation of jittered observations, and a
sampler write arriving during an HTTP request. Repeat preparation of complete
history made zero range requests. Large histories were chunked, fragmented
holes were coalesced, and timeout/point budgets were enforced. Scheduler tests
confirmed that immediate training starts without waiting for cached history.
