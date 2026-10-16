# When a trained model becomes active

A finished Trainer Job means we have an artifact, not that it should control
scaling. ReplicaSense keeps the active model while a new candidate earns its
place. This applies equally to native XGBoost, exported CPU PyTorch GRU, and
the statistical engines.

## Training and comparison are separate

Trainers only create candidates. Go reserves up to 48 hours (at most 20% of
the available segment) from fitting. PyTorch uses temporal training,
calibration, and holdout partitions; its normalization uses training values
only. Before either trainer fits a model, a shared Go preflight repairs gaps
from the workload's original Prometheus query. It checks the entire configured
window, inserts only missing real observations, reloads PostgreSQL, and refuses
to fit if any slots remain unavailable. It never joins an unresolved outage
or silently substitutes an older segment. The stored `training_window_end`
includes all observations consumed for fitting or calibration.

Repair includes leading and trailing missing history, not just interior gaps.
The current open sampling slot is excluded. Existing observations are never
overwritten, including sampler points arriving during repair. Repeated runs
query nothing when history is already complete. PyTorch calls the same static
Go helper shipped in its image; it does not maintain another repair client.

Limits under `components.training-scheduler.env` are passed to every Job:

| Variable | Default |
| --- | --- |
| `REPLICASENSE_TRAINER_HISTORY_REPAIR_TIMEOUT` | `2m` total repair budget |
| `REPLICASENSE_TRAINER_HISTORY_QUERY_TIMEOUT` | `15s` per request |
| `REPLICASENSE_TRAINER_HISTORY_MAX_POINTS` | `1000000` expected slots; hard limit `2000000` |

Each range request is capped at 10,000 points, with one retry for request errors
and a hard total of 256 HTTP attempts. Fragmented holes may be queried together,
but known slots are filtered out before insertion. Counts and repair boundaries
are saved in `history_repair` training metrics. Prometheus history follows the
configured PromQL expression and its normal lookback/staleness semantics; the
trainer adds no interpolation or invented observations.

Allow source retention to exceed `trainingWindow` with room for query alignment.
If Prometheus cannot provide older samples, training fails with an explicit
incomplete-history error; increase source retention or shorten the window.
Immediate training no longer waits for complete PostgreSQL history, because
the Job owns this preparation. Daily cadence and revision-level deduplication
are unchanged. Upgrade scheduler and both trainer images together.

Every five minutes, a serial background worker in the forecaster checks the
oldest pending candidate for each workload. It loads the exact stored artifact, the
current champion, and the seasonal baseline. Python's diagnostic scores and
the champion's historical training scores do not decide promotion.

All compared predictions have the same origins, horizon, sampling interval,
timezone, quantile, and actual future horizon maxima. Targets must be after
both artifacts' fitting cutoffs. The worker never refits an artifact after
scoring it, never interpolates evaluation inputs or targets, and discards
windows with missing sampling slots. Duplicate backfill/live rows count once.

Replica-bound or training-flag changes do not make an otherwise identical
champion incomparable. If the engine or target semantics changed, or the
active artifact cannot load, the candidate must earn activation against the
baseline instead; an incompatible old model cannot block replacement forever.

By default, evidence comes from the latest 48 hours and must span at least
24 hours, with complete non-overlapping target horizons covering at least
90% of that minimum duration. At least 72 horizons are required. At most 288
are scored, distributed across the eligible period rather than crowded into
the last few minutes. The seasonal baseline also needs complete historical
inputs; a long outage can therefore delay a trustworthy comparison beyond
the return of fresh observations.

Daily retraining does not reset a pending candidate's evaluation clock. Rejected
or incompatible candidates remain stored but leave the evaluation queue. Once
a newer champion is selected, older candidates cannot replace it. Pending
candidates expire after the greater of 72 hours or evaluation-window plus
minimum-duration; this allows longer shadow tests without an unbounded queue.

These defaults cover a daily pattern, not proof of weekly generalization.
For weekly services, configure a longer window and minimum duration after
measuring the extra evaluation cost. Do not lower the evidence requirements
just to make a candidate pass.

## Acceptance policy

The upper forecast must cover at least `quantile - coverageTolerance` of the
observed maxima. For quantile 0.97 and tolerance 0.03, that is 94%. No single
relative shortfall may exceed the configured maximum (50% by default).

Against the champion, a candidate must not worsen the underprediction rate,
mean shortfall, or mean shortfall in the highest-demand 10% of evaluated
horizons. Quantile pinball loss must improve by more than the configured
minimum, 3% by default. A tie retains the champion. When there is no champion,
the same comparison uses the seasonal baseline; a safe, essentially perfect
baseline-equivalent candidate can bootstrap without an artificial loss gain.

The report also includes excess replica-minutes, computed from demand and the
reactive threshold. This is a capacity proxy: it does not simulate HPA
stabilization, readiness, capacity constraints, or actual billing, and is not
currently an acceptance gate. It sums scored horizons only, not unscored time
when the window cap requires subsampling. Surge events are not silently removed from
observations. Test deliberately unexpected surges separately when measuring
recurring-pattern model quality.

