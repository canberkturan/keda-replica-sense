package training

import (
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"math"
)

type PromotionDecision struct {
	Promote   bool   `json:"promote"`
	Reason    string `json:"reason"`
	Reference string `json:"reference"`
}

func validMetrics(m forecast.ValidationMetrics) bool {
	return m.Windows > 0 && m.Coverage >= 0 && m.Coverage <= 1 && m.UnderpredictionRate >= 0 && m.UnderpredictionRate <= 1 && math.Abs(m.Coverage+m.UnderpredictionRate-1) < 1e-9 && finite(m.MeanAbsoluteError) && m.MeanAbsoluteError >= 0 && finite(m.PinballLossP95) && m.PinballLossP95 >= 0
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
