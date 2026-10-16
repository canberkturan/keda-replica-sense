package kube

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

type metricProbeTransport func(*http.Request) (*http.Response, error)

func (f metricProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPredictiveMetricProbeUsesKEDAAPIAndAcceptsZero(t *testing.T) {
	configuration := &rest.Config{
		Host: "https://kubernetes.example", BearerToken: "test-only-token",
		ContentConfig: rest.ContentConfig{ContentType: "application/vnd.kubernetes.protobuf", AcceptContentTypes: "application/vnd.kubernetes.protobuf"},
		Transport: metricProbeTransport(func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodGet || request.URL.Path != "/apis/external.metrics.k8s.io/v1beta1/namespaces/default/s1-replicasense_predictive" {
				t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
			}
			if request.URL.Query().Get("labelSelector") != "scaledobject.keda.sh/name=payments-api" {
				t.Fatalf("missing ScaledObject selector: %v", request.URL.Query())
			}
			if request.Header.Get("Authorization") != "Bearer test-only-token" {
				t.Fatal("probe must use Kubernetes client authentication")
			}
			if request.Header.Get("Accept") != "application/json" {
				t.Fatal("probe must request JSON even when the caller prefers protobuf")
			}
			return metricProbeResponse(200, `{"kind":"ExternalMetricValueList","items":[{"metricName":"s1-replicasense_predictive","value":"0"}]}`), nil
		}),
	}
	probe, err := NewPredictiveMetricProbe(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Check(context.Background(), "default", "payments-api", "s1-replicasense_predictive"); err != nil {
		t.Fatalf("a healthy scaler without a forecast must be accepted: %v", err)
	}
	if configuration.GroupVersion != nil || configuration.APIPath != "" || configuration.NegotiatedSerializer != nil || configuration.ContentType != "application/vnd.kubernetes.protobuf" {
		t.Fatal("constructor must not mutate the caller's Kubernetes configuration")
	}
}

func TestPredictiveMetricProbeRejectsUnavailableOrWrongMetrics(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"no values", 200, `{"kind":"ExternalMetricValueList","items":[]}`},
		{"wrong name", 200, `{"kind":"ExternalMetricValueList","items":[{"metricName":"s0-prometheus"}]}`},
		{"wrong kind", 200, `{"kind":"Status","items":[{"metricName":"s1-replicasense_predictive"}]}`},
		{"multiple values", 200, `{"kind":"ExternalMetricValueList","items":[{"metricName":"s1-replicasense_predictive"},{"metricName":"s1-replicasense_predictive"}]}`},
		{"invalid JSON", 200, `not JSON`},
		{"API forbidden", 403, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe, err := NewPredictiveMetricProbe(&rest.Config{Host: "https://kubernetes.example", Transport: metricProbeTransport(func(*http.Request) (*http.Response, error) {
				return metricProbeResponse(test.status, test.body), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			if err := probe.Check(context.Background(), "default", "payments-api", "s1-replicasense_predictive"); err == nil {
				t.Fatal("unexpected metric probe success")
			}
		})
	}
}

func TestPredictiveMetricProbeRequiresConfiguration(t *testing.T) {
	if err := (PredictiveMetricProbe{}).Check(context.Background(), "default", "payments-api", "metric"); err == nil {
		t.Fatal("missing client must fail")
	}
}

func metricProbeResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
