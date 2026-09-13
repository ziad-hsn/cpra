---
title: Candidate · Installation and builds
description: Candidate · Install CPRa through Go, native archives, Linux packages, containers, or Helm with explicit persistence and verification.
cpra_scope: release_candidate
---

> **Release candidate:** this page describes [`410fbfb`](https://github.com/ziad-hsn/cpra/commit/410fbfb0092d01277b3884cd04151c27443a4226), which is separate from `main`. See [version and availability](../../versions.md).


# Installation and builds

These pages describe the release-engineering candidate. The exact source revision
appears in the footer and `site-version.json`; use that revision when reproducing
this documentation snapshot. Native execution gates qualify each distribution.

## Install with Go

Replace `VERSION` with an actual published version from a verified release:

~~~sh
go install github.com/ziad-hsn/cpra@VERSION
go install github.com/ziad-hsn/cpra/cmd/cpractl@VERSION
cpractl local paths
cpractl local init
cpra -capabilities
~~~

Go 1.25 source compatibility remains separate from the supported compiler used
for official binaries. Installation includes the committed dashboard and embedded
starter files; no Node, pnpm, Git or generation step is needed. Initial local
configuration is empty and does not enable provider operations.

See [native installation](../native-installation.md) for all applicable driver
build tags, platform paths, service identities and stopped backup/restore.
Plain `go install` retains the default driver subset. The systemd recovery driver
is Linux-only. `cpra -validate` explains missing compiled drivers before opening
storage or constructing provider clients.

## Build from source

~~~sh
git clone --branch codex/release-engineering https://github.com/ziad-hsn/cpra.git
cd cpra
git checkout --detach 410fbfb0092d01277b3884cd04151c27443a4226
make
./bin/cpra -version
./bin/cpractl --version
~~~

For a reproducible release, check out the approved full commit from the release
metadata and follow the [recorded release recipe](../release-engineering.md).
Official artifacts use Go 1.27.1, normal optimization, retained symbols/DWARF,
trimmed source paths and explicit source identity. `make` remains a developer
build using Go's module/VCS metadata.

## Distribution routes

| Route | Outputs |
| --- | --- |
| Native archives | Linux/macOS tar.gz and Windows zip, amd64 and arm64 |
| Linux packages | DEB and RPM, amd64 and arm64 |
| Containers | Linux amd64/arm64 OCI image from the same staged executables |
| Deployment | Production Compose and separately versioned OCI Helm chart |
| Source and evidence | Complete source archive, checksums, notices, SBOMs and provenance |

A cross-build alone does not qualify an OS or architecture. Follow the release's
native verification report. Version placeholders do not imply an existing tag
or GitHub Release, and Sigstore signing does not imply Authenticode/notarization.

## Dashboard and images

The [release guide](../release-engineering.md) records the pinned Node/pnpm
versions and clean dashboard comparison. Provider credentials and developer
`.env` files are not frontend build inputs.

The production Dockerfile consumes staged release binaries. Source development
uses `docker/Dockerfile.dev`. Prepare persistent storage and authentication using
[Compose and Helm operations](../container-helm.md) before starting an instance.
