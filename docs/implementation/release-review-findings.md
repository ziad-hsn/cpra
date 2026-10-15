# Release review finding inventory

Baseline: `2ba7b73f0c9d398bfee2fbcf3896f70ccc1fb37c`.
Opened: 2026-10-12, `codex/release-repairs`.
Authority: [approved repair plan](release-repair-plan.md).

The three supplied reviews are identified here as R1 (initial OSS review), R2
(candidate recheck and corrections), and R3 (instrumented collection-admission
reproduction). R1 inspected `b293f34` for some claims; R2/R3 inspect `2ba7b73`.
“Reproduced (R2/R3)” credits the supplied reviewer evidence. It is not a new
passing check or an independent sign-off for this branch. Original evidence and
reproductions are retained separately; no raw credentials belong in this record.

Current repairs and fresh local check outcomes are recorded in
[release repair progress](release-repair-progress.md). Baseline dispositions
below remain unchanged historical observations; they are not current completion
claims.

| ID | Finding | Baseline disposition and evidence | Repair / qualification |
| --- | --- | --- | --- |
| F01 | README first run requires an uncreated token; CLI/dashboard access fails | Reproduced (R2): v2 self/health return 401 without named authority. | Go-based management initialization, access discovery, executed quickstart. |
| F02 | Quickstart uses an older revision and missing overview command | Source-confirmed (R1); final documentation identity remains open. | One current support matrix and metadata-driven pages. |
| F03 | Notification cooldown defers repeats and stale red follows recovery | Reproduced (R2): green 22:59:59, stale red 23:04:11 on second outage. `transitions.go` schedules cooldown into the future. | Episode identity, suppress repeats, cancel obsolete queued work, restart tests. |
| F04 | Maintenance delays rather than suppresses alerts | Source-confirmed: notification queue ignores maintenance; start waits. | Suppress admission/cancel covered queued work; no deferred release. |
| F05 | Cyan says RESTORED before healthy verification | Reproduced (R2); retained notification reproduction also demonstrates it. | Verification-pending text until healthy threshold, active-path tests. |
| F06 | Check failure reason is discarded; misleading intervention wording | Source-confirmed: durable result retains outcome only; fixed job templates. | Bounded safe diagnostics across state/events/API/SDK/CLI/dashboard/alerts. |
| F07 | HTTP 500 is treated as definite retryable rejection | Reproduced (R2): receiver invoked three times. R3 corrects R2's initial “design choice” interpretation against durability policy. | Unknown hold; only proven provider rejection retries; stable identity/deadline. |
| F08 | Docker stop grace also bounds the whole recovery operation | Source-confirmed (R2): both use `Timeout`. | Separate operation budget, production-path timeout tests. |
| F09 | Manifest typos and incomplete Discord configuration validate | Reproduced (R2): enabeld, maintainance, unhealthy_treshold, methd, absent webhook. | Shared strict static validation, source attribution, no I/O. |
| F10 | Collection-dependent generations accumulate across restarts | Source-confirmed; R2 narrows scope to applied collections. Original 97 MB claim was not remeasured. | Store-owned pinned generation catalog and safe retirement, measured cycles. |
| F11 | Every start creates a ledger, even file-only/empty catalog | Original universal claim corrected (R2). A narrower streamed-snapshot failure is reproduced at `16569a7`: monitor recovery creates an unnecessary empty ledger. | Validate empty snapshot namespaces without disk allocation; preserve lazy first collection storage. See the [repair and scoped evidence](collection-empty-snapshot-2026-10-15.md). |
| F12 | Windows collection initial mapping expands file excessively | Source-confirmed dependency behavior; original “3 GiB per restart” is not native measurement. | 64 MiB initial mapping, native growth/snapshot/footprint evidence. |
| F13 | Large quantities of collection/worker code are unfinished | Corrected: old status/code-volume claim is not a capability test. Approved features stay. | Exact behavior inventory and final acceptance, no feature deletion. |
| F14 | Worker routes absent from normal startup | Corrected (R2): tagged and explicitly enabled routes are registered. | Default/tagged negative route and real-process regression matrix. |
| F15 | Worker module omitted from all tests | Corrected: nested module has explicit SDK/worker targets; root ./... alone is insufficient. | Final three-module compiler/tag matrix. |
| F16 | Reset formats 14/21 to version 1 | Rejected review recommendation: candidate formats are 38/39, historical readers and replay must survive. | Introduce reserved 40/41 without resetting state. |
| F17 | Anonymous loopback API accepts hostile Host (DNS rebinding) | Reproduced (R1); source-confirmed missing Host check. | Approved loopback Host/origin checks, browser/security tests. |
| F18 | No token-rotation command | Corrected (R2): named rotate and read-token rotation exist. | Regression coverage; no duplicate rotation mechanism. |
| F19 | Supplied restart bootstrap token silently ignored | Source-confirmed (R2): committed authority wins without warning. | Safe warning, restart without obsolete bootstrap files, no authority replacement. |
| F20 | pprof/cmdline discloses command arguments | Source-confirmed endpoint registration. | Remove endpoint, restrict profiling Host. |
| F21 | One-character token accepted; no auth throttling | Source-confirmed; length is not an entropy test. | 32 printable-character minimum, bounded failed-auth throttling, rotation guidance. |
| F22 | Documentation mixes revisions and claims state resets | Source-confirmed (R1); old engineering records remain dated evidence. | Current matrix, navigation cleanup, final source rendering. |
| F23 | Internal plans expose workstation paths/private-work wording | Source-confirmed (R1). | Portable user examples; historical records retained outside user navigation. |
| F24 | Missing SECURITY, contribution guidance and code of conduct | Source-confirmed (R1). | Private vulnerability reporting, CONTRIBUTING, CoC. |
| F25 | Artwork licensing unresolved | Source-confirmed (R1); user selected CC BY 4.0. | Attribution, trademark note, distribution/dashboard notices. |
| F26 | Public go install depends on unpublished nested RC module | Confirmed publication gate; private workspace consumers do not prove public availability. | Publish dependency tags only after private qualification; public checksum readback. |
| F27 | Manifest/API configuration names and models differ | Intentional input/API representations, with confusing presentation/import behavior. | Explain normalized resources and import-once/apply; preserve valid manifests. |
| F28 | Dashboard separates live monitors/configuration ambiguously | Source-confirmed copy/navigation issue. | Clear labels/links; no unrequested redesign. |
| F29 | Operator-facing ECS jargon/backpressure/coverage wording confusing | Source-confirmed (R1). Measurement limitations must remain accurate. | Controller terminology and actionable bounded diagnostics. |
| F30 | CLI monitor list lacks operational up/down/unknown | Source-confirmed (R1). | Correlated observations in output contracts. |
| F31 | CLI/dashboard hide useful server problem details | Source-confirmed (R2), fixed generic strings. | Safe code/request/operation/phase/field details, terminal escaping. |
| F32 | HTTP never follows redirects and ignores ambient proxy | Intentional defaults. “Every redirect fails” is too broad where expected statuses are configured. | Production-path expected-3xx tests and explicit documentation. |
| F33 | Opsgenie alerts do not close | Source-confirmed (R1). Acceptance is asynchronous, not completion. | Incident/endpoint alias correlation and independently verified close effects. |
| F34 | PagerDuty warning severity always error | Source-confirmed (R1). | Correct severity and trigger/resolve correlation. |
| F35 | ICMP ignore_privilege has no effect | Source-confirmed (R1). | Supported unprivileged mode or explicit platform rejection. |
| F36 | gRPC service ignored by TCP-only reachability | Source-confirmed (R1); omitted-mode TCP remains compatible. | Opt-in unary health mode, service/TLS contract tests. |
| F37 | Permanent storage error keeps admission unavailable | Intentional fail-closed behavior; not grounds to delete/reopen corrupt state. | Document stopped repair/restore, distinguish contention and leadership. |
| F38 | Raft diagnostics discarded and incident opening not logged | Source-confirmed (R1). | Existing logger integration with safe correlation. |
| F39 | Mixed logging, wrong caller, ANSI in nonterminal/file output | Source-confirmed (R1). | Consistent logger and output/caller regression tests. |
| F40 | High idle CPU from 200 Hz controller loop | Awaiting fresh controlled profile; R1 reports 11%/91 MB for one monitor. | Measure empty/one-monitor first, preserve latency before loop changes. |
| F41 | CSP/framing protection absent | Source-confirmed (R1). | Parser-compatible CSP/security headers and required browser campaign. |
| F42 | Credential-use operator can choose arbitrary destination | Intentional trusted-operator boundary, undocumented. | Document accurately; no multi-tenant/destination-isolation claim. |
| F43 | SDK version/schema operations return 404 | Source-confirmed missing handlers (R1). | Canonical generated explanations and synchronized supported inventory. |
| F44 | Old alert pipeline only exercised by tests | Awaiting production/tag reachability audit; line count alone insufficient. | Remove only proven unreachable code, move meaningful active-path regressions. |
| F45 | Persistence tests exceed Make's 20-minute limit | Corrected (R2): timeout is 45 minutes. | Retain finite budget and final compiler/tag checks. |
| F46 | Empty-monitor filter message inverted | Source-confirmed (R1/R2), Monitors.tsx. | Fix copy with empty/filtered regression. |
| F47 | 100.0% overflows health ring | Awaiting rendered reproduction (R1 report). | Bounded visual correction and browser evidence. |
| F48 | Describe exposes raw JSON/zero timestamps | Source-confirmed (R1); exact output contract needs fresh check. | Useful rendering, explicit unavailable times. |
| F49 | Duplicate h1 headings | Awaiting DOM/a11y reproduction across routes. | Correct hierarchy, no blanket unverified rewrite. |
| F50 | Missing not-found route | Source-confirmed (R1). | Explicit unknown-route page and navigation test. |
| F51 | Public remote contains agent branches | Release-hygiene observation, not authorization to delete unrelated branches. | Qualified RC promotion; preserve other reviewers' work. |
| F52 | Missing Go dependency notices/MPL Raft attribution | Corrected (R2): packaging already collects dependency notices. | Verify target-specific final inventories and intact notices. |
| F53 | 35–50-resource apply fails during validation with 503 | Reproduced (R2/R3): 250 ms health-lock timeout during upload sets recovering; controller=false, collections=true. | Separate observation from uncertain-write recovery; synchronous accepted-prefix batching. |
| F54 | Pre-admission 503 misclassified as ambiguous mutation | Reproduced (R2), SDK recognizes only allocation-specific marker. | Exact proven-not-submitted contract; never infer safety from prose alone. |
| F55 | Interrupted original collection lacks full explicit CLI continuation | Source-confirmed: resume-upload only transfers missing uploads. | Original-operation resume through validation/activation, exact content identity. |
| F56 | λ only measured once at startup | Corrected current-source claim: controller refreshes demand after catalog growth. | Retain worker-demand regression during admission/idle repairs. |

