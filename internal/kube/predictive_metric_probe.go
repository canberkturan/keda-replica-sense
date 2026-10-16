package kube

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// PredictiveMetricProbe uses KEDA's external metrics API, preserving KEDA's
// configured scaler authentication, TLS and network path. It does not dial
// arbitrary ScaledObject endpoints or need access to authentication Secrets.
type PredictiveMetricProbe struct {
	Client rest.Interface
}

func NewPredictiveMetricProbe(configuration *rest.Config) (*PredictiveMetricProbe, error) {
	configuration = rest.CopyConfig(configuration)
	configuration.APIPath = "/apis"
	configuration.GroupVersion = &schema.GroupVersion{Group: "external.metrics.k8s.io", Version: "v1beta1"}
	// DoRaw deliberately decodes a small JSON envelope without importing the
	// external-metrics API scheme. Do not inherit a protobuf content preference.
	configuration.ContentType = "application/json"
	configuration.AcceptContentTypes = "application/json"
	configuration.NegotiatedSerializer = clientgoscheme.Codecs.WithoutConversion()
	client, err := rest.RESTClientFor(configuration)
	if err != nil {
		return nil, fmt.Errorf("create KEDA predictive metric probe: %w", err)
	}
	return &PredictiveMetricProbe{Client: client}, nil
}

func (p PredictiveMetricProbe) Check(ctx context.Context, namespace, scaledObject, metric string) error {
	if p.Client == nil || namespace == "" || scaledObject == "" || metric == "" {
		return fmt.Errorf("predictive metric probe requires client, namespace, ScaledObject and metric")
	}
	selector := labels.Set{"scaledobject.keda.sh/name": scaledObject}.AsSelector().String()
	body, err := p.Client.Get().Namespace(namespace).Resource(metric).Param("labelSelector", selector).DoRaw(ctx)
	if err != nil {
		return fmt.Errorf("read KEDA predictive metric: %w", err)
	}
	var response struct {
		Kind  string `json:"kind"`
		Items []struct {
			MetricName string `json:"metricName"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decode KEDA predictive metric response: %w", err)
	}
	if response.Kind != "ExternalMetricValueList" || len(response.Items) != 1 || response.Items[0].MetricName != metric {
		return fmt.Errorf("KEDA did not return the requested predictive metric")
	}
	return nil
}
