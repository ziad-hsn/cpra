---
title: Current development changes
description: Implemented CPRa repair checkpoints and the qualification still required before its first release candidate.
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
  distributions are not retained.
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
  Recovery and maintenance cancel obsolete unsent work. Intervention completion
  reports health verification pending; restored health requires the configured
  successful-check threshold. Ambiguous delivery does not authorize replay.
- Current observations expose bounded safe failure causes, queue saturation and
  measured five-minute latency distributions. Erlang C/Allen–Cunneen remains
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

Focused Go, real-process recovery, local network, frontend and required browser
checks have passing repair evidence, with independent review of finite
checkpoints. An earlier broader application run failed report/fixture checks
and exhausted its locally shortened persistence timeout. Corrected focused
checks and a documented-budget persistence rerun remain separate evidence.
These results do not establish a final frozen-source release matrix.

The release recipe now uses Go 1.27.2, gRPC 1.83.2 and `x/net v0.60.0` following
vulnerability scans. All repository Go modules share a Go 1.26.0 minimum.
Fresh scans across nine application, SDK, worker and example selections report
no affected imported packages or called symbols. The all-built-in application
selections retain one module-level advisory for OpenPGP packages they do not
import. Complete final-source security qualification remains required. Earlier scan records retain their own source identity; later repairs require new scans.

Publication still requires final source review/freeze, complete compiler/tag
and vulnerability checks, reproducible packaged artifacts, native platform and
service lifecycle tests, both image architectures, Compose/Helm storage and
upgrade tests, actual provider/agent effects, and comparative capacity evidence.
Native runner, Kubernetes and provider-account infrastructure is not configured.
Local fixtures and sandbox acceptance do not establish observed production
delivery or provider certification. The comparative million-monitor campaign
has not completed; its disk prerequisite still applies. The 24-hour endurance
run remains cancelled.

The repository retains individual pass/fail records and open work. No public tag, module, artifact,
image, chart or documentation promotion is implied by this page.
