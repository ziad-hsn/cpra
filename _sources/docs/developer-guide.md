---
title: "Development guide"
description: "Navigate the CPRa Go source, run meaningful verification, build optional drivers and maintain the documentation."
---

# Development guide

Build the `main` branch with Go 1.25 or later. Linux is the target of the current packaged distributions. Keep a focused regression case for changes that affect concurrency, parsing, deadlines, or externally visible behavior.

## Source layout

| Path | Responsibility |
| --- | --- |
| `main.go` | Flags, startup, listeners, and shutdown. |
| `internal/controller` | ECS ownership and pipeline lifecycle. |
| `internal/controller/systems` | Scheduling, result application, incident state, and snapshots. |
| `internal/loader/schema` | Monitor and integration configuration types and validation. |
| `internal/loader/streaming` | Bounded YAML/JSON loading and entity creation. |
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

The release workflow checks Go 1.25 and 1.27 and scans default and all-driver Go imports for reachable vulnerabilities. Local provider fixtures establish protocol behavior; they do not certify live customer accounts or production recovery permissions.

## Contributions

Report bugs with a source commit, build tags, reproducible steps, and a redacted manifest. For code changes, explain the behavior before and after and the evidence that the affected path works.

CPRa remains free under MIT. Financial support is intended to be voluntary donations; it does not create a paid feature tier or a promised response-time commitment.

[Adding a driver](how-to/adding-new-jobs.md) · [Documentation maintenance](maintaining-docs.md)
