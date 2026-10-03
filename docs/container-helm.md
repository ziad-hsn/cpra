# Containers and Helm

CPRa runs one controller with one local Raft data directory. The chart and Compose definition preserve that directory across replacement. They provide no multi-node failover, leader election between installations, or capacity guarantee. Never run a second installation against the same targets as an attempted HA setup: separate directories have independent incident ownership.

## Images and release identity

The production `docker/Dockerfile` packages the exact tested Linux release binaries. It does not compile Go or rebuild the dashboard. The release stage must contain `linux_amd64/` and `linux_arm64/`, each with `cpra`, `cpractl`, `LICENSE`, `LICENSES/`, `THIRD_PARTY_NOTICES/`, `RELEASE.json`, and `DEPENDENCIES.json`.

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-context release=dist/release --file docker/Dockerfile \
  --build-arg VERSION="$VERSION" --build-arg COMMIT="$COMMIT" \
  --build-arg SOURCE_DATE_EPOCH="$SOURCE_DATE_EPOCH" --provenance=false \
  --output type=oci,dest=cpra-image.tar,rewrite-timestamp=true \
  --tag "ghcr.io/ziad-hsn/cpra:$VERSION" .
```

Use a digest from the verified release for deployments. Cross-compilation and OCI image creation do not prove native ARM64 execution; release evidence must identify native tests separately. The image runs as UID/GID 1001, includes CA roots and dependency notices, and declares no anonymous volume. `docker/Dockerfile.dev` is the separate pinned source build, including the dashboard. The named release context is required by the production build. Build credentials belong in BuildKit secret mounts when needed, never build arguments or copied files. See [BuildKit contexts and build options](https://docs.docker.com/reference/cli/docker/buildx/build/) and [build secrets](https://docs.docker.com/build/building/secrets/).

Set `VERSION`, `COMMIT`, and `SOURCE_DATE_EPOCH` from the approved `RELEASE.json`. The pinned Alpine base supplies its CA bundle; wrapping binaries performs no package-repository installation. A preparation stage runs on the build host's architecture and copies the selected target binaries without executing them. It assigns explicit permissions and normalizes every packaged file and directory timestamp to source time before the final image copies that tree. This is necessary because BuildKit's `rewrite-timestamp` setting only clamps newer timestamps; it would otherwise preserve older checkout timestamps. Original input layers remain outside the final image. Provenance is added separately by the release workflow. For native execution tests, export the single-platform image with `--output type=docker,dest=candidate.tar,rewrite-timestamp=true`, then run `docker load -i candidate.tar`. Direct loading with timestamp rewriting conflicts with unpacking on some Docker/containerd builders. `scripts/packaging/image_repro.py` builds without cache in two independent roots, deliberately gives all input files and directories timestamps one day before or after the source date, and compares every OCI payload file, including compressed blobs and image metadata, with a config-digest check against the image used by runtime tests. Tar transport headers and signing timestamps are outside this unsigned-payload comparison.

## Compose

Copy `docker/.env.example` to a private environment file and set a released image digest, existing absolute monitor and token file paths, and a stable volume name. `CPRA_RUNTIME_FILE` optionally selects an existing runtime-settings file; otherwise Compose uses `runtime.yaml` beside the bundle. Token, manifest and runtime files must be readable by container UID/GID 1001. On Linux, a suitable installation is owner root, group 1001, mode 0640 with traversable parent directories. File-backed Compose secrets use a bind mount; Compose does not remap their ownership or apply secret `uid`, `gid`, and `mode`. Configuration containing provider credentials also needs restricted host permissions. [Compose secrets reference](https://docs.docker.com/reference/compose-file/services/#secrets).

```sh
docker compose --env-file /etc/cpra/compose.env -f docker/docker-compose.yml config --quiet
docker compose --env-file /etc/cpra/compose.env -f docker/docker-compose.yml up -d
docker compose --env-file /etc/cpra/compose.env -f docker/docker-compose.yml ps
```

The API binds to host loopback by default. Terminate TLS at an authenticated reverse proxy if remote access is required. The filesystem is read-only except the stable `cpra-data` volume and a bounded `/tmp`. Logs use Docker's bounded local driver. Stop receives 60 seconds, allowing the application's 45-second drain budget. Docker health reports readiness; Docker does not automatically restart a container solely because it becomes unhealthy. [Compose service behavior](https://docs.docker.com/reference/compose-file/services/).

Stop/recreate preserves the named volume. `down --volumes` deliberately deletes it and must not be used for routine upgrades. Do not change `CPRA_DATA_VOLUME` during an upgrade. The defaults of a 1 GiB container limit and 512 MiB Go memory limit are small-installation starting points, not sizing evidence. Go's soft limit covers Go-managed memory, not all mapped storage and process RSS; measure the complete working set and raise both limits before larger fleets. Leave `GOMAXPROCS` unset for Go's container-aware default. [Go memory limit](https://go.dev/doc/gc-guide#Memory_limit), [container-aware GOMAXPROCS](https://go.dev/blog/container-aware-gomaxprocs).

For local source development, set `CPRA_IMAGE=cpra:dev` and add `-f docker/compose.dev.yaml --build`. This intentionally uses a different image build path from release validation. Missing source bind files fail rather than being silently created as directories.

API authentication is committed in Raft; a startup token file bootstraps a fresh store once. Stop the Compose owner, use `cpractl local auth rotate-read-token` against that same stopped state directory, and retain its newly generated protected file and committed revision. The administrative container must use the service's state identity and an output directory outside Raft. If the outcome is unconfirmed, retain that file and reconcile with `local auth list` before proceeding. Distribute the confirmed token into a new restricted host file, update `CPRA_TOKEN_FILE`, then recreate the service. Verify that its authenticated readiness succeeds and the old token is rejected. Replacing the file alone does not rotate committed access. Named management principals use `local auth rotate PRINCIPAL` instead. See [local authentication administration](local-authentication.md). Provider SDK credentials likewise need an explicit deployment restart unless that provider's documented refresh behavior has been tested.

## Helm installation

The application chart uses Chart API v2 and declares Kubernetes 1.32 through 1.36. The contract suite renders all five minors under Helm 3 and Helm 4; live evidence records the specific Kubernetes, Helm, and storage driver actually exercised. A declared range is not a claim that every CSI driver or ingress controller was tested.

Create API and manifest Secrets separately from Helm values. Versioned immutable names are the supported rotation contract. For example, with existing files:

```sh
kubectl -n monitoring create secret generic cpra-api-v1 --from-file=token=/secure/cpra-token
kubectl -n monitoring patch secret cpra-api-v1 --type=merge -p '{"immutable":true}'
kubectl -n monitoring create secret generic cpra-monitors-v1 --from-file=monitors.yaml=/secure/monitors.yaml
kubectl -n monitoring patch secret cpra-monitors-v1 --type=merge -p '{"immutable":true}'
helm upgrade --install cpra ./charts/cpra --namespace monitoring \
  --set auth.existingSecret=cpra-api-v1 \
  --set manifest.existingSecret=cpra-monitors-v1 \
  --set image.digest=sha256:REPLACE_WITH_VERIFIED_RELEASE_DIGEST \
  --wait --timeout 15m
