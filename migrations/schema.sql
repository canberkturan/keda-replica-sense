-- ReplicaSense bootstrap schema for a new, empty PostgreSQL database.
-- Apply exactly once with the database-owner role before Helm installation.
-- This intentionally has no destructive statements and is not an upgrade
-- migration for databases created by prior releases.

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE workloads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id TEXT NOT NULL,
    namespace TEXT NOT NULL,
    scaled_object_name TEXT NOT NULL,
    predictive_trigger_name TEXT NOT NULL,
    scaled_object_uid TEXT NOT NULL,
    observed_generation BIGINT NOT NULL,
    scale_target_name TEXT NOT NULL,
    min_replica_count INTEGER NOT NULL,
    max_replica_count INTEGER NOT NULL,
    source_trigger_name TEXT NOT NULL,
    prometheus_server_address TEXT NOT NULL,
    prometheus_query TEXT NOT NULL,
    reactive_threshold NUMERIC NOT NULL,
    source_fingerprint CHAR(64) NOT NULL,
    forecast_horizon_seconds BIGINT NOT NULL,
    forecast_quantile NUMERIC(6,5) NOT NULL,
    training_window_seconds BIGINT NOT NULL,
    sampling_interval_seconds BIGINT NOT NULL,
    model_engine TEXT NOT NULL,
    business_timezone TEXT NOT NULL DEFAULT 'UTC',
    startup_latency_seconds BIGINT NOT NULL DEFAULT 0,
    safety_buffer_seconds BIGINT NOT NULL DEFAULT 0,
    policy_revision CHAR(64) NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    validation_errors JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deactivated_at TIMESTAMPTZ,
    CONSTRAINT workloads_logical_identity_unique
        UNIQUE (cluster_id, namespace, scaled_object_name, predictive_trigger_name),
    CONSTRAINT workloads_replica_bounds_valid
        CHECK (min_replica_count >= 0 AND max_replica_count > 0 AND min_replica_count <= max_replica_count),
    CONSTRAINT workloads_durations_positive
        CHECK (forecast_horizon_seconds > 0 AND training_window_seconds > 0 AND sampling_interval_seconds > 0),
    CONSTRAINT workloads_horizon_matches_sampling_interval
        CHECK (forecast_horizon_seconds % sampling_interval_seconds = 0),
    CONSTRAINT workloads_quantile_valid
        CHECK (forecast_quantile > 0 AND forecast_quantile < 1),
    CONSTRAINT workloads_business_timezone_nonempty
        CHECK (length(trim(business_timezone)) > 0),
    CONSTRAINT workloads_decision_horizon_nonnegative
        CHECK (startup_latency_seconds >= 0 AND safety_buffer_seconds >= 0),
    CONSTRAINT workloads_status_valid
        CHECK (status IN ('active', 'invalid', 'inactive')),
    CONSTRAINT workloads_fingerprint_format
        CHECK (source_fingerprint ~ '^[0-9a-f]{64}$' AND policy_revision ~ '^[0-9a-f]{64}$')
);

CREATE INDEX workloads_active_cluster_idx
    ON workloads (cluster_id, status) WHERE status = 'active';

CREATE FUNCTION set_workloads_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER workloads_set_updated_at
    BEFORE UPDATE ON workloads
    FOR EACH ROW EXECUTE FUNCTION set_workloads_updated_at();

CREATE TABLE samples (
    cluster_id TEXT NOT NULL,
    workload_id UUID NOT NULL REFERENCES workloads(id),
    source_fingerprint CHAR(64) NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    observed_value DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT samples_identity_unique
        UNIQUE (cluster_id, workload_id, source_fingerprint, observed_at),
    CONSTRAINT samples_fingerprint_format
        CHECK (source_fingerprint ~ '^[0-9a-f]{64}$'),
    CONSTRAINT samples_observed_value_finite
        CHECK (observed_value <> 'NaN'::double precision AND observed_value <> 'Infinity'::double precision AND observed_value <> '-Infinity'::double precision)
);

CREATE INDEX samples_workload_observed_at_idx
    ON samples (cluster_id, workload_id, observed_at DESC);

