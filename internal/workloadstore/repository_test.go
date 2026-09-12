package workloadstore

import "testing"

func TestParentKeyKeepsClusterInIdentity(t *testing.T) {
	first := ParentKey{ClusterID: "cluster-a", Namespace: "payments", ScaledObjectName: "api"}
	second := ParentKey{ClusterID: "cluster-b", Namespace: "payments", ScaledObjectName: "api"}
	if first == second {
		t.Fatal("parent keys from separate clusters must not compare equal")
	}
}
