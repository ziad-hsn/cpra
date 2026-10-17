# CPRa Helm chart

A single-owner CPRa StatefulSet with local Raft, a retained PVC and authenticated client probes. The API is read-only by default. Explicit `management.enabled=true` connects named operator access, encrypted configuration storage and HTTPS for dashboard/SDK/CLI writes. This is not an HA chart.

Required values: `auth.existingSecret` when `api.enabled=true`, exactly one of `manifest.existingSecret`, `manifest.existingConfigMap`, or `manifest.existingClaim`, and a verified release image. Credentials and monitor contents are supplied through existing resources, never chart values. Default storage requests `ReadWriteOncePod` from a compatible CSI provisioner. Headless mode (`api.enabled=false`) installs no API Service, auth mount, probes, or Helm test Job.

Management also requires immutable Secret references for `management.policy`, `management.encryption` and `management.tls`, with API and persistent Raft enabled. The local wrapping key is an existing 32-byte binary key; startup never generates or replaces it. A non-root init container copies selected sources into private files in a memory volume. CPRa mounts only the protected directory read-only. HTTPS probes verify the supplied CA and certificate, which must cover `127.0.0.1` and the Service's `RELEASE-FULLNAME.NAMESPACE.svc` name. The Helm test Job receives only its read token and CA. Generic chart-managed Ingress is unavailable in management mode; an operator-managed HTTPS backend must preserve verified TLS to CPRa. Authentication changes use stopped local administration before deploying new Secret references.

See [the complete installation, lifecycle, backup, permissions and verification guide](https://github.com/ziad-hsn/cpra/blob/main/docs/container-helm.md). The repository copy is `docs/container-helm.md`.

Log notifications with an omitted or empty `file` write JSON to stdout. For
relative file destinations, set `notificationLog.enabled=true`. A separate,
bounded `emptyDir` is used by default; its files disappear when the Pod is
replaced. For retained notification files, supply `notificationLog.existingClaim`
with a dedicated claim. State and configuration claims cannot be reused.
The non-root initializer creates a private `notifications` directory when absent
and refuses existing symlinks, unexpected owners or permissions other than 0700.
It never recursively changes retained files. A CSI driver that broadens retained
directory/file permissions must be corrected or replaced; CPRa refuses that
storage. Qualify the actual driver and filesystem, including remount behavior.
File output has no automatic rotation: configure external retention or prefer
stdout with the container runtime's log limits. Exhaustion causes notification
failure, never deletion of retained history or state.

```sh
helm lint charts/cpra -f charts/cpra/ci/lint-values.yaml --strict
helm template cpra charts/cpra -f charts/cpra/ci/lint-values.yaml
helm lint charts/cpra -f charts/cpra/ci/management-values.yaml --strict
helm template cpra charts/cpra -f charts/cpra/ci/management-values.yaml
```

Both `ci/` fixtures contain resource names only and are not ready-to-install environments. Chart API v2 supports Helm 3 and 4; declared Kubernetes range is 1.32–1.36. Native cluster/storage coverage is recorded separately in release evidence. The first candidate uses chart version `0.1.0-rc.1` and application/image tag `v0.1.0-rc.1`. These source values do not establish that an image or chart has been published. Candidates remain private until all agreed gates pass; the release pipeline records the approved version and verified image digest in the packaged chart.

The private runtime campaign invokes `scripts/packaging/live_helm.py` with
`--management` for both Helm clients. This preserves the read-only scenarios and
adds a separate managed deployment for protected Secret copies, verified TLS,
conditional encrypted writes, restart, stopped access rotation, RWOP fencing
and retained-history recovery. Release evidence must contain both modes;
rendering or harness unit tests cannot supply runtime outcomes. The stopped copy
scenario is a trusted whole-directory clone, distinct from the authenticated
CLI backup/restore procedure.
