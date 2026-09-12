package scaledobject

import (
	"testing"
)

func TestParse(t *testing.T) {
	options := ParserOptions{
		ClusterID:              "prod-openshift-01",
		ScalerAddresses:        map[string]struct{}{"replicasense.default.svc:6000": {}},
		DefaultMinReplicaCount: 0,
		DefaultMaxReplicaCount: 100,
	}
	valid := func() ScaledObjectInput {
		min, max := int32(3), int32(50)
		return ScaledObjectInput{
			Namespace:       "default",
			Name:            "payments-api",
			UID:             "uid-1",
			Generation:      1,
			ScaleTargetName: "payments-api",
			MinReplicaCount: &min,
			MaxReplicaCount: &max,
			Triggers: []TriggerInput{
				{Type: "prometheus", Name: "reactive", Metadata: map[string]string{
					"serverAddress": "http://prometheus:9090",
					"query":         "sum(rate(http_requests_total{app=\"payments-api\"}[1m]))",
					"threshold":     "100",
				}},
				{Type: "external", Name: "predictive", Metadata: map[string]string{
					"scalerAddress":   "replicasense.default.svc:6000",
					"sourceTrigger":   "reactive",
					"forecastHorizon": "10m",
					"quantile":        "0.95",
				}},
			},
		}
	}

	tests := []struct {
		name       string
		input      ScaledObjectInput
		candidates int
		valid      bool
	}{
		{name: "valid predictive trigger", input: valid(), candidates: 1, valid: true},
		{name: "missing source trigger", input: withoutMetadata(valid(), "sourceTrigger"), candidates: 1, valid: false},
		{name: "source trigger is not prometheus", input: sourceType(valid(), "cpu"), candidates: 1, valid: false},
		{name: "invalid quantile", input: withMetadata(valid(), "quantile", "1"), candidates: 1, valid: false},
		{name: "unsafe Prometheus address", input: withSourceMetadata(valid(), "serverAddress", "ftp://prometheus:9090"), candidates: 1, valid: false},
		{name: "day training window", input: withMetadata(valid(), "trainingWindow", "30d"), candidates: 1, valid: true},
		{name: "business timezone", input: withMetadata(valid(), "businessTimezone", "Europe/Istanbul"), candidates: 1, valid: true},
		{name: "invalid business timezone", input: withMetadata(valid(), "businessTimezone", "not/a-timezone"), candidates: 1, valid: false},
		{name: "readiness horizon", input: withMetadata(withMetadata(valid(), "startupLatency", "2m"), "safetyBuffer", "1m"), candidates: 1, valid: true},
		{name: "unrelated external scaler is ignored", input: withMetadata(valid(), "scalerAddress", "other.default.svc:6000"), candidates: 0, valid: false},
		{name: "duplicate predictive source trigger", input: duplicatePredictiveSource(valid()), candidates: 2, valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Parse(test.input, options)
			if len(result.Candidates) != test.candidates {
				t.Fatalf("candidate count = %d, want %d", len(result.Candidates), test.candidates)
			}
			if test.candidates == 0 {
				return
			}
			if got := result.Candidates[0].Spec != nil; got != test.valid {
				t.Fatalf("valid spec = %t, want %t; violations: %#v", got, test.valid, result.Candidates[0].Violations)
			}
		})
	}
}

func TestParseFingerprintBoundaries(t *testing.T) {
	options := ParserOptions{ClusterID: "cluster-a", ScalerAddresses: map[string]struct{}{"replicasense.default.svc:6000": {}}, DefaultMaxReplicaCount: 100}
	base := Parse(validInput(), options).Candidates[0].Spec
	threshold := Parse(withSourceMetadata(validInput(), "threshold", "200"), options).Candidates[0].Spec
	quantile := Parse(withMetadata(validInput(), "quantile", "0.90"), options).Candidates[0].Spec
	query := Parse(withSourceMetadata(validInput(), "query", "up"), options).Candidates[0].Spec

	if base.SourceFingerprint != threshold.SourceFingerprint || base.PolicyRevision == threshold.PolicyRevision {
		t.Fatal("threshold must retain source fingerprint and change policy revision")
	}
	if base.SourceFingerprint != quantile.SourceFingerprint || base.PolicyRevision == quantile.PolicyRevision {
		t.Fatal("quantile must retain source fingerprint and change policy revision")
	}
	if base.SourceFingerprint == query.SourceFingerprint {
		t.Fatal("query must change source fingerprint")
	}
}

func validInput() ScaledObjectInput {
	min, max := int32(1), int32(10)
	return ScaledObjectInput{Namespace: "default", Name: "api", ScaleTargetName: "api", MinReplicaCount: &min, MaxReplicaCount: &max, Triggers: []TriggerInput{
		{Type: "prometheus", Name: "reactive", Metadata: map[string]string{"serverAddress": "http://prometheus:9090", "query": "rate(requests_total[1m])", "threshold": "100"}},
		{Type: "external", Name: "predictive", Metadata: map[string]string{"scalerAddress": "replicasense.default.svc:6000", "sourceTrigger": "reactive"}},
	}}
}

func withMetadata(input ScaledObjectInput, key, value string) ScaledObjectInput {
	input.Triggers[1].Metadata[key] = value
	return input
}

func withoutMetadata(input ScaledObjectInput, key string) ScaledObjectInput {
	delete(input.Triggers[1].Metadata, key)
	return input
}

func withSourceMetadata(input ScaledObjectInput, key, value string) ScaledObjectInput {
	input.Triggers[0].Metadata[key] = value
	return input
}

func sourceType(input ScaledObjectInput, triggerType string) ScaledObjectInput {
	input.Triggers[0].Type = triggerType
	return input
}

func duplicatePredictiveSource(input ScaledObjectInput) ScaledObjectInput {
	second := input.Triggers[1]
	second.Name = "predictive-second"
	input.Triggers = append(input.Triggers, second)
	return input
}
