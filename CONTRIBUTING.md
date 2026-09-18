# Contributing to ReplicaSense

Thank you for improving ReplicaSense. Contributions of documentation, tests,
bug fixes, model improvements, and Kubernetes operational experience are all
welcome.

## Getting started

Fork the repository, create a focused branch, and keep each pull request small
enough to review. Explain the user-visible behavior being changed and include
tests for code changes whenever practical.

ReplicaSense is a Go project. A current Go toolchain and Docker are enough for
the standard checks. Helm is needed when changing the chart.

```bash
make test vet build
helm lint charts/replicasense
helm template replicasense charts/replicasense --set clusterID=contributor-test >/dev/null
```

Changes touching XGBoost, its Dockerfile, or native dependencies must also
build the final native image. This verifies both the native unit tests and its
minimal runtime libraries:

```bash
docker build --file Dockerfile.xgboost \
  --build-arg COMMAND=replicasense-forecaster \
  --tag replicasense-forecaster:contributor-test .
```

## Testing Kubernetes behavior

An optional non-production Kubernetes cluster—kind, minikube, or another
environment you control—is useful for validating chart and KEDA changes. Do
not depend on a private lab or personal cluster. Describe the Kubernetes and
KEDA versions used, the applied manifests, expected behavior, and observed
result in the pull request.

Predictive changes must preserve these invariants:

- Native KEDA triggers remain independently reactive.
- Stale, missing, invalid, or unsafe predictive data fails closed.
- Metrics use stable low-cardinality workload labels; do not add raw PromQL,
  tenant identifiers, request IDs, or other unbounded labels.
- Model evaluation does not leak future samples into training.

## Documentation and chart changes

Keep examples generic and safe to copy. Do not include real credentials,
private cluster names, personal paths, or screenshots with sensitive data.
When changing exported metrics, update [docs/metrics.md](docs/metrics.md).
When changing chart values or installation requirements, update the chart
README and [installation guide](docs/enterprise-installation.md).

Release metadata is versioned. A release changes the chart version, image tags,
and the matching Artifact Hub package directory together. Do not alter historic
Artifact Hub package metadata after it has been published.

## License

By submitting a contribution, you agree to license it under Apache-2.0.
