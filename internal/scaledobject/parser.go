package scaledobject

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/identity"
)

const (
	defaultHorizon               = 10 * time.Minute
	defaultQuantile              = 0.95
	defaultTrainingWindow        = 30 * 24 * time.Hour
	defaultSamplingInterval      = time.Minute
	defaultModelEngine           = "xgboost"
	defaultMinimumTrainingWindow = 7 * 24 * time.Hour
)

// Parse builds one candidate per ReplicaSense external trigger. It performs no
// network, Kubernetes, or database access.
func Parse(input ScaledObjectInput, options ParserOptions) ParseResult {
	result := ParseResult{}
	if strings.TrimSpace(options.ClusterID) == "" {
		result.Violations = append(result.Violations, Violation{Field: "clusterID", Message: "is required"})
	}
	if strings.TrimSpace(input.Namespace) == "" {
		result.Violations = append(result.Violations, Violation{Field: "metadata.namespace", Message: "is required"})
	}
	if strings.TrimSpace(input.Name) == "" {
		result.Violations = append(result.Violations, Violation{Field: "metadata.name", Message: "is required"})
	}
	if strings.TrimSpace(input.ScaleTargetName) == "" {
		result.Violations = append(result.Violations, Violation{Field: "spec.scaleTargetRef.name", Message: "is required"})
	}

	named := make(map[string][]TriggerInput)
	for _, trigger := range input.Triggers {
		if trigger.Name != "" {
			named[trigger.Name] = append(named[trigger.Name], trigger)
		}
	}

	seenPredictiveNames := make(map[string]int)
	seenSources := make(map[string]int)
	for _, trigger := range input.Triggers {
		if !isReplicaSenseTrigger(trigger, options) {
			continue
		}
		seenPredictiveNames[trigger.Name]++
		if source := strings.TrimSpace(trigger.Metadata["sourceTrigger"]); source != "" {
			seenSources[source]++
		}
	}

	for _, trigger := range input.Triggers {
		if !isReplicaSenseTrigger(trigger, options) {
			continue
		}
		candidate := parseCandidate(input, options, trigger, named, seenPredictiveNames, seenSources)
		result.Candidates = append(result.Candidates, candidate)
	}
	return result
}

func isReplicaSenseTrigger(trigger TriggerInput, options ParserOptions) bool {
	if trigger.Type != "external" || len(options.ScalerAddresses) == 0 {
		return false
	}
	_, ok := options.ScalerAddresses[strings.TrimSpace(trigger.Metadata["scalerAddress"])]
	return ok
}

