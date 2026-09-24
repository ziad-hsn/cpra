---
title: Plan and status · Candidate author review
description: Plan and status · Candidate author review for the reviewed CPRa source; see the version and availability notice.
cpra_scope: plan
---

> **Candidate design and evidence:** this record describes unreleased implementation work or an approved plan. Its checklists do not establish availability in `main`. [Version and availability](../versions.md) identifies the earlier 13 September reviewed snapshots; [current implementation status](STATUS.md) records the later branch checkpoints.

# Candidate author review

The review compared the approved requirements with changed code, tests,
configuration, operator views and harness behavior. This is an author review,
not an independent sign-off.

## Corrections made during review

- Restored empty action/cooldown maps safely after a snapshot of a healthy monitor.
- Cancelled unsent actions when a monitor is removed or its revision changes.
- Rechecked maintenance at dispatch and execution and rescheduled deferred work.
- Allowed observed healthy recovery to cancel an intervention that never started.
- Kept pre-intervention check generations from invalidating verification.
- Recorded scheduled obligations before admission and counted unfinished work
  against threshold attainment without waiting for its completion.
- Removed whole-window copies from each histogram observation; frozen copies
  remain at the snapshot boundary. The recorder microbenchmark measured zero
  allocations per observation on this host.
- Redacted arbitrary observer text and retained only numeric/boolean evidence,
  resource fingerprints and source hashes in private evidence artifacts.
- Verified complete backup restoration, including the retained event catalog.
- Preserved numeric API routes, bounded pages, authentication and actual latency
  availability in the dashboard.

## Evidence boundaries

Real subprocess crash tests and six local live-driver scenarios passed. The
browser timeline agreed with API/CLI events. Default/all-driver Go checks,
Go 1.25 compatibility and dashboard checks passed. Local AMD64/ARM64/source
archives include dependency notices and exclude data directories, credentials,
node_modules and local campaign evidence. ARM64 was cross-compiled, not executed.

Provider-account readers, systemd privileges and designated Kubernetes/AWS
resources are not configured. Database fixture downloads and large campaigns
failed their physical-disk prerequisite before starting. The million-monitor
rate, p99 targets at that rate, 24-hour endurance and resource stabilization
remain unverified. The harness preserves failed and incomplete outcomes.

## Release decision

Keep the candidate application separate from published main until
independent review and the required provider, comparative and endurance evidence
are complete. No full-provider certification, production-capacity claim, HA
claim or unconditional SLA is justified by the available evidence.

The candidate documentation is now published for review alongside the main
documentation. That publication does not close the application release gates above.