CREATE TABLE forecast_snapshots (
    id BIGSERIAL PRIMARY KEY,
    cluster_id TEXT NOT NULL,
    workload_id UUID NOT NULL REFERENCES workloads(id),
    source_fingerprint CHAR(64) NOT NULL,
    generated_at TIMESTAMPTZ NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    model_engine TEXT NOT NULL,
    forecast_horizon_seconds BIGINT NOT NULL CHECK (forecast_horizon_seconds > 0),
    forecast_p50 DOUBLE PRECISION NOT NULL CHECK (forecast_p50 >= 0 AND forecast_p50 <> 'NaN'::double precision AND forecast_p50 <> 'Infinity'::double precision),
    forecast_p95 DOUBLE PRECISION NOT NULL CHECK (forecast_p95 >= 0 AND forecast_p95 <> 'NaN'::double precision AND forecast_p95 <> 'Infinity'::double precision),
    surge_demand DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (surge_demand >= 0),
    safe_demand DOUBLE PRECISION NOT NULL CHECK (safe_demand >= 0),
    safety_reason TEXT NOT NULL DEFAULT '',
    realized_horizon_max DOUBLE PRECISION,
    evaluated_at TIMESTAMPTZ,
    covered BOOLEAN,
    underpredicted BOOLEAN,
    absolute_error DOUBLE PRECISION,
    UNIQUE (cluster_id, workload_id, source_fingerprint, generated_at)
);

CREATE INDEX forecast_snapshots_latest_idx
    ON forecast_snapshots (cluster_id, workload_id, generated_at DESC);

CREATE TABLE models (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id TEXT NOT NULL,
    workload_id UUID NOT NULL REFERENCES workloads(id),
    source_fingerprint CHAR(64) NOT NULL,
    engine TEXT NOT NULL,
    engine_version TEXT NOT NULL DEFAULT '',
    feature_schema_version TEXT NOT NULL,
    training_window_start TIMESTAMPTZ NOT NULL,
    training_window_end TIMESTAMPTZ NOT NULL,
    dataset_fingerprint CHAR(64) NOT NULL,
    hyperparameters JSONB NOT NULL DEFAULT '{}'::jsonb,
    validation_metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
    artifact_data BYTEA NOT NULL,
    artifact_sha256 CHAR(64) NOT NULL,
    status TEXT NOT NULL DEFAULT 'candidate',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at TIMESTAMPTZ,
    retired_at TIMESTAMPTZ,
    CONSTRAINT models_status_valid CHECK (status IN ('candidate', 'active', 'retired')),
    CONSTRAINT models_window_valid CHECK (training_window_end > training_window_start),
    CONSTRAINT models_fingerprints_valid CHECK (source_fingerprint ~ '^[0-9a-f]{64}$' AND dataset_fingerprint ~ '^[0-9a-f]{64}$' AND artifact_sha256 ~ '^[0-9a-f]{64}$')
);

CREATE UNIQUE INDEX models_one_active_per_workload
    ON models (cluster_id, workload_id, source_fingerprint) WHERE status = 'active';
CREATE INDEX models_active_lookup_idx
    ON models (cluster_id, workload_id, created_at DESC);

CREATE TABLE training_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id TEXT NOT NULL,
    workload_id UUID NOT NULL REFERENCES workloads(id),
    training_slot TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'claimed',
    engine TEXT NOT NULL,
    training_window_start TIMESTAMPTZ NOT NULL,
    training_window_end TIMESTAMPTZ NOT NULL,
    dataset_size BIGINT,
    validation_metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
    model_id UUID REFERENCES models(id),
    failure_reason TEXT,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT training_runs_status_valid CHECK (status IN ('claimed', 'running', 'succeeded', 'failed')),
    CONSTRAINT training_runs_window_valid CHECK (training_window_end > training_window_start),
    CONSTRAINT training_runs_one_claim_per_slot UNIQUE (cluster_id, workload_id, training_slot)
);

CREATE INDEX training_runs_due_idx
    ON training_runs (cluster_id, status, training_slot DESC);

COMMIT;