func parseCandidate(input ScaledObjectInput, options ParserOptions, predictive TriggerInput, named map[string][]TriggerInput, seen, seenSources map[string]int) Candidate {
	candidate := Candidate{PredictiveTriggerName: predictive.Name}
	add := func(field, message string) {
		candidate.Violations = append(candidate.Violations, Violation{Field: field, Message: message})
	}
	if predictive.Name == "" {
		add("triggers[].name", "is required for a ReplicaSense predictive trigger")
	}
	if seen[predictive.Name] > 1 {
		add("triggers[].name", "must be unique among ReplicaSense predictive triggers")
	}

	sourceName := strings.TrimSpace(predictive.Metadata["sourceTrigger"])
	if sourceName == "" {
		add("triggers[].metadata.sourceTrigger", "is required")
	}
	if seenSources[sourceName] > 1 {
		add("triggers[].metadata.sourceTrigger", "must be used by only one ReplicaSense predictive trigger")
	}
	sources := named[sourceName]
	if sourceName != "" && len(sources) != 1 {
		add("triggers[].metadata.sourceTrigger", "must resolve to exactly one trigger")
	}

	var source domain.PrometheusSource
	if len(sources) == 1 {
		if sources[0].Type != "prometheus" {
			add("triggers[].metadata.sourceTrigger", "must reference a prometheus trigger")
		} else {
			source = parsePrometheusSource(sources[0], add)
		}
	}

	forecast := parseForecastConfig(predictive, options, add)
	bounds := parseBounds(input, options, add)
	if strings.TrimSpace(options.ClusterID) == "" || strings.TrimSpace(input.Namespace) == "" || strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.ScaleTargetName) == "" {
		add("scaledObject", "has required top-level fields missing")
	}
	if len(candidate.Violations) > 0 {
		return candidate
	}

	key := domain.WorkloadKey{
		ClusterID:             options.ClusterID,
		Namespace:             input.Namespace,
		ScaledObjectName:      input.Name,
		PredictiveTriggerName: predictive.Name,
	}
	sourceFingerprint, err := identity.Fingerprint(struct {
		ServerAddress string `json:"serverAddress"`
		Query         string `json:"query"`
	}{source.ServerAddress, source.Query})
	if err != nil {
		add("sourceFingerprint", err.Error())
		return candidate
	}
	policyRevision, err := identity.Fingerprint(struct {
		SourceFingerprint string                `json:"sourceFingerprint"`
		Threshold         float64               `json:"threshold"`
		Bounds            domain.ReplicaBounds  `json:"bounds"`
		Forecast          domain.ForecastConfig `json:"forecast"`
	}{sourceFingerprint, source.Threshold, bounds, forecast})
	if err != nil {
		add("policyRevision", err.Error())
		return candidate
	}

	candidate.Spec = &domain.WorkloadSpec{
		Key:               key,
		ScaleTarget:       domain.ScaleTarget{Name: input.ScaleTargetName},
		Bounds:            bounds,
		Source:            source,
		Forecast:          forecast,
		SourceFingerprint: sourceFingerprint,
		PolicyRevision:    policyRevision,
	}
	return candidate
}

func parsePrometheusSource(trigger TriggerInput, add func(string, string)) domain.PrometheusSource {
	serverAddress := strings.TrimSpace(trigger.Metadata["serverAddress"])
	if serverAddress == "" {
		add("source.metadata.serverAddress", "is required")
	} else if parsed, err := url.ParseRequestURI(serverAddress); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		add("source.metadata.serverAddress", "must be an absolute http(s) URL without credentials or fragment")
	}
	query := strings.TrimSpace(trigger.Metadata["query"])
	if query == "" {
		add("source.metadata.query", "is required")
	}
	threshold, err := strconv.ParseFloat(strings.TrimSpace(trigger.Metadata["threshold"]), 64)
	if err != nil || threshold <= 0 {
		add("source.metadata.threshold", "must be a positive number")
	}
	return domain.PrometheusSource{TriggerName: trigger.Name, ServerAddress: serverAddress, Query: query, Threshold: threshold}
}

