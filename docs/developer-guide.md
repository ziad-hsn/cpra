---
title: Development guide
description: Navigate the current CPRa modules, run source verification and distinguish private development checks from release qualification.
cpra_scope: development
---

See [versions and availability](versions.md) for the current source and open
qualification gates.


# Development guide

CPRa preserves Go 1.25 source compatibility. The release recipe pins Go 1.27.2;
minimum-version tests are a separate check. Before nested-module publication,
Make uses a command-local workspace for the application, SDK and worker modules.
That workspace supports development; it does not prove public module downloads
or qualified native distributions.

## Source layout

| Path | Responsibility |
| --- | --- |
| `main.go` | Flags, startup, listeners, and shutdown. |
| `internal/controller` | ECS ownership and pipeline lifecycle. |
| `internal/controller/systems` | Scheduling, result application, incident state, and snapshots. |
| `internal/manifest` / `internal/manifestcheck` | Manifest types and static configuration validation. |
| `internal/loader` | Bounded YAML/JSON loading and entity creation. |
| `internal/jobs` | Check, recovery, and notification implementations. |
| `internal/queue` | Queues, observations, workers, and sizing. |
| `internal/persistence` | Raft state, history, command contracts and collection ledgers. |
| `internal/management` | Authenticated resources, conditional mutations and collection application. |
| `internal/httpserver` / `internal/fleetview` | HTTP interfaces, embedded dashboard and bounded observations. |
| `internal/cpractl` / `internal/localadmin` | SDK-backed CLI and local installation/backup administration. |
| `internal/logger` | Structured diagnostics used by the process and framework adapters. |
| `sdk/go` / `sdk/go/worker` | Independently versioned public client and tagged external-worker modules. |
| `dashboard` | React dashboard source and tests. |
| `scripts/release` | Recorded builds, distribution packaging and qualification evidence. |

Application internals are not SDK import contracts. Public consumers use the
[Go SDK](sdk/index.md); it does not import the controller, Raft or provider
execution libraries. Preserve the build-tag boundary for external jobs.

## Verification

~~~sh
make check
make test-all-drivers
make sdk-check
make dashboard-build dashboard-check
~~~

`make check` runs formatting, vet and default application race tests. The
all-driver target covers applicable built-in tags; it does not enable external
jobs. `sdk-check` explicitly verifies both nested modules and their tagged
variants. Root `go test ./...` does not traverse nested module boundaries.
Dashboard checks include build, types, lint, tests and embedded freshness.

Complete release matrices run against one frozen source with recorded tools,
flags, tags and outcomes. Local checks, cross-builds and fixtures retain their
own evidence scope; they do not establish native execution, actual account
effects or publication. Required browser campaigns fail when their tooling is
absent. The test budget is bounded; a timed-out run is incomplete evidence.

## Contributions

Report bugs with a source commit, build tags, reproducible steps, and a redacted manifest. For code changes, explain the behavior before and after and the evidence that the affected path works.

CPRa remains free under MIT. Financial support is intended to be voluntary donations; it does not create a paid feature tier or a promised response-time commitment.

[Adding a driver](how-to/adding-new-jobs.md) · [Documentation maintenance](maintaining-docs.md)
