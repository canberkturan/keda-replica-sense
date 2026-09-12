//go:build !xgboost

package forecast

import (
	"fmt"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// TrainXGBoost is intentionally unavailable in the portable image. Operators
// must select the explicitly native-enabled image for this engine.
func TrainXGBoost([]domain.Sample, Request) (Model, []byte, error) {
	return nil, nil, fmt.Errorf("xgboost is unavailable: use an image built with the xgboost tag")
}

func ValidateXGBoost([]domain.Sample, Request, int) (ValidationMetrics, error) {
	return ValidationMetrics{}, fmt.Errorf("xgboost is unavailable: use an image built with the xgboost tag")
}

func loadXGBoostArtifact(id string, artifact []byte) (Model, error) {
	return nil, fmt.Errorf("xgboost model %s cannot load: this image lacks native xgboost support", id)
}

var _ = time.Second