## Shadow mode and rollback

Set `REPLICASENSE_MODEL_PROMOTION_MODE=shadow` to compute the same evidence and
decisions without activating or rolling back any artifact. Existing active
models keep serving subject to their normal freshness/age checks. Changing
the mode back to `automatic` requires another current evaluation, not replay
of an old approval. Shadow mode is a model-selection control; it does not
disable predictions from an already active model.
Fresh revalidation may renew that existing model's validity in either mode;
shadow mode prevents artifact switches, not certification of the incumbent.

Automatic rollback is opt-in with `REPLICASENSE_MODEL_ROLLBACK_ENABLED=true`.
The worker monitors the active artifact as well as candidates, so a rejected
candidate cannot block champion monitoring indefinitely. Rollback considers
only the previous champion recorded at activation, requires the same full
contract, and uses real observations after the new model's activation.
The current model must fail revalidation, while the previous model must pass
safety gates and improve loss by more than 10% (or a stricter configured
minimum). The normal minimum duration/windows still apply. Shadow mode records
`shadow_rollback` instead of switching models. This is a model-quality rollback,
not a substitute for the immediate reactive scaling path during incidents.

Activation and rollback lock the workload and compare the current policy and
active model ID in one database transaction. If either changed while scoring,
the decision is not applied. The old artifact is retired, not deleted. Do not
use `clearOldModels=true` for routine retraining if you want comparison and
rollback history preserved.

Successful revalidation refreshes the active model's validity timestamp only
when its safety gates pass and its loss is within 10% of the seasonal baseline.
Withheld or degraded checks do not extend validity. The forecaster still
enforces `REPLICASENSE_MODEL_MAX_AGE` (72 hours by default) and independent
source freshness. An old model can therefore remain usable with fresh,
complete evidence, but never merely because candidates keep getting rejected.

## Helm settings

All settings below belong under `components.forecaster.env`:

| Variable | Default |
| --- | --- |
| `REPLICASENSE_MODEL_PROMOTION_MODE` | `automatic` (`shadow` is the other option) |
| `REPLICASENSE_MODEL_EVALUATION_INTERVAL` | `5m` |
| `REPLICASENSE_MODEL_EVALUATION_WINDOW` | `48h` |
| `REPLICASENSE_MODEL_EVALUATION_MIN_DURATION` | `24h` |
| `REPLICASENSE_MODEL_EVALUATION_MIN_WINDOWS` | `72` |
| `REPLICASENSE_MODEL_EVALUATION_MAX_WINDOWS` | `288` (hard limit 4096) |
| `REPLICASENSE_MODEL_COVERAGE_TOLERANCE` | `0.03` |
| `REPLICASENSE_MODEL_MIN_IMPROVEMENT` | `0.03` |
| `REPLICASENSE_MODEL_MAX_RELATIVE_UNDERPREDICTION` | `0.5` |
| `REPLICASENSE_MODEL_ROLLBACK_ENABLED` | `false` |

Invalid safety-policy values stop startup rather than weakening the gates.
Evaluation is off the inference path, serial per process, with a four-minute
deadline per workload. PostgreSQL advisory locks coordinate multiple
forecasters; transaction fences remain the final activation check. Evaluation
uses the forecaster's existing resource limits. Large GRU artifacts, longer
windows, or many workloads warrant measuring CPU and inference latency first.

## Finding out what happened

The `models.validation_metrics.promotion_v2` object contains the latest action,
reason, policy, contract, reference model ID, input fingerprint, evaluation
boundaries, and scores for every compared artifact. `evaluation_history` keeps
the latest 20 attempts per artifact; export records externally if longer audit
retention is required. Withheld evidence has a diagnostic `detail` field.

Prometheus exposes `replicasense_model_evaluation_status`,
`replicasense_model_evaluation_score`,
`replicasense_model_evaluation_decisions_total`, and
`replicasense_model_evaluation_last_unixtime`. Model IDs, timestamps, and raw
error text stay out of metric labels. A completed attempt is not necessarily
a successful promotion.
The `candidate` score role means the artifact currently being assessed; during
active-model revalidation it refers to the incumbent, not a newly trained model.
See the [dated validation report](model-promotion-validation.md) for a lab run
where both fresh candidates were correctly rejected.

Upgrade the trainer images and forecaster together. No schema migration or
model reset is required: contracts and evidence use existing JSONB columns.
Existing active artifacts are not removed. Legacy candidates without the new
contract cannot be promoted; retrain them. A legacy champion can be measured
using its conservative stored training cutoff and earn a current serving-contract
certificate only by passing the same complete fresh-observation revalidation.
Its old training metadata is not reconstructed or rewritten. Unknown-contract
legacy artifacts are not rollback targets. Never rerun the bootstrap SQL on an
existing database.
