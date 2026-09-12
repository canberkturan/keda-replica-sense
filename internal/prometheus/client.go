// Package prometheus implements the narrow Prometheus HTTP API used only by
// the ReplicaSense sampler.
package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/sampler"
)

type Client struct {
	HTTPClient *http.Client
}

const maxResponseBytes = 10 << 20

func (c Client) QueryInstant(ctx context.Context, query sampler.PrometheusQuery) (float64, error) {
	baseURL, err := prometheusURL(query.ServerAddress)
	if err != nil {
		return 0, err
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/api/v1/query"
	parameters := baseURL.Query()
	parameters.Set("query", query.Query)
	parameters.Set("time", query.ObservedAt.UTC().Format("2006-01-02T15:04:05Z"))
	baseURL.RawQuery = parameters.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("create prometheus query request: %w", err)
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("request prometheus instant query: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return 0, fmt.Errorf("prometheus instant query returned HTTP %d", response.StatusCode)
	}

	var payload queryResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&payload); err != nil {
		return 0, fmt.Errorf("decode prometheus instant query response: %w", err)
	}
	if payload.Status != "success" {
		return 0, fmt.Errorf("prometheus instant query failed: %s", payload.Error)
	}
	return payload.Data.value()
}

func (c Client) QueryRange(ctx context.Context, query sampler.RangeQuery) ([]sampler.TimedValue, error) {
	baseURL, err := prometheusURL(query.ServerAddress)
	if err != nil {
		return nil, err
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/api/v1/query_range"
	parameters := baseURL.Query()
	parameters.Set("query", query.Query)
	parameters.Set("start", query.Start.UTC().Format(time.RFC3339))
	parameters.Set("end", query.End.UTC().Format(time.RFC3339))
	// Prometheus accepts duration syntax or a float count of seconds. Go emits
	// "1m0s", which Prometheus does not accept as a duration, so use seconds.
	parameters.Set("step", strconv.FormatFloat(query.Step.Seconds(), 'f', -1, 64))
	baseURL.RawQuery = parameters.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL.String(), nil)
	if err != nil {
		return nil, err
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("prometheus range query returned HTTP %d", response.StatusCode)
	}
	var payload queryResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Status != "success" {
		return nil, fmt.Errorf("prometheus range query failed: %s", payload.Error)
	}
	if payload.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("prometheus range result type %q is unsupported", payload.Data.ResultType)
	}
	var matrix []struct {
		Values [][]json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(payload.Data.Result, &matrix); err != nil {
		return nil, err
	}
	if len(matrix) == 0 {
		return []sampler.TimedValue{}, nil
	}
	if len(matrix) != 1 {
		return nil, fmt.Errorf("prometheus query must return exactly one series, got %d", len(matrix))
	}
	values := make([]sampler.TimedValue, 0, len(matrix[0].Values))
	for _, pair := range matrix[0].Values {
		if len(pair) != 2 {
			return nil, fmt.Errorf("prometheus range result has invalid value shape")
		}
		var timestamp float64
		var rawValue string
		if err := json.Unmarshal(pair[0], &timestamp); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(pair[1], &rawValue); err != nil {
			return nil, err
		}
		value, err := parseSampleValue(rawValue)
		if err != nil {
			return nil, err
		}
		values = append(values, sampler.TimedValue{ObservedAt: time.Unix(0, int64(timestamp*float64(time.Second))).UTC(), Value: value})
	}
	return values, nil
}

type queryResponse struct {
	Status string    `json:"status"`
	Error  string    `json:"error"`
	Data   queryData `json:"data"`
}

type queryData struct {
	ResultType string          `json:"resultType"`
	Result     json.RawMessage `json:"result"`
}

func (d queryData) value() (float64, error) {
	var values [][]json.RawMessage
	switch d.ResultType {
	case "vector":
		var vector []struct {
			Value []json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(d.Result, &vector); err != nil {
			return 0, fmt.Errorf("decode prometheus vector: %w", err)
		}
		if len(vector) != 1 {
			return 0, fmt.Errorf("prometheus query must return exactly one series, got %d", len(vector))
		}
		values = append(values, vector[0].Value)
	case "scalar":
		var scalar []json.RawMessage
		if err := json.Unmarshal(d.Result, &scalar); err != nil {
			return 0, fmt.Errorf("decode Prometheus scalar: %w", err)
		}
		values = append(values, scalar)
	default:
		return 0, fmt.Errorf("prometheus query result type %q is unsupported", d.ResultType)
	}
	if len(values[0]) != 2 {
		return 0, fmt.Errorf("prometheus result has invalid value shape")
	}
	var rawValue string
	if err := json.Unmarshal(values[0][1], &rawValue); err != nil {
		return 0, fmt.Errorf("decode prometheus sample value: %w", err)
	}
	return parseSampleValue(rawValue)
}

func prometheusURL(raw string) (*url.URL, error) {
	baseURL, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse prometheus server address: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, fmt.Errorf("prometheus server address must use http or https")
	}
	if baseURL.Host == "" || baseURL.User != nil || baseURL.Fragment != "" {
		return nil, fmt.Errorf("prometheus server address must contain a host and no credentials or fragment")
	}
	return baseURL, nil
}

func parseSampleValue(raw string) (float64, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse prometheus sample value: %w", err)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("prometheus sample value must be finite")
	}
	return value, nil
}
