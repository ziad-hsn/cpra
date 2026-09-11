# Durable CPRa implementation status

Base: `a370969b041b399c0778318d8915ce059fd74294`; branch: `codex/durable-cpra`.

The eight adjacent tickets follow the approved dependency order. This is an
implementation candidate. The complete release gate is **open**.

| Ticket | Implemented | Evidence / outstanding work |
| --- | --- | --- |
| 1 Storage | Default Raft, versioned state, identity, locking, snapshots, explicit memory mode | Restart, forced-kill, corruption, format, snapshot, backup and race checks; Go 1.25 compatibility. |
| 2 Lifecycle | Deterministic commits, per-endpoint intent/start/results, unknown holds, configuration reconciliation | Real subprocess crash boundaries and real controller restart regression checks. |
| 3 History | Daily indexed bbolt segments, 30-day retention, compaction watermark, bounded cursor pages | Replay, retention, concurrent insert pagination, missing-segment and complete backup checks. |
| 4 SLO | Bounded driver histograms, exact counters, persisted coverage, cadence obligations, model plus feedback | Distribution, unfinished/missed work, timeout, restart and control-response tests. Full workload targets remain unverified. |
| 5 Interfaces | Read-only state/history/SLO API and CLI; incremental fleet index; dashboard timeline/status | Auth/route/page tests, real browser and API/CLI agreement; 15 dashboard tests/build/lint/type checks. |
| 6 Providers | 33-case config runner, effect/receipt observers, disposable fixture setup, evidence files, manual workflow | Six local driver scenarios passed. Remaining 27 have no passing live evidence. Actual database images, privileged systemd, designated Kubernetes/cloud accounts and receipt readers remain prerequisites. |
| 7 Scale | Pinned matched-build preparation, physical-disk gate, 10k/100k/1m comparison, fault and 24-hour harness | Ten-monitor harness smoke only. The physical host has approximately 4 GB free and fails the required 30 GiB plus fixture allowance. Large campaign blocked before fixture creation. |
| 8 Release | Persistent deployments, backup docs, workflow entry points, dependency notices and documentation candidate | Local packaging and docs-link checks. Independent review, completed provider/performance/endurance evidence and publication to main/public gh-pages remain outstanding. |

No ticket's independent-review checkbox is marked complete. The implementation
was checked and revised by its author; that is not an independent review.

Local evidence is under `evidence/local/` and excluded from source archives and
Git. It includes failed fixture attempts as well as successful runs. Component
checks and short harness runs cannot satisfy the million-monitor or full-provider
gates. No subscriptions, infrastructure purchases, user disk cleanup or unrelated
service changes are part of this work.
