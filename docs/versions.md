---
title: Versions and availability
description: Current implementation, verification boundaries and pending publication.
cpra_scope: development
---

# Versions and availability

This source is under private release-repair qualification as of 12 October 2026.
The intended first release is application/SDK/worker `v0.1.0-rc.1` and Helm chart
`0.1.0-rc.1`. These are planned versions, not proof that installable public tags,
assets or registry entries exist. Candidates remain access-controlled until the
agreed gates pass.

Published documentation, Git branches, SDK tags and release artifacts advance
separately. Use matching source and documentation; qualified artifacts carry
their source identity in release metadata. Current development checks are tied
to their actual source inventories. They are not a final frozen-release pass.

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
| Artwork and code | MIT code; CPRa-authored artwork under CC BY 4.0 with attribution | Third-party notices and the separate project-name note remain applicable |

The [quickstart](tutorials/quickstart.md) uses protected management initialization
and localhost TLS. Management imports bootstrap manifests once; subsequent
changes use explicit apply. Notification receivers are contacts, not login
identities. [Operator controls](management-controls.md) distinguish acknowledge,
dismiss, snooze and disable; there is no check-now command.

The [repair progress record](implementation/release-repair-progress.md) describes
completed local checks and remaining implementation work. The
[approved qualification plan](implementation/release-repair-plan.md) requires
independent reviews, final-source matrices, native/provider/deployment evidence
and the matched capacity campaign. No native runner fleet, private Kubernetes
environment or designated provider/agent accounts are configured yet. Missing
configuration is not a passing verification result.

The 24-hour endurance test is cancelled. The local idle CPU comparison is a
short diagnostic experiment, not the million-monitor performance gate.

Dated [historical version review](review/version-review-2026-10-01.md) and
[earlier candidate records](candidate/index.md) remain available for their exact
past scope. They do not describe the current setup instructions or qualify this
candidate. Current release documentation is rendered from the final release
metadata after source freeze; it must not infer an identity from an older pass.
