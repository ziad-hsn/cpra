# Release engineering

CPRa distributes one source module, two command-line programs, six native archives, four Linux packages, a two-platform Linux OCI image, production Compose configuration and an OCI Helm chart. A build or a cross-compilation alone does not establish platform support. The release workflow requires the corresponding native execution, installation, storage and deployment evidence before publishing an installable version tag.

Provider account verification and the million-monitor, 24-hour campaign are separate gates. Packaging tests must not be described as provider certification, a capacity result, an SLA or distributed failover evidence.

## Source and build identity

The canonical module is `github.com/ziad-hsn/cpra`. Its `go.mod` retains `go 1.25.0`; official binaries use **Go 1.27.1**. A separate minimum-compiler job tests Go 1.25.0. Source compatibility does not extend Go's upstream security-support period. Compiler upgrades require review of the recipe, recorded archive checksums and compatibility results. See the [Go release policy](https://go.dev/doc/devel/release#policy).

`scripts/release/recipe.json` is the build contract and `.goreleaser.yaml` matches it. The release recipe uses:

- Normal optimization, `-trimpath`, `-mod=readonly`, `-pgo=off`, `-buildvcs=false` and bounded build parallelism (`-p=2`).
- `CGO_ENABLED=0`, `GOAMD64=v1` and `GOARM64=v8.0`.
- Symbols and DWARF retained: no `-s`, `-w` or executable compression.
- `GOENV=off`, `GOWORK=off`, `GOTOOLCHAIN=local`; ambient `GOFLAGS`, experiments, compiler and module-policy overrides are discarded.
- All optional drivers, with Linux-only systemd capability excluded on other operating systems by build constraints.

