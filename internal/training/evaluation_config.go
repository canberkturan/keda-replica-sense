package training

import (
	"fmt"
	"strconv"
	"time"
)

// EvaluationPolicyFromEnvironment rejects invalid safety configuration instead
// of silently weakening it. The same policy serves every trainer implementation.
func EvaluationPolicyFromEnvironment(lookup func(string) string) (EvaluationPolicy, error) {
	p := DefaultEvaluationPolicy()
	if mode := lookup("REPLICASENSE_MODEL_PROMOTION_MODE"); mode != "" {
		p.Mode = mode
	}
	for name, dst := range map[string]*time.Duration{"REPLICASENSE_MODEL_EVALUATION_WINDOW": &p.Window, "REPLICASENSE_MODEL_EVALUATION_MIN_DURATION": &p.MinDuration} {
		if raw := lookup(name); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil {
				return p, fmt.Errorf("%s: %w", name, err)
			}
			*dst = value
		}
	}
	for name, dst := range map[string]*int{"REPLICASENSE_MODEL_EVALUATION_MIN_WINDOWS": &p.MinWindows, "REPLICASENSE_MODEL_EVALUATION_MAX_WINDOWS": &p.MaxWindows} {
		if raw := lookup(name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				return p, fmt.Errorf("%s: %w", name, err)
			}
			*dst = value
		}
	}
	for name, dst := range map[string]*float64{"REPLICASENSE_MODEL_COVERAGE_TOLERANCE": &p.CoverageTolerance, "REPLICASENSE_MODEL_MIN_IMPROVEMENT": &p.MinimumImprovement, "REPLICASENSE_MODEL_MAX_RELATIVE_UNDERPREDICTION": &p.MaxRelativeUnderprediction} {
		if raw := lookup(name); raw != "" {
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return p, fmt.Errorf("%s: %w", name, err)
			}
			*dst = value
		}
	}
	if raw := lookup("REPLICASENSE_MODEL_ROLLBACK_ENABLED"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return p, err
		}
		p.Rollback = value
	}
	return p, p.Validate()
}
