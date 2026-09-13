---
title: Current release notes
description: Changes and verification for the CPRa current main source and the separately documented release and SDK candidates.
cpra_scope: main
---

> **Current main:** applies to the application at [`51a835a`](https://github.com/ziad-hsn/cpra/commit/51a835a29f2fb7af2e0301910042a52e308cbb24). See [version and availability](versions.md) for newer candidate work.


# Current release notes

These documentation pages describe source commit [51a835a](https://github.com/ziad-hsn/cpra/commit/51a835a29f2fb7af2e0301910042a52e308cbb24), published to `main` on **11 September 2026**. This is an OSS preview source snapshot, not a claim that a versioned binary release has been published.

## Documentation refresh · 13 September 2026

All reader guides are now canonical on main and synchronized to GitHub Pages. [Versions and availability](versions.md) separates current main from the newer durable, packaging and SDK candidates. The expanded docs include the management plan, full SDK references, four integration lessons and recorded qualification boundaries.

## Branding and appearance · 11 September 2026

The supplied CPRa identity, shared neutral/graphite palette and System/Light/Dark dashboard settings are included in main. Documentation preserves older saved theme choices. The [main verification run](https://github.com/ziad-hsn/cpra/actions/runs/34641274785) checked this source; the theme work added four regression tests, bringing the dashboard suite to 17.

## Runtime preview changes · 9 September 2026

- Runtime worker sizing again calls Erlang C and Allen–Cunneen with measured arrival and service variability.
- Queue segment transitions and overflow handling preserve accepted work and progress.
- Recovery admission, incident accounting, maintenance, and healthy-check verification were corrected.
- Driver deadlines, configuration compatibility, HTTP delivery responses, and required provider fields were tightened.
- TLS certificate warnings propagate into degraded status and yellow notifications without starting recovery.
- Dashboard, API, and CLI distinguish unavailable data, current health errors, and future check times.
- Metrics families, container packaging, dependency notices, and release verification were updated.

## Historical runtime-preview verification

The 9 September `a370969` runtime snapshot passed Go 1.25 and Go 1.27 formatting, vet, default race tests, and race tests with every optional driver tag. Dashboard build, type checks, lint, 13 tests, and dependency audit passed. The [GitHub verification run](https://github.com/ziad-hsn/cpra/actions/runs/34334452067) also completed successfully.

The release review included real process scenarios, authenticated browser checks, a running queueing-model experiment, container execution, and archive consistency checks. ARM64 binaries were cross-compiled and inspected, not executed.

Go scans found no affected reachable symbols or imported packages. Four advisories were present only in the required `golang.org/x/crypto` module's unused packages; this is not a claim that every module is advisory-free.

## Current boundaries

- One process owns a monitor configuration; there is no cross-instance coordination.
- Incident and delivery state is in memory and resets on restart.
- Per-monitor history is not retained.
- MongoDB SRV discovery and Kubernetes exec credentials are unsupported.
- Local provider fixtures do not establish production-account certification.
- No comparative performance benchmark or long-duration soak test was performed.
- The queueing model estimates mean latency, not a percentile or SLA guarantee.

[Start a local instance](tutorials/quickstart.md) · [Deploy with these limits](how-to/deploy-to-production.md)