Race/coverage instrumentation belongs to verification builds. Neither `netgo`, `osusergo`, external-linker flags nor an unrepresentative PGO profile is a general release optimization. Refer to [Go build flags](https://pkg.go.dev/cmd/go), [linker options](https://pkg.go.dev/cmd/link), [toolchain selection](https://go.dev/doc/toolchain) and [PGO guidance](https://go.dev/doc/pgo).

Release preparation requires a clean committed tree. It generates `RELEASE.json` from that commit, recording the application version, full source commit, source UTC timestamp and epoch, target matrix, compiler archive hashes, build recipe, dashboard digest, storage-format version, chart version, package version/revision and source-file inventory. Source inputs are checked again before building. The recipe injects CPRa's actual `internal/version` variables with the canonical module path. The displayed date is labeled **source date**, not compilation time.

Official builds deliberately omit automatic `vcs.*` fields. The explicit identity and signed provenance bind their source. Ordinary `go build` and `go install` use Go's `runtime/debug.ReadBuildInfo` fallback: a module version alone does not invent a commit.

## Installable source and embedded dashboard

Use the same published version for both programs:

```sh
go install github.com/ziad-hsn/cpra@VERSION
go install github.com/ziad-hsn/cpra/cmd/cpractl@VERSION
```

Replace `VERSION` with an actual published tag, such as `vMAJOR.MINOR.PATCH`. Plain installation retains the default driver subset. To match prebuilt driver coverage:

```sh
go install -tags 'redis postgres mysql mongo rabbitmq kafka kubernetes aws systemd teams twilio' github.com/ziad-hsn/cpra@VERSION
go install -tags 'redis postgres mysql mongo rabbitmq kafka kubernetes aws systemd teams twilio' github.com/ziad-hsn/cpra/cmd/cpractl@VERSION
cpra -capabilities
cpra -validate -yaml /absolute/path/monitors.yaml
```

The dashboard, starter configuration and local service templates are embedded. End users need no checkout, Node, pnpm or generation step. `scripts/release/go_install.py` exercises both installation variants outside a checkout with only Go on `PATH`, using a source-derived module proxy before a public tag exists. That evidence is explicitly distinct from public-proxy retrieval. This follows the [versioned go install contract](https://go.dev/ref/mod#go-install).

Frontend development uses the exact Node **24.21.0** and pnpm **11.22.0** recorded in the recipe and the frozen lockfile. `dashboard_build.py --check` refuses local dotenv files, discards ambient frontend build variables, builds and tests, then requires generated assets and license notices to match committed files. Provider credentials never become frontend inputs. [Vite environment handling](https://v6.vite.dev/guide/env-and-mode) explains why values exposed at build time can become public browser content.

## Reproduce unsigned distributions

The release builder needs Python 3.12+, the selected Go distribution, and the checksum-pinned GoReleaser OSS/nFPM tools. Node/pnpm are needed only to verify the committed dashboard, not to rebuild a shipped source archive.

```sh
python3 scripts/release/install_go.py --version go1.27.1 --out /absolute/new/go
export PATH=/absolute/new/go/bin:$PATH
python3 scripts/release/install_tools.py
python3 scripts/release/dashboard_build.py --check
make release-check check test-all-drivers
make release-prepare VERSION=vMAJOR.MINOR.PATCH
make release-build-goreleaser
make release-package
python3 scripts/release/sbom.py --syft bin/release-tools/syft
python3 scripts/release/release.py verify
```

Official artifacts are staged in `dist/release/OS_ARCH/`. Container images consume those exact staged executables. `scripts/release/release.py build --go /absolute/go/bin/go` provides the equivalent direct Go recipe for source reconstruction.

Source archives come from `git archive` of the approved commit plus explicitly generated `RELEASE.json`. They preserve executable modes and include build scripts, schemas, examples, notices, embedded templates and dashboard assets. Untracked workstation files are not selected by extension. Changes to `go.mod` or `go.sum` during a release build fail the build.

The full reproducibility gate compares the checkout's unsigned executables, native archives and DEB/RPM files with **two separate extractions** of the distributed source archive:

```sh
python3 scripts/release/source_rebuild.py \
  --source dist/release/cpra-vMAJOR.MINOR.PATCH-source.tar.gz \
  --stage dist/release --go /absolute/go/bin/go \
  --nfpm /absolute/path/bin/release-tools/nfpm \
  --out dist/release/reproducibility.json
```

All six targets are compared by default. A selective local run must list its targets and cannot satisfy the complete release gate. Signing timestamps/certificates are verified separately and are not part of unsigned-byte reproducibility. The chart packager also normalizes its tar headers to source time after staging the tested immutable image digest.

For an intentional dirty-tree experiment, `make release-prepare RELEASE_CANDIDATE=--candidate VERSION=v0.0.0-local.1` creates visibly marked candidate metadata. Candidate outputs cannot produce an official source archive or pass publication.

## Native packages and services

The output matrix is Linux/macOS tar archives and Windows ZIP archives for `amd64`/`arm64`; DEB and RPM are built for both Linux architectures. MSI, PKG, Homebrew, Scoop, WinGet and hosted apt/yum repositories are not provided by this release machinery. Sigstore signatures do not imply Apple notarization or Windows Authenticode.

Read [native installation](native-installation.md) for platform paths, installer ownership, service administration and stopped backup/restore. Linux system packages install administrator-owned binaries under `/usr/bin`, configuration under `/etc/cpra`, and state under `/var/lib/cpra`. A Linux user service uses absolute XDG locations with home-directory fallbacks; a system service does not put state in the installing user's home. macOS and Windows use their platform application-data locations. Explicit data-directory flags and runtime settings retain precedence.

The packaged systemd unit uses `Type=notify`, a dedicated account, state mode `0700`, `KillMode=mixed`, bounded restart rate, a 45-second application shutdown budget and a 60-second stop allowance. Installation leaves the service stopped. Host recovery permissions, credential files, SDK profiles and socket access require deliberate operator configuration under the actual service identity. Package hooks never grant Docker or systemd recovery authority or copy the installing user's credentials.

DEB and RPM share file definitions but have separate lifecycle scripts. Upgrades stop a running old process before file replacement and restart only if it was running. A stopped service remains stopped; upgrades preserve enabled state. Existing `cpra` accounts must have a non-root UID and the matching non-root primary group; package hooks fail rather than changing an incompatible identity. RPM's removal of the old package during an upgrade does not disable the replacement. SemVer prereleases map to a `~` prefix followed by an order-preserving identifier encoding; package revision is recorded separately. For example, tag `v1.2.3-rc.1` maps to package version `1.2.3~bfkevaa1a`. The original tag stays in archive filenames, application identity and `RELEASE.json`. This encoding also preserves arbitrary SemVer cases such as `a.1 < a-1`, where copying the raw suffix would invert Debian/RPM ordering. The exact package-manager version is recorded in `RELEASE.json`.

Edited package configuration is preserved through conffile/noreplace semantics. Debian scripts never rewrite a declared conffile. Removal retains durable state and the service identity. Before Debian removal, an administrator-private configuration recovery copy is kept in `/var/backups/cpra/package-config`; it survives a later purge. This credential-bearing copy is **outside** the Raft state directory and must be handled as secret configuration. Debian purge still applies Debian's normal conffile deletion; use the explicit recovery copy if reinstalling after purge. RPM retains edited configuration as `.rpmsave`. Neither removal nor purge recursively deletes durable state. Data deletion is a separate operator action. See [Debian conffile policy](https://www.debian.org/doc/debian-policy/ch-files.html#configuration-files) and [RPM scriptlet semantics](https://github.com/rpm-software-management/rpm/blob/master/docs/man/rpm-scriptlets.7.scd).

The lifecycle harness uses actual package managers and booted systemd only on explicitly isolated test hosts. It covers fresh installation, edited configuration, ready service, running/stopped updates between distinct versions, prerelease ordering and prerelease-to-final installation, enabled/disabled preservation, remove, reinstall and purge/recovery copies. The package-version fixtures reuse the candidate executable to test package-manager ordering and hooks; they do not establish compatibility between different storage-writing binary versions. A mocked `systemctl` test is a script contract test and cannot be reported as native service evidence.

## Container, chart and operational gates

Read [container and Helm operations](container-helm.md). Production images use the tested release executables, UID/GID `1001:1001`, certificates, notices and `cpractl`; developer builds have a separate Dockerfile. Compose uses a stable volume, read-only configuration, loopback exposure and bounded logs. No fixture tools or host-control privileges are part of the universal image. The deterministic `cpra-vMAJOR.MINOR.PATCH-compose.tar.gz` bundle contains Compose configuration, runtime settings, a versioned image example, identity and operating instructions. Its README commands run directly from the extracted bundle; the default image can be replaced with the verified release digest from `OCI.json`. Credentials and monitor manifests are supplied separately by the operator.

Helm creates one StatefulSet owner with retained storage; maintenance selects zero owners. Large manifests come from a pre-populated read-only configuration volume, not Helm values or oversized Kubernetes objects. API token rotation uses a new immutable Secret reference and an intentional rollout. The chart has no automatic backup hooks, live-copy guarantee, HPA, fake reload operation or multi-node failover claim.

Both Helm 3 and 4, including a 3-to-4 upgrade, are exercised. The release gate requires real RWOP contention evidence on the selected CSI fixture, in addition to chart rendering. An RWO local-path test remains useful but cannot pass the default RWOP gate. A test CSI hostpath driver is not production storage certification; record and retest the actual filesystem/storage class/CSI stack before claiming it.

Before replacement: validate the candidate, stop admission and the owner, take a complete stopped backup, replace the artifact, start against the same directory, and verify recovery. The backup includes node identity, Raft database, snapshots, history catalog and retained segments. Keep matching configuration/artifact identity and credentials under the separate documented backup procedure. Rollback requires storage-format compatibility and does not undo committed data or external interventions.

## Publication and verification

The release workflow is manually dispatched with a candidate version and the documentation ref whose source metadata matches the exact source commit. `publish=false` builds and verifies without creating a release tag. Publication requires reviewed `main`, passing compatibility/native/service/package/container/chart/documentation jobs, and the protected `release` environment. Configure required reviewers, release/tag restrictions and the narrowly scoped `RELEASE_ADMIN_READ_TOKEN` secret used to read the repository's immutable-release setting. The ordinary job token handles contents/packages/attestations; pull-request jobs have no publication credentials. [GitHub's workflow security guidance](https://docs.github.com/en/actions/how-tos/security-for-github-actions/security-guides/security-hardening-for-github-actions) covers these boundaries.

Do not manually pre-create a stable tag. It becomes installable through Go independently of whether a GitHub Release is still a draft. The workflow first assembles signatures and evidence, creates a non-SemVer validation draft, downloads it and verifies checksums, signer identity, issuer, source commit and provenance. Only then does it create the version tag and final draft, verify the final downloads, and publish. Immutable releases must be enabled; published bytes/tags are never overwritten. Corrections use a new version. Image aliases and the chart are promoted from the exact tested payloads. The chart version is a separate immutable version: bump `charts/cpra/Chart.yaml` before releasing any changed chart or image binding, including an application prerelease-to-final transition. Authenticated registry checks reject an existing application or chart version before creating the stable Git tag; network or authorization uncertainty fails the gate. A partially failed publication remains a failed release operation; do not hide it by overwriting artifacts.

A release includes source/native/package/Compose payloads, `RELEASE.json`, checksums, target-specific dependency inventories and SPDX SBOMs, executed evidence, signature/provenance bundles and a trusted-root snapshot. These establish different properties: inventory, integrity, builder/source provenance and observed runtime behavior.

Verify downloaded assets before execution:

```sh
sha256sum --check SHA256SUMS
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/ziad-hsn/cpra/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS

gh attestation verify cpra-vMAJOR.MINOR.PATCH-linux-amd64.tar.gz \
  --repo ziad-hsn/cpra --signer-workflow ziad-hsn/cpra/.github/workflows/release.yml \
  --source-digest FULL_SOURCE_COMMIT --source-ref refs/heads/main \
  --bundle provenance.sigstore.json
```

For offline verification, bring the asset, bundle, GitHub CLI and independently trusted roots into the offline environment. Obtain current trusted roots on a trusted connected machine with `gh attestation trusted-root > trusted_root.jsonl`, then add `--custom-trusted-root trusted_root.jsonl` to the verification command. A root file shipped beside an unverified artifact is not an independent trust anchor. See [GitHub's offline verification procedure](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/verify-attestations-offline).

Published support claims must link to the completed evidence for the same source commit. Cross-builds, native execution, mocked protocols, provider accounts and long-duration performance runs are separate evidence classes; missing evidence remains an outstanding gate.

## Mainstream reference implementations

The implementation applies upstream contracts to CPRa's ownership/storage model. [Caddy's GoReleaser configuration](https://github.com/caddyserver/caddy/blob/master/.goreleaser.yml) informs explicit recipes and native packaging; [Prometheus's build configuration](https://github.com/prometheus/prometheus/blob/main/.promu.yml) illustrates source identity and artifact inventories; [Tailscale's Windows service integration](https://github.com/tailscale/tailscale/blob/main/cmd/tailscaled/tailscaled_windows.go) illustrates native SCM lifecycle; and [etcd's released-asset verification](https://github.com/etcd-io/etcd/blob/main/.github/workflows/verify-released-assets.yaml) demonstrates verification of distributed executables. These projects are references, not claims that CPRa inherits their deployment or durability guarantees.
