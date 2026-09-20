#!/usr/bin/env python3
"""CPU-only, temporal-validation PyTorch GRU Trainer for ReplicaSense.

The trainer intentionally exports JSON tensors instead of pickle or TorchScript.
The always-on Go forecaster validates and evaluates those tensors itself.
"""

import hashlib
import json
import math
import os
import sys
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from zoneinfo import ZoneInfo

import psycopg
from psycopg.rows import dict_row
import torch
from torch import nn

SEQUENCE = 120
INPUTS = 10
HIDDEN = 32
EPOCHS = 24
BATCH = 128
FORMAT = "replicasense-pytorch-gru-v1"
FEATURE_SCHEMA = "demand-calendar-sequence-pytorch-v1"


@dataclass
class Contract:
    cluster_id: str
    workload_id: str
    run_id: str
    policy_revision: str
    source_fingerprint: str
    horizon_seconds: int
    sampling_seconds: int
    training_window_seconds: int
    quantile: float
    timezone_name: str


class QuantileGRU(nn.Module):
    def __init__(self):
        super().__init__()
        self.gru = nn.GRU(INPUTS, HIDDEN, batch_first=True)
        self.head = nn.Linear(HIDDEN, 1)

    def forward(self, values):
        output, _ = self.gru(values)
        return self.head(output[:, -1, :]).squeeze(1)


def require(name):
    value = os.getenv(name, "").strip()
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


def pinball(prediction, target, quantile):
    residual = target - prediction
    return torch.maximum(quantile * residual, (quantile - 1.0) * residual).mean()


def normalizer(values):
    low, high = min(values), max(values)
    return low, max(high - low, 1.0)


def feature(timestamp, value, minimum, scale, location):
    local = timestamp.astimezone(location)
    minute = (local.hour * 60 + local.minute) / 1440.0
    weekday = local.weekday() / 7.0
    days = (datetime(local.year + (local.month == 12), local.month % 12 + 1, 1, tzinfo=location) - timedelta(days=1)).day
    day = (local.day - 1) / days
    month = (local.month - 1) / 12.0
    weekend = 1.0 if local.weekday() >= 5 else 0.0
    return [
        (value - minimum) / scale,
        math.sin(2 * math.pi * minute), math.cos(2 * math.pi * minute),
        math.sin(2 * math.pi * weekday), math.cos(2 * math.pi * weekday),
        math.sin(2 * math.pi * day), math.cos(2 * math.pi * day),
        math.sin(2 * math.pi * month), math.cos(2 * math.pi * month), weekend,
    ]


def make_examples(samples, steps, minimum, scale, location, end_limit, maximum=4096):
    # end is the last observable sample; labels are future horizon maxima.
    last = min(end_limit, len(samples) - steps - 1)
    indexes = list(range(SEQUENCE - 1, last + 1))
    if len(indexes) > maximum:
        indexes = [indexes[index * (len(indexes) - 1) // (maximum - 1)] for index in range(maximum)]
    features, targets = [], []
    for end in indexes:
        features.append([feature(point[0], point[1], minimum, scale, location) for point in samples[end - SEQUENCE + 1:end + 1]])
        targets.append((max(value for _, value in samples[end + 1:end + steps + 1]) - minimum) / scale)
    return torch.tensor(features, dtype=torch.float32), torch.tensor(targets, dtype=torch.float32), indexes


def fit(features, targets, quantile, seed):
    torch.manual_seed(seed)
    model = QuantileGRU()
    optimizer = torch.optim.AdamW(model.parameters(), lr=0.001, weight_decay=0.0001)
    generator = torch.Generator().manual_seed(seed)
    for _ in range(EPOCHS):
        order = torch.randperm(len(features), generator=generator)
        for start in range(0, len(order), BATCH):
            rows = order[start:start + BATCH]
            prediction = model(features[rows])
            loss = pinball(prediction, targets[rows], quantile)
            optimizer.zero_grad(set_to_none=True)
            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), 1.0)
            optimizer.step()
    model.eval()
    return model


