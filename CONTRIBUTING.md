# Contributing to ReplicaSense

Thank you for contributing. Keep pull requests focused and include tests for
changed behavior.

Before opening a pull request, run:

```bash
make test vet build
```

For changes to Kubernetes behavior, apply the development manifests and record
the relevant `keda-lab` evidence in `docs/acceptance-criteria.md`. Predictive
behavior must preserve the independent native KEDA trigger and fail closed on
stale or unsafe forecasts.

By submitting a contribution, you agree to license it under Apache-2.0.
