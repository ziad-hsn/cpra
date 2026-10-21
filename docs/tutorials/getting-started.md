---
title: Installation and builds
description: Build matching source, inspect compiled drivers and select a qualified distribution.
cpra_scope: development
---

# Installation and builds

[Versions and availability](../versions.md) is the current support matrix.
A build recipe does not establish that its public release, module tag, service
or native platform has been qualified. Use matching source, metadata and guides.

## Build from source

Use Go 1.26.10 or Go 1.27.3 (or a newer supported, patched release), Make and
Python 3 in the selected checkout. See [compiler compatibility](../versions.md).

~~~sh
make
./bin/cpra -version
./bin/cpractl --version
./bin/cpra -capabilities
~~~

Make uses a local SDK workspace while module candidates are unpublished. It
respects an explicitly selected GOWORK. The dashboard assets are already
committed; this build does not require Node or frontend generation.

Start with [protected management initialization](quickstart.md), which creates
named access and localhost TLS. Services receive explicit absolute configuration
and state paths. Foreground user defaults follow platform conventions; inspect
them with cpractl local paths. Existing working-directory state requires an
explicit stopped migration.

## Include optional drivers

~~~sh
make BUILD_TAGS='redis postgres kubernetes'
~~~

Optional checks are redis, postgres, mysql, mongo, rabbitmq and kafka.
Optional recovery tags are kubernetes, aws and Linux-only systemd. Optional
notification tags are teams and twilio. A2A is built in. Capability inspection
and static validation reject unavailable compiled/platform drivers before
opening storage or contacting targets. See [driver fields](../reference/jobs-reference.md).

The externaljobs extension is separate: it requires its own build tag, explicit
runtime enablement and scoped credentials. Full built-in-driver builds do not
enable it.

## Versioned Go installation

After the corresponding SDK/worker and application versions are qualified and
published, these are the module installation paths:

~~~sh
go install github.com/ziad-hsn/cpra@VERSION
go install github.com/ziad-hsn/cpra/cmd/cpractl@VERSION
~~~

Replace VERSION with a published immutable version. Plain installation retains
the default driver subset; select optional tags explicitly when needed. These
examples do not assert that the planned RC tags are already installable.

## Rebuild the dashboard

Use the repository-pinned Node and pnpm versions, Python 3 and the selected Go
compiler:

~~~sh
make dashboard-build dashboard-check
~~~

The frozen lockfile, parser generation, type/lint/tests and embedded asset
manifest form the rebuild contract. Provider credentials and developer
environment files must not be frontend build inputs.

## Archives, packages and deployments

The planned archive matrix is Linux/macOS/Windows amd64 and arm64. Linux packages
use DEB/RPM-specific lifecycle hooks; OCI images cover Linux amd64 and arm64.
[Release engineering](../release-engineering.md) records the reproducible
unstripped build recipe, metadata, notices, SBOMs and verification gates.

Follow [native installation](../native-installation.md) for administrator-owned
service executables, platform data locations and stopped backup/restore. Follow
[Compose and Helm](../container-helm.md) for retained volumes, one owner, secret
rotation, safe probes and file-backed large manifests. Credentials and host
control privileges require explicit configuration.
