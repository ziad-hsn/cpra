---
title: Versions and availability
description: Current implementation, verification boundaries and pending publication.
cpra_scope: development
---

# Versions and availability

This source is under private release-repair qualification as of 20 October 2026.
The intended first release is application/SDK/worker `v0.1.0-rc.1` and Helm chart
`0.1.0-rc.1`. These are planned versions, not proof that installable public tags,
assets or registry entries exist. Candidates remain access-controlled until the
agreed gates pass.

Published documentation, Git branches, SDK tags and release artifacts advance
separately. Use matching source and documentation; qualified artifacts carry
their source identity in release metadata. Current development checks are tied
to their actual source inventories. They are not a final frozen-release pass.

## Current development source

| Area | Current implementation | Qualification boundary |
| --- | --- | --- |
| Process ownership | One controller and one single-node Raft owner per data directory; explicit memory mode | No multi-node failover or cross-instance monitor coordination |
| State after restart | Committed configuration, incidents, controls and action identities recover | Interrupted started actions are unknown and held, never automatically replayed |
| History | Incident/intervention/notification events retained for 30 days | No retained raw check stream or per-monitor percentile history |
| Management | Named authenticated monitor, endpoint, receiver, group and encrypted-secret CRUD; conditional controls and collection apply | Operator controls destinations; this is not multi-tenant destination isolation |
| Dashboard and CLI | Management forms and read-only observations; explicit original-operation continuation | Configuration admission and controller application are reported separately |
| SDK and workers | Separate Go modules; external workers require build tag, runtime enablement and scoped credentials | Public downloaded-module qualification and publication remain open |
| Check latency | Observed rolling distributions and threshold attainment; model plus bounded feedback | Estimates and measurements do not establish an unconditional SLA |
| Drivers | 14 checks, six recovery roles and 15 notification roles across applicable build tags | Fixture contracts, provider acceptance and observed real effects are distinct evidence levels |
| gRPC | TCP reachability when mode is omitted; explicit verified-TLS unary health mode | Only SERVING succeeds in health mode; it never falls back to TCP |
| Distributions | Go-install contract, archive/package/image/Compose/Helm recipes and local administration | Native OS/architecture, service, filesystem and deployment matrices remain required |
| Build compatibility | Common Go 1.26.10 minimum; release compiler Go 1.27.3 | Final-source compiler, driver-tag, vulnerability and reproducibility checks remain required |
| Artwork and code | MIT code; CPRa-authored artwork under CC BY 4.0 with attribution | Third-party notices and the separate project-name note remain applicable |

Use Go 1.26.10 or later in the 1.26 series, or Go 1.27.3 or later in the 1.27
series. The module minimum alone permits Go 1.27.0–1.27.2, which remain affected
by [GO-2026-6809](https://vuln.go.dev/ID/GO-2026-6809.json).

The [quickstart](tutorials/quickstart.md) uses protected management initialization
and localhost TLS. Management imports bootstrap manifests once; subsequent
changes use explicit apply. Notification receivers are contacts, not login
identities. [Operator controls](management-controls.md) distinguish acknowledge,
dismiss, snooze, disable and Ignore; there is no check-now command.

## Remaining release gates

A repair candidate needs independent source review, a frozen source identity,
complete application/SDK/worker compiler and build-tag checks, security scans,
real-server browser checks and reproducible distribution payloads. Packaged
binaries need native execution and service-lifecycle checks on each claimed
OS and architecture. Container and Helm qualification includes storage ownership,
backup/restore, upgrades and credential rotation on the tested storage stack.

No native runner fleet, private Kubernetes environment or designated
provider/agent accounts are configured yet. Actual provider effects and the
matched comparative capacity campaign remain unqualified. Missing configuration
is not a passing verification result. Short local isolation experiments do not
establish five-minute SLO attainment or million-monitor capacity.

The 24-hour endurance test is cancelled. GitHub's reporting-status API returned
`enabled: false` on 20 October 2026. Repository administration must enable private
vulnerability reporting and verify the reporting form before announcement; the
available connector does not expose that setting. Historical
engineering plans and evidence are preserved in the repository, outside the
public site and search index. Release documentation must use the final release
metadata; an earlier successful check does not qualify later source changes.
