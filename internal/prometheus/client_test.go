package prometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/sampler"
)

func TestClientQueryInstant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/query" || request.URL.Query().Get("query") != "up" {
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
		_, _ = writer.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"value":[1756713600,"42.5"]}]}}`))
	}))
	defer server.Close()

	value, err := (Client{}).QueryInstant(context.Background(), sampler.PrometheusQuery{ServerAddress: server.URL, Query: "up", ObservedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil || value != 42.5 {
		t.Fatalf("QueryInstant() = %v, %v; want 42.5, nil", value, err)
	}
}

func TestClientRejectsMultipleSeries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"1"]},{"value":[1,"2"]}]}}`))
	}))
	defer server.Close()

	if _, err := (Client{}).QueryInstant(context.Background(), sampler.PrometheusQuery{ServerAddress: server.URL, Query: "up", ObservedAt: time.Now()}); err == nil {
		t.Fatal("QueryInstant() error = nil, want multiple series error")
	}
}

func TestClientRejectsNonFiniteValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"NaN"]}]}}`))
	}))
	defer server.Close()

	if _, err := (Client{}).QueryInstant(context.Background(), sampler.PrometheusQuery{ServerAddress: server.URL, Query: "up", ObservedAt: time.Now()}); err == nil {
		t.Fatal("QueryInstant() error = nil, want non-finite sample rejection")
	}
}

func TestClientRejectsUnsafePrometheusAddress(t *testing.T) {
	for _, address := range []string{"ftp://prometheus:9090", "http://user:password@prometheus:9090", "http://prometheus:9090#fragment"} {
		if _, err := (Client{}).QueryInstant(context.Background(), sampler.PrometheusQuery{ServerAddress: address, Query: "up", ObservedAt: time.Now()}); err == nil {
			t.Fatalf("QueryInstant(%q) error = nil", address)
		}
	}
}