func parseForecastConfig(trigger TriggerInput, options ParserOptions, add func(string, string)) domain.ForecastConfig {
	horizon := parseDuration(trigger.Metadata["forecastHorizon"], defaultHorizon, "metadata.forecastHorizon", add)
	trainingWindow := parseDuration(trigger.Metadata["trainingWindow"], defaultTrainingWindow, "metadata.trainingWindow", add)
	samplingInterval := parseDuration(trigger.Metadata["samplingInterval"], defaultSamplingInterval, "metadata.samplingInterval", add)
	startupLatency := parseNonNegativeDuration(trigger.Metadata["startupLatency"], "metadata.startupLatency", add)
	safetyBuffer := parseNonNegativeDuration(trigger.Metadata["safetyBuffer"], "metadata.safetyBuffer", add)
	immediateTraining := parseBool(trigger.Metadata["immediateTraining"], "metadata.immediateTraining", add)
	clearOldModels := parseBool(trigger.Metadata["clearOldModels"], "metadata.clearOldModels", add)
	quantile := parseQuantile(trigger.Metadata["quantile"], add)
	engine := strings.TrimSpace(trigger.Metadata["modelEngine"])
	if engine == "" {
		engine = defaultModelEngine
	}
	if engine != defaultModelEngine && engine != "seasonal-baseline" && engine != "rolling-quantile" && engine != "holt-winters" && engine != "xgboost" && engine != "gru" {
		add("metadata.modelEngine", fmt.Sprintf("unsupported model engine %q", engine))
	}
	minimumTrainingWindow := options.MinimumTrainingWindow
	if minimumTrainingWindow <= 0 {
		minimumTrainingWindow = defaultMinimumTrainingWindow
	}
	if trainingWindow < minimumTrainingWindow || trainingWindow > 90*24*time.Hour {
		add("metadata.trainingWindow", fmt.Sprintf("must be between %s and 90d", formatTrainingWindow(minimumTrainingWindow)))
	}
	if trainingWindow%time.Second != 0 {
		add("metadata.trainingWindow", "must be an exact number of seconds")
	}
	if samplingInterval%time.Second != 0 {
		add("metadata.samplingInterval", "must be an exact number of seconds")
	}
	decisionHorizon := horizon + startupLatency + safetyBuffer
	if samplingInterval <= 0 || decisionHorizon <= 0 || decisionHorizon%samplingInterval != 0 {
		add("metadata.forecastHorizon", "plus startupLatency and safetyBuffer must be a positive multiple of samplingInterval")
	}
	if horizon%time.Second != 0 {
		add("metadata.forecastHorizon", "must be an exact number of seconds")
	}
	timezone := strings.TrimSpace(trigger.Metadata["businessTimezone"])
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		add("metadata.businessTimezone", "must be a valid IANA timezone, for example Europe/Istanbul")
	}
	return domain.ForecastConfig{Horizon: horizon, Quantile: quantile, TrainingWindow: trainingWindow, SamplingInterval: samplingInterval, ModelEngine: engine, BusinessTimezone: timezone, StartupLatency: startupLatency, SafetyBuffer: safetyBuffer, ImmediateTraining: immediateTraining, ClearOldModels: clearOldModels}
}

func formatTrainingWindow(value time.Duration) string {
	if value%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", value/(24*time.Hour))
	}
	return value.String()
}

func parseBool(raw, field string, add func(string, string)) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		add(field, "must be true or false")
	}
	return value
}

func parseNonNegativeDuration(raw, field string, add func(string, string)) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	value, err := parseReplicaSenseDuration(raw)
	if err != nil || value < 0 {
		add(field, "must be a non-negative duration, for example 45s or 2m")
		return 0
	}
	return value
}

func parseDuration(raw string, defaultValue time.Duration, field string, add func(string, string)) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return defaultValue
	}
	value, err := parseReplicaSenseDuration(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		add(field, "must be a positive duration, for example 10m or 30d")
		return defaultValue
	}
	return value
}

// parseReplicaSenseDuration extends time.ParseDuration with whole-day values.
// Go intentionally does not define a day because calendar days vary, while our
// training and sampling windows are fixed 24-hour periods.
func parseReplicaSenseDuration(raw string) (time.Duration, error) {
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.ParseInt(strings.TrimSuffix(raw, "d"), 10, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(raw)
}

func parseQuantile(raw string, add func(string, string)) float64 {
	if strings.TrimSpace(raw) == "" {
		return defaultQuantile
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 || value >= 1 {
		add("metadata.quantile", "must be greater than 0 and less than 1")
		return defaultQuantile
	}
	return value
}

func parseBounds(input ScaledObjectInput, options ParserOptions, add func(string, string)) domain.ReplicaBounds {
	min, max := options.DefaultMinReplicaCount, options.DefaultMaxReplicaCount
	if input.MinReplicaCount != nil {
		min = *input.MinReplicaCount
	}
	if input.MaxReplicaCount != nil {
		max = *input.MaxReplicaCount
	}
	if min < 0 {
		add("spec.minReplicaCount", "must not be negative")
	}
	if max <= 0 {
		add("spec.maxReplicaCount", "must be positive")
	}
	if min > max {
		add("spec", "minReplicaCount must not exceed maxReplicaCount")
	}
	return domain.ReplicaBounds{Min: min, Max: max}
}