helm test cpra --namespace monitoring
```

The namespace must already exist. These commands describe an operator installation, not permission to use an arbitrary current context. For automation, supply the intended kubeconfig/context explicitly. The chart contains no credential values, no generated token Secret, and no inline monitor content. Helm stores release metadata in cluster Secrets, so placing credentials in values would expose them to release readers. [Helm storage backends](https://helm.sh/docs/topics/advanced/#storage-backends).

### Management over HTTPS

The installation above exposes authenticated read-only monitoring. For dashboard,
SDK and CLI writes, explicitly enable management while keeping API and persistent
Raft enabled. The chart currently supports the local wrapping-key backend for
management; the application's other encryption backends are not chart options.
Provision a [named verifier policy](management-startup.md#enabling-management),
an existing 32-byte binary wrapping key and a server certificate/private key
outside the state directory. Keep plaintext named tokens with their owners. The
probe token in `auth.existingSecret` is a separate read-only token; its verifier
must differ from every named policy verifier.

The server certificate must include `127.0.0.1` and the exact
`RELEASE-FULLNAME.NAMESPACE.svc` DNS name. For release `cpra` with default naming
in namespace `monitoring`, that name is `cpra-cpra.monitoring.svc`. Its CA must
verify that certificate. Preserve the release/fullname when upgrading an existing
installation. Provision the files first; do not generate a replacement wrapping
key each time the pod starts.

```sh
kubectl -n monitoring create secret generic cpra-policy-v1 --from-file=policy.yaml=/secure/principals.yaml
kubectl -n monitoring patch secret cpra-policy-v1 --type=merge -p '{"immutable":true}'
kubectl -n monitoring create secret generic cpra-key-v1 --from-file=wrapping.key=/secure/wrapping.key
kubectl -n monitoring patch secret cpra-key-v1 --type=merge -p '{"immutable":true}'
kubectl -n monitoring create secret generic cpra-tls-v1 \
  --from-file=tls.crt=/secure/server.pem \
  --from-file=tls.key=/secure/server.key \
  --from-file=ca.crt=/secure/ca.pem
