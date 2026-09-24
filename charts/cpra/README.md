# CPRa Helm chart

A single-owner CPRa StatefulSet with local Raft, retained PVC, authenticated read-only API, and exec client probes. This is not an HA chart.

Required values: `auth.existingSecret` when `api.enabled=true`, exactly one of `manifest.existingSecret`, `manifest.existingConfigMap`, or `manifest.existingClaim`, and a verified release image. Credentials and monitor contents are supplied through existing resources, never chart values. Default storage requests `ReadWriteOncePod` from a compatible CSI provisioner. Headless mode (`api.enabled=false`) installs no API Service, auth mount, probes, or Helm test Job.

See [the complete installation, lifecycle, backup, permissions and verification guide](https://github.com/ziad-hsn/cpra/blob/main/docs/container-helm.md). The repository copy is `docs/container-helm.md`.

```sh
helm lint charts/cpra -f charts/cpra/ci/lint-values.yaml --strict
helm template cpra charts/cpra -f charts/cpra/ci/lint-values.yaml
```

`ci/lint-values.yaml` contains resource names only and is not a ready-to-install environment. Chart API v2 supports Helm 3 and 4; declared Kubernetes range is 1.32–1.36. Native cluster/storage coverage is recorded separately in release evidence. Chart/app version 0.1.0 is the source development version and is replaced by the release pipeline.
