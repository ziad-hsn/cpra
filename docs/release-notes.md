---
title: Current development changes
description: CPRa configuration, monitoring, notification, storage and operator-interface behavior.
cpra_scope: development
---

# Current development changes

CPRa's first versioned release candidate remains private and unpublished. The
target is `v0.1.0-rc.1` for the application, Go SDK and worker module, and
`0.1.0-rc.1` for the Helm chart. [Versions and availability](versions.md) is the
current support matrix. Historical engineering records remain in the repository
and are excluded from the public website.

## Implemented behavior

- A single owner commits monitor, incident and action state through Raft by
  default. Restart resumes safe work and holds interrupted actions whose
  external outcome is unknown. Memory mode is explicit. This does not provide
  cross-instance coordination or failover.
- Incident and action events are retained for 30 days, including notification
  endpoint outcomes. Successful raw checks and historical per-monitor latency
  distributions are not retained. Ordinary dependency-free completed action records
  are retained for 90 days; unknown actions, review evidence and their required
  dependencies are protected. Logical retirement and physical reclamation are
  reported separately.
- Authenticated management supports configuration collections, monitor editing,
  recipients, groups and protected credentials. The API, Go SDK, CLI and
  dashboard expose the same resource/version contracts. Acknowledge records
  attention; dismiss suppresses incident notifications; snooze pauses checks
  and notifications for its period; disable excludes the monitor until enabled.
  Ignore continues health observations while suppressing ordinary notifications
  and interventions. Verified recovery clears Ignore; gray notices describe
  actual enable/disable transitions, including during Ignore and maintenance.
- Startup imports configured management manifests once. Later file edits require
  explicit apply. Interrupted collection application retains its original
  operation and requires explicit continuation with the original inputs.
- Notification intent has stable episode, endpoint and delivery identities.
  New conditions remain pending while relevant; their execution budget begins
  with the first committed start. Ordinary built-in notifications have 45 seconds
  across at most three attempts. Recovery and maintenance cancel obsolete unsent
  openings, while relevant closures retain their original destinations. Intervention completion
  reports health verification pending; restored health requires the configured
  successful-check threshold. Ambiguous delivery does not authorize replay.
- Current observations expose bounded safe failure causes, queue saturation and
  measured five-minute latency distributions. Local preparation failures make
  current health unknown and monitoring unavailable, with the previous observation
  and time shown separately. Erlang C/Allen–Cunneen remains
  the initial sizing model; observed percentile control is separate from a
  contractual SLA or an unconditional capacity claim.
- The gRPC check supports explicit unary health mode with verified TLS by
  default. Only `SERVING` is healthy. Omitting the mode preserves TCP
  reachability. TLS-capable checks support explicit per-monitor certificate
  verification bypass, off by default, with isolated transport settings. This
  does not disable recovery health verification or CPRa API authentication.
  Docker stop grace and overall operation budget are separate.

See the [quickstart](tutorials/quickstart.md), [durability guide](durability.md),
[management controls](cpractl-controls.md) and
[provider verification guide](provider-testing.md) for their operating contracts.

## Development evidence and remaining gates {#current-boundaries}

Development tests cover individual behavior and failure boundaries. Only evidence
bound to the final frozen source can qualify a release. Independent review,
required browser campaigns and the complete compiler/tag matrix remain required;
a focused pass does not replace these gates.

The release recipe uses Go 1.27.3, gRPC 1.83.2 and `x/net v0.60.0`. All repository
Go modules share a Go 1.26.10 minimum. The compiler pins include the certificate
verification correction for [GO-2026-6809](https://vuln.go.dev/ID/GO-2026-6809.json).
Go 1.27.0–1.27.2 remain affected even though they satisfy the module minimum.
Final-source security checks must use the patched compilers; earlier clean scans
predate this advisory. The separate module-level OpenPGP advisory in built-in
driver dependency graphs remains recorded without an imported package or called
symbol. Updating the compiler does not resolve that separate disposition.

Publication still requires final source review/freeze, complete compiler/tag
and vulnerability checks, reproducible packaged artifacts, native platform and
service lifecycle tests, both image architectures, Compose/Helm storage and
upgrade tests, actual provider/agent effects, and comparative capacity evidence.
Native runner, Kubernetes and provider-account infrastructure is not configured.
Local fixtures and sandbox acceptance do not establish observed production
delivery or provider certification. The comparative million-monitor campaign
has not completed; its disk prerequisite still applies.

The repository retains individual pass/fail records and open work. No public tag, module, artifact,
image, chart or documentation promotion is implied by this page.