kubectl -n monitoring patch secret cpra-tls-v1 --type=merge -p '{"immutable":true}'
```

Add these references to the values file that already selects the verified image,
probe token, manifest and storage:

```yaml
management:
  enabled: true
  policy:
    existingSecret: cpra-policy-v1
    key: policy.yaml
  encryption:
    existingSecret: cpra-key-v1
    key: wrapping.key
    previous: []
  tls:
    existingSecret: cpra-tls-v1
    keys:
      certificate: tls.crt
      privateKey: tls.key
      ca: ca.crt
```

These are references, never policy contents, tokens, certificates or encryption
key values. When Helm can query an existing Secret, it checks immutability and
the selected keys. Offline rendering cannot establish that a Secret exists or
has valid contents; missing projection keys prevent the pod from starting.
Kubernetes immutable Secrets require replacement references and new pods for
rotation. [Secret operations](https://kubernetes.io/docs/concepts/configuration/secret/).

The non-root init container uses the same CPRa image and receives no Raft volume.
It reads the selected Secret files, copies them into a memory-backed volume and
sets its protected directory to `0700` and files to `0600`, including on retry.
The application mounts only that protected directory read-only at
`/etc/cpra/management`; it receives no mount of the projected input volume.
CPRa's protected-file checks remain enabled. A direct mount of a group-writable
projected directory would not satisfy those checks; `fsGroupChangePolicy` does
not change Secret or memory-volume permission behavior. Test the actual UID 1001
mounts on the selected cluster. [Volume behavior](https://kubernetes.io/docs/concepts/storage/volumes/),
[security contexts](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/).

All managed probes use HTTPS on loopback, `--ca-file` and the staged read-only
token. The Helm test Job uses the Service DNS name and receives only the CA and
read token, without the TLS private key, policy, wrapping keys or state claim.
For local access, port-forward the Service and use `https://127.0.0.1:8060` with
the supplied CA. Generic chart-managed Ingress is rejected in management mode;
configure an operator-managed gateway/proxy only when it verifies TLS to CPRa.
The chart does not implement a generic TLS-backend policy for ingress controllers.

Policy and token files bootstrap fresh authentication once. After restart,
committed authority wins; replacing these Secret references does not rotate or
revoke it. Use the stopped administration procedure below, then deploy its new
versioned immutable references. A wrapping key unlocks existing encrypted state.
For a controlled key change, add the original key's Secret/key reference to
`management.encryption.previous` before selecting the new active key. This list
allows at most 32 distinct references, excluding the active reference. Retain
original keys for as long as retained data or backups require them; changing
chart values does not rewrite every ciphertext or make old backups decryptable
with only the new key. Protect key backups separately from the complete Raft
directory.

