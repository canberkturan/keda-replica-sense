package config

import "testing"

func TestLoadController(t *testing.T) {
	environment := map[string]string{
		"REPLICASENSE_CLUSTER_ID":       "prod-openshift-01",
		"REPLICASENSE_SCALER_ADDRESSES": "replicasense.default.svc:6000, replicasense.keda.svc:6000",
		"REPLICASENSE_LEADER_ELECTION":  "false",
		"REPLICASENSE_DATABASE_URL":     "postgres://replicasense:password@postgres:5432/replicasense",
	}
	config, err := LoadController(environmentLookup(environment))
	if err != nil {
		t.Fatalf("LoadController() error = %v", err)
	}
	if config.Parser.ClusterID != "prod-openshift-01" || len(config.Parser.ScalerAddresses) != 2 {
		t.Fatalf("unexpected parser config: %#v", config.Parser)
	}
	if config.LeaderElection {
		t.Fatal("LeaderElection = true, want false")
	}
	if config.MetricsBindAddress != ":8080" || config.HealthProbeBindAddress != ":8081" {
		t.Fatalf("unexpected default addresses: %#v", config)
	}
}

func TestLoadControllerRequiresIdentityAndAddress(t *testing.T) {
	if _, err := LoadController(environmentLookup(map[string]string{})); err == nil {
		t.Fatal("LoadController() error = nil, want missing cluster ID error")
	}
	if _, err := LoadController(environmentLookup(map[string]string{"REPLICASENSE_CLUSTER_ID": "cluster-a", "REPLICASENSE_DATABASE_URL": "postgres://example"})); err == nil {
		t.Fatal("LoadController() error = nil, want missing scaler address error")
	}
	if _, err := LoadController(environmentLookup(map[string]string{"REPLICASENSE_CLUSTER_ID": "cluster-a", "REPLICASENSE_SCALER_ADDRESSES": "replicasense.default.svc:6000"})); err == nil {
		t.Fatal("LoadController() error = nil, want missing database URL error")
	}
}

func environmentLookup(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
