package forecast

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

type StoredModel struct {
	ID                string
	WorkloadID        string
	SourceFingerprint string
	Engine            string
	Artifact          []byte
	ActivatedAt       time.Time
}

type ModelArtifact struct {
	Engine string `json:"engine"`
}

// Prediction represents a horizon-maximum demand distribution. P95 remains a
// compatibility field name for the configured upper operational quantile (not
// necessarily literal Q0.95). The baseline has one deterministic estimate, so
// its P50 and P95 are equal.
type Prediction struct {
	P50 float64
	P95 float64
}

// Model is the inference boundary used by the forecaster. Training and model
// storage can evolve independently as long as they produce this interface.
type Model interface {
	Engine() string
	PredictSamples([]domain.Sample, Request) (Prediction, error)
}

// UpperQuantileModel exposes the training quantile of a statistical artifact.
// Model caches use it to prevent a valid-but-differently-configured artifact
// from being served after a workload quantile change.
type UpperQuantileModel interface {
	OperationalQuantile() float64
}

type SeasonalBaseline struct{}

func (SeasonalBaseline) Engine() string { return "seasonal-baseline" }

func (SeasonalBaseline) PredictSamples(samples []domain.Sample, request Request) (Prediction, error) {
	value, err := (SeasonalNaive{}).PredictSamples(samples, request)
	if err != nil {
		return Prediction{}, err
	}
	return Prediction{P50: value, P95: value}, nil
}

func ResolveModel(engine string) (Model, error) {
	switch strings.TrimSpace(engine) {
	case "", "seasonal-baseline":
		return SeasonalBaseline{}, nil
	case "rolling-quantile":
		return RollingQuantile{}, nil
	case "holt-winters":
		return HoltWinters{}, nil
	case "xgboost":
		return nil, fmt.Errorf("xgboost requires a trained native artifact")
	case "gru":
		return nil, fmt.Errorf("gru requires a trained artifact")
	default:
		return nil, fmt.Errorf("unsupported model engine %q", engine)
	}
}

// LoadModel validates a stored native artifact before exposing a model for
// inference. No deserialization mechanism that can execute arbitrary code is
// used (in particular, never Python pickle).
func LoadModel(stored StoredModel) (Model, error) {
	var artifact ModelArtifact
	if err := json.Unmarshal(stored.Artifact, &artifact); err != nil {
		return nil, fmt.Errorf("decode model artifact %s: %w", stored.ID, err)
	}
	if artifact.Engine != stored.Engine {
		return nil, fmt.Errorf("model artifact engine %q does not match stored engine %q", artifact.Engine, stored.Engine)
	}
	if stored.Engine == "xgboost" {
		return loadXGBoostArtifact(stored.ID, stored.Artifact)
	}
	if stored.Engine == "gru" {
		return loadGRUArtifact(stored.ID, stored.Artifact)
	}
	return ResolveModel(stored.Engine)
}