## Execution evidence

No new test pass is recorded at inventory creation. Each repair must add exact
commands, terminal outcomes, source identity/scope and remaining gates here or
in a linked bounded evidence record. Full release matrices wait for the final
freeze; focused development checks must never be relabeled as that matrix.

External prerequisites remain native runners for every claimed OS/architecture,
private staging infrastructure, configured provider accounts/designated targets,
and sufficient measured physical host disk for the comparative campaign. No
24-hour endurance run is included.

## Current implementation status, 2026-10-14

The baseline table above retains the supplied review's original evidence. This
status summarizes repair work and its limits; local checks do not close the
final source, platform, provider or publication gates.

| Findings | Current disposition |
| --- | --- |
| F01–F09 | Protected initialization, current quickstart, episode/cooldown/maintenance policy, verification wording, safe diagnostics, unknown HTTP effects, Docker budgets and strict validation are implemented with focused regressions. Complete process/provider qualification remains open. |
| F10–F12 | Store-owned Linux/APFS/NTFS retirement, bounded allocation accounting and pending-retirement restore are implemented; lazy allocation and the Windows 64 MiB initial mapping are preserved. Focused Linux and native Windows x64/local-NTFS retention, snapshot-growth and backup/restore checks pass with finite independent review. Failed native runs and link-fixture exclusions remain explicit. Native macOS, Windows arm64/service identities, full corruption and frozen-source qualification remain open. See the [backup/restore record](collection-backup-restore-2026-10-14.md) and its linked storage evidence. |
| F13–F16 | Corrections/rejected reset recommendation stand. Approved collections/worker features and historical formats remain; explicit nested-module and build-tag qualification still needs the frozen source. |
| F17–F21, F41–F42 | Host/origin, bootstrap warning, profiling restrictions, token length/throttling, CSP and trusted-operator boundaries are implemented. Default real-server browser evidence and focused local security checks exist; remaining matrix/native service evidence and final security review remain open. |
| F22–F27, F51–F52 | Current/historical documentation boundaries, security/contribution/conduct material and artwork licensing are implemented. Main-navigation runtime guides are refreshed. Public module installation, complete packaged notices, native distribution and release identity still require final qualification/promotion. Unrelated remote branches remain untouched. |
| F28–F31, F43, F46–F50 | Descriptive interfaces, observations, problem details, discovery, headings, percentages and missing routes are repaired. Current automated fixture accessibility/keyboard checks pass; they are distinct from real-server and native browser evidence. Final-source matrix/review coverage remains open. |
| F32–F36 | Redirect/proxy defaults remain intentional. Correlated PagerDuty/Opsgenie, privilege-mode ICMP and explicit gRPC health behavior have production-path fixture coverage. Account effects and native service-identity checks remain unverified. |
| F37 | Permanent failure remaining unavailable is intentional; stopped repair/restore is documented. It is not permission to delete state or reopen it speculatively. |
| F38–F40 | Application/pool/Raft diagnostics and caller/color corrections passed finite source review; the resulting typed-nil regression is reproduced and closed. Owner deadline waiting passed finite source review and short paired CPU measurements. Broader logging coverage and frozen performance qualification remain separate. |
| F44 | The unused nil-Store batch/alert path is removed after production/tag reachability analysis and migration of meaningful tests to the active Store-backed owner. Focused default/tagged/minimum/race checks and independent review pass. The [audit follow-up](alert-path-audit-2026-10-13.md) records the finite code repair and its remaining final-matrix boundary. |
| F45 | The documented 45-minute budget remains. The complete working-source persistence and local-administration suites pass on Go 1.27.2 with race detection after the generation/restore repairs. Three subprocess helper entries skip outside their parent fixtures; no functional case is omitted. A complete frozen-source matrix is still required. |
| F53–F56 | Health/write-recovery separation, synchronous accepted-prefix grouping, exact non-admission handling and original-operation continuation are implemented. Real 35/50-monitor CLI imports, reply-loss/continuation and Raft/bbolt contention regressions pass within the [continuation](collection-continuation-2026-10-14.md) and [contention](collection-contention-2026-10-14.md) evidence scopes; independent review is closed for those repairs. Worker demand refresh remains active. Measured slow-disk throughput and the complete frozen compiler/tag matrices remain open. |

Exact checkpoint identities, failed runs, scoped passing checks and independent
review dispositions are retained in [repair progress](release-repair-progress.md)
and the dated implementation records linked from it.

## Scoped follow-up, 14 October

F12 now has native Windows x64 snapshot-overlap growth checks on Go 1.27.1 and
1.25.0, plus a Linux race regression. Other native/service/recovery requirements
remain open; see the [growth record](collection-snapshot-growth-2026-10-14.md).

F44's production/tag reachability audit, meaningful regression migration and
unused batch/alert removal are complete for this working implementation.
Independent review and scoped default/tagged/minimum/race checks pass with the
boundaries recorded in the [audit follow-up](alert-path-audit-2026-10-13.md).
This closes the finite code repair, not the final frozen release matrix.

F55 now has real CLI reply-loss/continuation coverage for upload, validation and
activation. These checks found and repaired ordinary-profile and post-validation
input-proof failures. Interrupted compiler attempts remain terminal with their
original evidence and zero activation. Focused default/minimum/tagged/race checks
and independent source review pass; see the
[continuation record](collection-continuation-2026-10-14.md). Slow-storage contention
and the complete frozen release matrices remain separate.