Use `helm test` without `--logs` for this Job hook: Helm 3's log option can look for a Pod with the Job's name and return an error even after the Job succeeds. If the test fails, inspect `kubectl -n monitoring logs job/RELEASE-FULLNAME-test-api`. Failed Jobs remain for diagnosis for up to one day; successful Jobs are removed.

Exactly one manifest source is required: `manifest.existingSecret` for credential-bearing manifests, `manifest.existingConfigMap` for non-secret manifests, or `manifest.existingClaim` with a relative `manifest.file` for a large read-only configuration volume. ConfigMap and Secret objects have a 1 MiB limit, and Helm release records have storage limits too. The chart keeps large manifests out of its rendered release. Use a separate claim from the writable state claim; stage and validate files before starting CPRa. [ConfigMap limits](https://kubernetes.io/docs/concepts/configuration/configmap/), [Secret limits](https://kubernetes.io/docs/concepts/configuration/secret/).

Optional SDK/profile files come from `providerFiles.existingSecret`, mounted read-only at `/etc/cpra/provider`. `providerEnv` accepts only `{name, secret, key}` references to existing Secret keys. Configure file paths through the driver's supported manifest fields or the SDK's standard environment variables. It never accepts literal credentials. Use a new Secret name and an intentional upgrade to rotate files or environment values. Kubernetes recovery optionally mounts a service account token and grants only patch access to named Deployments and update access to named Deployment scale subresources in the release namespace. External-cluster credentials and broader permissions are operator-managed; no cluster-wide role is installed.

Ingress is disabled by default. The read-only chart mode can enable it with a host, TLS Secret and root path `/`; the current dashboard does not support a path prefix. Managed mode requires the HTTPS access procedure above. The chart creates no host socket mounts, privileged containers, systemd bus mounts or Docker daemon access. Those recovery drivers remain available in the Linux binary but require a separate privileged deployment design. ICMP can opt into the pod's unprivileged ping group range without adding `NET_RAW`. A NetworkPolicy is optional because permitted destinations depend on monitored targets. When enabled, explicitly permit DNS, monitored endpoints, notification providers, dashboard ingress, and the Helm test client's access; the default empty rules deny traffic. A supporting CNI is required for enforcement.

## Storage, probes, and maintenance

The StatefulSet has exactly one replica, or zero with `maintenance=true`. There is no replica count setting, HPA, automatic rollback hook, or disruption-budget claim. The default claim requests `ReadWriteOncePod`, which needs a compatible CSI provisioner. It fences the volume to one pod. `ReadWriteOnce` is supported only with `persistence.acknowledgeReadWriteOnce=true`: it allows multiple pods on the same node and is a weaker fence. An existing claim's access mode and storage behavior must be checked by the operator; Helm does not change that claim. Never force-delete a StatefulSet pod while an old node may still be executing it. [Persistent volumes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/), [force-deletion hazards](https://kubernetes.io/docs/tasks/run-application/force-delete-stateful-set-pod/).

StorageClass `null` selects the cluster default by omission; `""` requests no dynamic class. Provisioner permissions must make the mounted directory usable by UID/GID 1001. The pod requests `fsGroup=1001` with `OnRootMismatch`; CSI drivers may implement volume ownership themselves. Test the selected driver, including filesystem locking and durable writes, before production use. The chart does not grant root privileges to fix ownership. Memory-only mode requires both `storage.mode=memory` and `persistence.enabled=false` and loses state when the pod is replaced.

Startup and liveness use `cpractl health`; readiness uses `cpractl ready`. All read a mounted token file and make authenticated read-only API requests. Read-only chart mode explicitly permits HTTP for its loopback probes and internal Helm test Service. Managed mode uses verified HTTPS and the mounted CA for both. External clients use an HTTPS endpoint. No probe executes a monitor or acquires the state-directory lock. The startup budget is ten minutes and can be increased for a measured large-fleet load time. Monitored target outages must not fail liveness. Durable-store failure or drain makes readiness unavailable. CPU starvation can also exhaust an exec probe timeout, so size resources and probe budgets together. The Helm test Job mounts only the API token and, for HTTPS, its CA; it is never a second controller. [Kubernetes probes](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/).

The CLI request timeout is two seconds and the outer exec timeout is three seconds, allowing the CLI time to report a request deadline before Kubernetes kills it. This deliberately adjusts the original two-second outer probe proposal; matching inner and outer deadlines can hide the diagnostic. For headless operation, set `api.enabled=false`. The chart then omits token mounts, API Services, ingress, probes, and its Helm test Job. Process existence alone does not prove controller progress in that mode; inspect the process logs and durable state through the documented stopped procedure.

For an offline backup, upgrade with the same values plus `maintenance=true`, then wait for the controller pod to terminate. Only then mount the state claim in a designated backup pod, copy the **whole data directory** (Raft log/stable store, node identity, completed snapshots, history segments and catalog), and remove that pod before resuming. A simultaneous backup Job cannot mount a RWOP volume while CPRa owns it. An application snapshot by itself is not a complete event-history backup. Validate a restore on an empty, separate volume while the original owner is stopped; never bring both copies online against the same configured targets. Do not change release/fullname or PVC template names during routine upgrades.

Uninstall and scaling retain the StatefulSet-created PVC. Deleting its namespace or the claim is a separate destructive operation. Volume snapshots are an optional CSI mechanism, not a substitute for a quiesced, validated whole-state backup. Kubernetes retention does not override the storage provider's behavior after claim deletion. [StatefulSet retention](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/), [volume snapshots](https://kubernetes.io/docs/concepts/storage/volume-snapshots/).

For API token rotation, first enable maintenance and wait for the owner to exit and release its claim. Mount that claim in a single administrative pod with the state identity, then run `cpractl local auth rotate-read-token` with a new protected output outside Raft. Confirm its committed revision; reconcile an unconfirmed result before taking further action. Transfer the generated token securely into a new immutable `cpra-api-v2` Secret, remove the administrative pod and wait for the claim to be released. Upgrade `auth.existingSecret` to that new name and leave maintenance to resume one owner. Verify authenticated readiness and rejection of the old token. Replacing the Secret alone cannot rotate Raft's committed verifier. Named management identities use `local auth rotate PRINCIPAL`. Keep a GitOps reconciler from restarting the owner during this procedure. Use versioned manifest Secrets too, or change `manifest.revision` to intentionally restart after a controlled ConfigMap/PVC update.

Helm 3 and Helm 4 differ in apply, waiting and rollback options; this chart uses portable Chart v2 resources and explicit `--wait --timeout`. Helm 4 normally uses server-side apply for new installs and preserves legacy apply behavior when upgrading a Helm 3 release. Do not add automatic rollback-on-failure to durability upgrades: rolling back Kubernetes resources cannot undo state migrations or external recovery actions. Review release compatibility and backup instructions first. [Helm overview](https://helm.sh/docs/overview/), [Helm 4 upgrade](https://helm.sh/docs/helm/helm_upgrade/), [Helm 3 upgrade](https://helm.sh/docs/v3/helm/helm_upgrade/).

A failed rollout can leave a StatefulSet pod on the bad template after the template is corrected. Restore a validated image/configuration reference first, inspect the current owner, and delete the failed pod normally if the controller is still waiting on it. Never force deletion or mount its state concurrently. Changing the volume claim template or storage class is a migration; an ordinary values edit is not a supported expansion procedure. Follow the provisioner's expansion process and verify the actual filesystem size separately.

## Verification

Install the pinned Python test dependency and run the actual chart renderer and Compose parser:

```sh
python3 -m pip install -r scripts/packaging/requirements.txt
python3 -m unittest discover -s scripts/packaging -p test_container_helm.py -v
HELM_BIN=/path/to/helm3 python3 -m unittest discover -s scripts/packaging -p test_container_helm.py -v
```

The live harnesses under `scripts/packaging` accept explicit candidate images and designated test environments and write JSON evidence. Each evidence file names the storage access mode and observation boundary. Rendering, server-side API validation, native execution, restart recovery, and CSI fencing are separate gates; a missing runtime or provisioner remains not verified. Publishing a chart or image does not establish comparative performance. The 24-hour endurance test is outside the requested work and is not a mandatory completion gate.

```sh
python3 scripts/packaging/live_compose.py --image cpra:candidate \
  --release-dir dist/release/linux_amd64 --out evidence/local/compose-candidate.json
python3 scripts/packaging/image_repro.py --release-dir dist/release --arch amd64 \
  --reference-image cpra:candidate --out evidence/local/image-repro-candidate.json
python3 scripts/packaging/live_helm.py --image cpra:candidate \
  --helm /path/to/helm3 --upgrade-helm /path/to/helm4 --management \
  --kubeconfig /private/test-kubeconfig --context kind-cpra-packaging-test \
  --namespace cpra-candidate-test --access-mode ReadWriteOncePod \
  --storage-class TESTED_CSI_CLASS --out evidence/local/helm3-to4-candidate.json
```

The normal Compose harness requires clean frozen release metadata. For an
explicitly prepared private working-tree candidate, `--diagnostic` exercises the
same runtime scenarios but records `working_tree_container` without a release
identity or a passing release gate. Preserve each attempt in a new output file.
Its generated monitor, runtime and token files are readable by UID 1001; the
production deployment must provision its own file permissions.

The Helm suite requires a new, disposable namespace and an already provisioned
test cluster. Load the candidate image into that cluster first. The initial
read-only deployment counts real target receipts around probes and `helm test`,
and tests delayed configuration availability, upgrade between Helm clients,
failed upgrade recovery, token rotation, a manifest larger than 1 MiB on a
separate read-only PVC, restart, stopped full-directory copy, headless operation
and PVC retention. Its 10,001-monitor file has one active monitor; this tests
installation input delivery, not fleet performance.

`--management` then creates a separate managed deployment and state claim in
the same disposable namespace. It provisions fixture-only immutable Secrets
outside Helm values, checks actual UID 1001 ownership and `0700`/`0600` files,
verifies loopback and Service DNS TLS, creates an encrypted credential and a
disabled monitor, and applies a conditional edit. It requires rejection of stale
versions and unauthorized writes, retained catalog identities and committed
authority after restart, stopped read/operator token rotation, calibrated zero
probe effects, RWOP rejection of a second owner, and stopped encrypted-state
recovery with retained incident history. Both deployment modes test the Helm 3
to 4 upgrade when `--upgrade-helm` is supplied. The private release workflow
requires this managed phase for both Helm clients. Its evidence reader rejects
read-only reports, missing scenarios, changed artifacts and inconsistent
observations.

The copy scenario mounts the stopped source read-only, compares every copied
file digest, removes the copy pod before restarting an owner and verifies the
same identity, catalog resources and event IDs from a separate claim. This is
a trusted stopped complete-directory clone. It does not exercise the CLI's
authenticated backup manifest or its explicit-restore access reset; those have
separate native recovery checks. Harness contract tests, chart rendering and
host process tests do not establish Kubernetes execution. The actual managed
cluster campaign remains unverified until its complete runtime report exists.

When a test environment only supports `ReadWriteOnce`, pass that mode explicitly
and retain the resulting `not_verified` CSI/RWOP finding. Such a report cannot
qualify the RWOP release gate. The suite cleans up its namespace and therefore
its disposable claims; this cleanup is not the production uninstall procedure.

For reproducible CI, `scripts/packaging/install_csi_fixture.py` installs the pinned [Kubernetes CSI hostpath test driver](https://github.com/kubernetes-csi/csi-driver-host-path) only into a dedicated single-node `kind-cpra-*` cluster. The application chart keeps its normal security profile; the separate CSI fixture needs privileged node mounts inside that disposable cluster. Use `cpra-csi-hostpath` as the test storage class. The harness requires the scheduler to reject a second pod claiming the live RWOP volume and records the actual driver and filesystem. This qualifies that reference test environment, not a production storage service; run the same suite against the intended production CSI/filesystem combination before claiming support for it.