def predict(model, samples, end, minimum, scale, location):
    values = [[feature(point[0], point[1], minimum, scale, location) for point in samples[end - SEQUENCE + 1:end + 1]]]
    with torch.no_grad():
        return float(model(torch.tensor(values, dtype=torch.float32))[0]) * scale + minimum


def residual_quantile(model, samples, indexes, steps, minimum, scale, location, quantile):
    residuals = [max(value for _, value in samples[end + 1:end + steps + 1]) - predict(model, samples, end, minimum, scale, location) for end in indexes]
    residuals.sort()
    return residuals[min(len(residuals) - 1, math.ceil(quantile * len(residuals)) - 1)]


def validation(samples, steps, sampling_seconds, minimum, scale, location, quantile):
    # Train / calibration / test are contiguous temporal partitions. The test
    # period is never used for fitting or conformal residual calibration.
    train_end = int(len(samples) * 0.80) - steps - 1
    calibration_end = int(len(samples) * 0.90) - steps - 1
    p50_features, p50_targets, _ = make_examples(samples, steps, minimum, scale, location, train_end)
    p95_features, p95_targets, _ = make_examples(samples, steps, minimum, scale, location, train_end)
    p50 = fit(p50_features, p50_targets, 0.50, 20260920)
    p95 = fit(p95_features, p95_targets, quantile, 20260921)
    calibration_indexes = list(range(max(SEQUENCE - 1, train_end + 1), calibration_end + 1, max(1, steps)))
    offset = residual_quantile(p95, samples, calibration_indexes, steps, minimum, scale, location, quantile)
    test_indexes = list(range(max(SEQUENCE - 1, calibration_end + 1), len(samples) - steps, max(1, steps)))
    if len(test_indexes) > 288:
        test_indexes = test_indexes[-288:]
    if not test_indexes:
        raise RuntimeError("insufficient temporal validation windows")
    coverage = under = mae = pinball_loss = 0.0
    seasonal_coverage = seasonal_under = seasonal_mae = seasonal_pinball = 0.0
    season = max(1, int(os.getenv("REPLICASENSE_SEASONAL_PERIOD_SECONDS", "86400")) // sampling_seconds)
    for end in test_indexes:
        actual = max(value for _, value in samples[end + 1:end + steps + 1])
        upper = max(predict(p50, samples, end, minimum, scale, location), predict(p95, samples, end, minimum, scale, location) + offset)
        coverage += upper >= actual
        under += upper < actual
        mae += abs(upper - actual)
        pinball_loss += quantile * (actual - upper) if actual >= upper else (1 - quantile) * (upper - actual)
        seasonal = max(value for _, value in samples[end + 1 - season:end + steps + 1 - season])
        seasonal_coverage += seasonal >= actual
        seasonal_under += seasonal < actual
        seasonal_mae += abs(seasonal - actual)
        seasonal_pinball += quantile * (actual - seasonal) if actual >= seasonal else (1 - quantile) * (seasonal - actual)
    count = len(test_indexes)
    return ({"windows": count, "coverage": coverage / count, "underprediction_rate": under / count, "mean_absolute_error": mae / count, "pinball_loss_p95": pinball_loss / count}, {"windows": count, "coverage": seasonal_coverage / count, "underprediction_rate": seasonal_under / count, "mean_absolute_error": seasonal_mae / count, "pinball_loss_p95": seasonal_pinball / count}, p50, p95, offset)


def tensor_data(model):
    state = model.state_dict()
    return {
        "weight_ih_l0": state["gru.weight_ih_l0"].flatten().tolist(),
        "weight_hh_l0": state["gru.weight_hh_l0"].flatten().tolist(),
        "bias_ih_l0": state["gru.bias_ih_l0"].flatten().tolist(),
        "bias_hh_l0": state["gru.bias_hh_l0"].flatten().tolist(),
        "head_weight": state["head.weight"].flatten().tolist(),
        "head_bias": float(state["head.bias"].item()),
    }


def active_validation(cursor, contract):
    cursor.execute("SELECT validation_metrics->'walk_forward' AS validation FROM models WHERE cluster_id=%s AND workload_id=%s AND source_fingerprint=%s AND status='active'", (contract.cluster_id, contract.workload_id, contract.source_fingerprint))
    row = cursor.fetchone()
    return row["validation"] if row else None


def promote(candidate, reference):
    if candidate["coverage"] < 0.93:
        return False, f"coverage_below_minimum: {candidate['coverage']:.4f} < 0.9300", ""
    if candidate["underprediction_rate"] > 0.07:
        return False, f"underprediction_above_maximum: {candidate['underprediction_rate']:.4f} > 0.0700", ""
    name = "active_champion" if reference else "deterministic_baseline"
    reference = reference or candidate["deterministic_baseline"]
    if candidate["underprediction_rate"] > reference["underprediction_rate"] or candidate["pinball_loss_p95"] > reference["pinball_loss_p95"] or (candidate["pinball_loss_p95"] == reference["pinball_loss_p95"] and candidate["mean_absolute_error"] > reference["mean_absolute_error"]):
        return False, f"not_better_than_{name}", name
    return True, "promotion_policy_satisfied", name


def main():
    contract = Contract(require("REPLICASENSE_CLUSTER_ID"), require("REPLICASENSE_WORKLOAD_ID"), require("REPLICASENSE_TRAINING_RUN_ID"), require("REPLICASENSE_POLICY_REVISION"), "", 0, 0, 0, 0, "")
    if os.getenv("REPLICASENSE_MODEL_ENGINE", "gru") != "gru":
        raise RuntimeError("pytorch trainer only supports the gru engine")
    torch.set_num_threads(max(1, min(2, int(os.getenv("REPLICASENSE_PYTORCH_THREADS", "2")))))
    with psycopg.connect(require("REPLICASENSE_DATABASE_URL"), row_factory=dict_row) as connection:
        with connection.cursor() as cursor:
            cursor.execute("UPDATE training_runs SET status='running', started_at=now(), failure_reason=NULL WHERE id=%s AND status='claimed'", (contract.run_id,))
            cursor.execute("SELECT source_fingerprint, forecast_horizon_seconds, sampling_interval_seconds, training_window_seconds, forecast_quantile, business_timezone, policy_revision FROM workloads WHERE id=%s AND cluster_id=%s AND status='active'", (contract.workload_id, contract.cluster_id))
            workload = cursor.fetchone()
            if not workload or workload["policy_revision"] != contract.policy_revision:
                raise RuntimeError("workload is inactive or its policy revision changed")
            contract.source_fingerprint, contract.horizon_seconds, contract.sampling_seconds, contract.training_window_seconds, contract.quantile, contract.timezone_name = workload["source_fingerprint"], workload["forecast_horizon_seconds"], workload["sampling_interval_seconds"], workload["training_window_seconds"], float(workload["forecast_quantile"]), workload["business_timezone"]
            end = datetime.now(timezone.utc).replace(second=0, microsecond=0)
            start = end - timedelta(seconds=contract.training_window_seconds)
            cursor.execute("SELECT observed_at, observed_value FROM samples WHERE cluster_id=%s AND workload_id=%s AND source_fingerprint=%s AND observed_at >= %s AND observed_at <= %s ORDER BY observed_at", (contract.cluster_id, contract.workload_id, contract.source_fingerprint, start, end))
            samples = [(row["observed_at"], float(row["observed_value"])) for row in cursor.fetchall()]
            steps = contract.horizon_seconds // contract.sampling_seconds
            seasonal_seconds = int(os.getenv("REPLICASENSE_SEASONAL_PERIOD_SECONDS", "86400"))
            if seasonal_seconds <= 0:
                raise RuntimeError("REPLICASENSE_SEASONAL_PERIOD_SECONDS must be positive")
            if steps < 1 or len(samples) < max(SEQUENCE + steps + 288, seasonal_seconds // contract.sampling_seconds):
                raise RuntimeError("insufficient samples for PyTorch GRU training")
            max_gap_intervals = int(os.getenv("REPLICASENSE_MAX_GAP_INTERVALS", "2"))
            if max_gap_intervals <= 0:
                raise RuntimeError("REPLICASENSE_MAX_GAP_INTERVALS must be positive")
            for previous, current in zip(samples, samples[1:]):
                if (current[0] - previous[0]).total_seconds() > contract.sampling_seconds * max_gap_intervals:
                    raise RuntimeError(f"training data has a gap larger than {max_gap_intervals} sampling intervals")
            location = ZoneInfo(contract.timezone_name)
            values = [value for _, value in samples]
            minimum, scale = normalizer(values)
            walk_forward, baseline, p50, p95, offset = validation(samples, steps, contract.sampling_seconds, minimum, scale, location, contract.quantile)
            # Store exactly the models and conformal adjustment that completed
            # the temporal holdout. The test period was never used to fit or
            # calibrate either output head.
            artifact = {"engine": "gru", "format": FORMAT, "feature_schema": FEATURE_SCHEMA, "upper_quantile": contract.quantile, "sequence_length": SEQUENCE, "input_size": INPUTS, "hidden_size": HIDDEN, "value_minimum": minimum, "value_scale": scale, "p95_calibration": offset, "p50": tensor_data(p50), "p95": tensor_data(p95)}
            champion = active_validation(cursor, contract)
            candidate = dict(walk_forward)
            candidate["deterministic_baseline"] = baseline
            accepted, reason, reference = promote(candidate, champion)
            metrics = {"sample_count": len(samples), "baseline": False, "operational_quantile": contract.quantile, "walk_forward": walk_forward, "deterministic_baseline": baseline, "promotion": {"promote": accepted, "reason": reason, "reference": reference}, "trainer": {"implementation": "pytorch-cpu", "torch_version": torch.__version__, "sequence_length": SEQUENCE, "hidden_size": HIDDEN}}
            encoded = json.dumps(artifact, separators=(",", ":")).encode()
            digest = hashlib.sha256(encoded).hexdigest()
            data_digest = hashlib.sha256("".join(f"{point.isoformat()}:{value}\n" for point, value in samples).encode()).hexdigest()
            cursor.execute("INSERT INTO models (cluster_id, workload_id, source_fingerprint, engine, engine_version, feature_schema_version, training_window_start, training_window_end, dataset_fingerprint, hyperparameters, validation_metrics, artifact_data, artifact_sha256, status) VALUES (%s,%s,%s,'gru','pytorch-cpu',%s,%s,%s,%s,%s::jsonb,%s::jsonb,%s,%s,'candidate') RETURNING id", (contract.cluster_id, contract.workload_id, contract.source_fingerprint, FEATURE_SCHEMA, start, end, data_digest, json.dumps({"epochs": EPOCHS, "hidden_size": HIDDEN, "sequence_length": SEQUENCE}), json.dumps(metrics), encoded, digest))
            model_id = cursor.fetchone()["id"]
            if accepted:
                cursor.execute("UPDATE models SET status='retired', retired_at=now() WHERE cluster_id=%s AND workload_id=%s AND source_fingerprint=%s AND status='active'", (contract.cluster_id, contract.workload_id, contract.source_fingerprint))
                cursor.execute("UPDATE models SET status='active', activated_at=now() WHERE id=%s", (model_id,))
            cursor.execute("UPDATE training_runs SET status='succeeded', model_id=%s, dataset_size=%s, validation_metrics=%s::jsonb, finished_at=now() WHERE id=%s", (model_id, len(samples), json.dumps(metrics), contract.run_id))
        connection.commit()
    print(json.dumps({"event": "pytorch_gru_training_completed", "model_id": str(model_id), "promoted": accepted, "promotion_reason": reason, "validation_windows": walk_forward["windows"], "coverage": walk_forward["coverage"], "underprediction_rate": walk_forward["underprediction_rate"]}, separators=(",", ":")))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"train: {error}", file=sys.stderr)
        database = os.getenv("REPLICASENSE_DATABASE_URL")
        run_id = os.getenv("REPLICASENSE_TRAINING_RUN_ID")
        if database and run_id:
            try:
                with psycopg.connect(database) as connection:
                    with connection.cursor() as cursor:
                        cursor.execute("UPDATE training_runs SET status='failed', failure_reason=%s, finished_at=now() WHERE id=%s", (str(error)[:2048], run_id))
                    connection.commit()
            except Exception:
                pass
        sys.exit(1)
