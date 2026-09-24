---
title: Candidate · Development guide
description: Candidate · Navigate the CPRa Go source, run meaningful verification, build optional drivers and maintain the documentation.
cpra_scope: release_candidate
---

> **Release candidate:** this page describes [`410fbfb`](https://github.com/ziad-hsn/cpra/commit/410fbfb0092d01277b3884cd04151c27443a4226), which is separate from `main`. See [version and availability](../versions.md).


# Development guide

Build the `codex/release-engineering` candidate with Go 1.25 or later. The release matrix includes Linux, macOS and Windows, each conditional on native execution evidence. Keep a focused regression case for changes that affect concurrency, parsing, deadlines, or externally visible behavior.

## Source layout

| Path | Responsibility |
| --- | --- |
| `main.go` | Flags, startup, listeners, and shutdown. |
| `internal/controller` | ECS ownership and pipeline lifecycle. |
| `internal/controller/systems` | Scheduling, result application, incident state, and snapshots. |
| `internal/loader/schema` | Monitor and integration configuration types and validation. |
| `internal/loader/streaming` | Bounded YAML/JSON loading and entity creation. |
| `internal/durable` | Committed state, event history, snapshots and stopped-store validation. |
| `internal/runtimeconfig` / `internal/platformpath` | Storage/SLO configuration and platform directories. |
| `internal/localadmin` / `internal/preflight` | Local ownership, services, backup/restore and offline validation. |
| `internal/verification` | Provider runners and evidence boundaries. |
| `charts/cpra` / `scripts/packaging` | Chart configuration, native/container packaging and operational checks. |
| `internal/jobs` | Check, recovery, and notification implementations. |
| `internal/queue` | Queues, observations, workers, and sizing. |
| `internal/alerts` | Alert policy and cooldown handling. |
| `internal/web` | Read-only snapshots, HTTP API, metrics, and embedded dashboard. |
| `internal/client` / `internal/cpractl` | API client and CLI. |
| `dashboard` | React dashboard source and tests. |
| `scripts/release` | Dashboard staging and distribution packaging. |

These are internal Go packages, not a separately versioned public SDK. Link to concrete source when discussing a type or behavior rather than assuming a stable external import contract.

## Verification

~~~sh
make check
make test-all-drivers
make dashboard-build dashboard-check
~~~

`make check` runs formatting, vet, and default race tests. The all-driver target includes every optional integration tag. Dashboard verification includes its build, TypeScript, lint, and tests.

The release workflow checks Go 1.25.0 and the pinned Go 1.27.1 release compiler and scans default and all-driver Go imports for reachable vulnerabilities. Local provider fixtures establish protocol behavior; they do not certify live customer accounts or production recovery permissions.

## Contributions

Report bugs with a source commit, build tags, reproducible steps, and a redacted manifest. For code changes, explain the behavior before and after and the evidence that the affected path works.

CPRa remains free under MIT. Financial support is intended to be voluntary donations; it does not create a paid feature tier or a promised response-time commitment.

[Adding a driver](how-to/adding-new-jobs.md) · [Documentation maintenance](maintaining-docs.md)

[Recorded build and publication contract](release-engineering.md) · [Native operations](native-installation.md) · [Container and Helm operations](container-helm.md)

The later [Go SDK candidate](../sdk/index.md) moves the client into independent modules and uses a temporary workspace while those modules are unpublished. It is not included in the pinned release candidate described here.
