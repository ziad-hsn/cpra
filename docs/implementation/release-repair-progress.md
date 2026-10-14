# Release repair implementation progress

Plan: [approved release repairs](release-repair-plan.md).
Baseline: `2ba7b73f0c9d398bfee2fbcf3896f70ccc1fb37c`.
Branch: `codex/release-repairs`.
Updated: 2026-10-14. The candidate is not frozen or publicly qualified.

The [compiler/dependency security checkpoint](dependency-security-2026-10-14.md)
updates the release compiler to Go 1.27.2 and gRPC to 1.83.2. Five `x/net`
HTTP/2 advisories remain: their upstream fix requires Go 1.26, and the approved
Go 1.25 application minimum has not been changed without a user decision.
The earlier `73d4797` qualification prefix is incomplete and does not qualify
the changed recipe or dependency graph.

The [collection backup/restore follow-up](collection-backup-restore-2026-10-14.md)
repairs a reproduced pending-cleanup restore failure. The authenticated private
restore copy now completes the original admitted retirement before publication;
the original backup and ordinary runtime identity checks remain intact. Focused
Linux regression/race checks and independent source review pass. The subsequent
maintenance-order repair lets admitted cleanup proceed before scanning. Focused
Windows x64/local-NTFS proof cases and both stopped-backup/restore scenarios pass;
the native runner's directory-handle interference and failed attempts remain
recorded separately. The complete Linux persistence and local-administration
suites now pass with Go 1.27.2 race detection, along with their static analysis.
Focused Go 1.25 and Go 1.27.2 `externaljobs`/race follow-ups also pass. The
final-source matrices remain required.

All checks below are private development evidence for the inspected working
source. They are not a replacement for the complete final-commit matrices or
independent review. Failed reproductions and intermediate failures are retained.

The branch now includes published `main` at `b293f34`; merge `4516d1d` adds the
missing merge history without a content change. Its clean private source archive
and isolated dependency bootstrap passed, including the refreshed nested-module
checksums. The first complete compiler matrix was started against that archive
with `GOWORK=off`, race detection and an explicit scratch-budget governor.
Independent review then identified the documentation metadata gap recorded at
the end of this file. That correction required a new source freeze. Results for
`4516d1d` retain that identity and cannot qualify the corrected source or the
subsequent compiler/dependency changes.

| Ticket | Implemented in this repair branch | Still required |
| --- | --- | --- |
| 1. Baseline | Approved plan saved; all three reviews mapped to F01-F56 with source and evidence dispositions; other checkouts and prior evidence preserved. | Final finding dispositions and independent review. |
| 2. Notifications | Formats 40/41; episode, logical delivery and stable per-target endpoint identities; obsolete queued cancellation; maintenance cancellation; verification-pending wording; unknown HTTP 5xx; persisted overall deadlines and retry identity; canonical bounded failure causes in current state, intent, outcomes, retained events and operator interfaces. PagerDuty/Opsgenie receive committed incident/endpoint correlation; Opsgenie distinguishes acceptance from processing and closes the original alias. Focused lifecycle and real network receiver tests pass. | Complete process-crash/restart campaign and final-source matrix. Provider retry/receipt, diagnostics and maintenance checkpoints passed independent review; unsupported rejection evidence remains unknown. |
| 3. Collections | Immutable routine health publication without the FSM lock; health-read cancellation does not poison write recovery; contiguous uploads use one synchronous transaction per 64 resources/1 MiB group; exact pre-submission marker classification in API/SDK; explicit `resume` with server verification of original raw and ordered resource identities, including complete uploads; `resume-upload` retains upload-only behavior. Real native CLI imports of 35/50 monitors complete and retain identity after graceful restart on both compilers and the tagged build. Real Raft/bbolt writer contention preserves routine health, conservative admission and the exact original prefix across restart; permanent ledger damage remains unavailable. | Native CLI reply-loss and contention coverage pass with independent source review. Complete frozen-source matrices and measured slow-disk throughput remain open. Existing original-operation dashboard continuation passed the required default browser campaign. |
| 4. Storage | Store-owned generation catalog, reader/snapshot pins and bounded Linux/APFS/NTFS retirement are implemented. Current/previous/pinned generations remain protected; catalog publication precedes removal of the same verified intent. Path-free counts, cleanup conditions and bounded completed-generation apparent/allocated file subtotals with coverage and oldest sample time are exposed through state, SDK and dashboard. Windows initial mapping remains 64 MiB. Linux minimum/tagged/race checks pass. Native Windows x64 retention, snapshot pins, catalog ordering, five Store restart/snapshot cycles and four process-termination boundaries pass in a subset with four explicit symlink-fixture exclusions, on both compilers. Finite source review closed the intent replacement repair. Existing lazy allocation, stopped backup/restore and synchronous writes remain. | Windows x64 frozen-snapshot growth passes on both compilers with Linux race coverage and independent review; native Windows symlink and service-identity cases, Windows arm64 and macOS execution remain open; full backup/restore and corruption matrix after these changes; complete final-source review/qualification. The retained full Windows run is failed evidence, not superseded by the passing subset. |
| 5. Access | Go-based `local init --management`, exclusive protected files and localhost CA/leaf certificate; system token-output separation; mode-aware service arguments; minimum 32 printable token characters; explicit access discovery; ignored-bootstrap warning; loopback Host/origin guards; profiling cmdline removal; bounded failed-authentication throttling with 401/Retry-After; CSP/security headers. Local initialization, API/authentication and real application start/create/restart checks pass. Dashboard type checking, lint and tests pass across the full run and focused fixture repairs. | Remaining compiler/tag browser matrix, native system service/ACL checks, trusted-operator documentation and final security review. Current embedded rebuild and required Go1.27.1/default browser campaign pass. |
| 6. Drivers/configuration | File parsers always reject unknown fields and duplicate JSON keys. Shared static driver settings validate required destinations, ranges, gRPC mode and Docker budgets in manifests, management and SDK preflight/new apply, while original-input normalization stays byte-compatible. gRPC health mode uses verified TLS by default, the configured service and SERVING-only success. Docker has separate stop grace and operation budgets. ICMP selects supported privilege mode or rejects the platform. PagerDuty/Opsgenie correlation, closure and severity fixtures pass. Complete jobs/manifest/loader/SDK API/collection suites and focused management checks pass. | Native service-identity ICMP checks; complete driver/provider and process campaign; independent review. |
| 7. Interfaces/diagnostics | Version and canonical schema explanation routes implemented and authorized; SDK inventory and access discovery generated from canonical inputs; unavailable source time remains omitted. Dashboard primary headings, missing routes, empty/filtered/out-of-range page wording, compact health label and controller terminology repaired. CLI monitor lists show observed health; human describe renders safe summary fields and explicit unavailable times/counts. Terminal controls are escaped in table/key-value cells; safe check/action diagnostics are shown consistently. Raft/pool and committed incident/action diagnostics use the application logger with safe fields and corrected caller attribution. Result-shard timers and owner deadline waiting reduce measured idle CPU. | Remaining browser/compiler/tag matrix; the reviewed dead-path removal is recorded below. Source re-review closed the typed-nil pool correction and finite owner-waiting implementation. The short local CPU comparison is separate from fleet capacity and frozen release qualification. |
| 8. Materials/qualification | README begins with protected management initialization. Private compiler/frontend tools installed from recorded checksums. CC BY 4.0 artwork license, Ziad Hassan attribution and separate name-use note added, with archive/package/image/dashboard notice inputs. Security reporting, contribution and conduct guidance added. | Remaining documentation consolidation, generated assets/notices and packaging verification, final source freeze and all release gates. |

## Recorded local checks

The release compiler used for new checks is Go 1.27.2, checksum-pinned by the
repository installer. Earlier Go 1.27.0/1.27.1 checks remain earlier development
evidence. Frontend qualification uses pinned Node v24.21.0 and pnpm 11.22.0.

- API/SDK admission classification and original-handle tests passed.
- SDK original-file continuation and mismatch/lost-reply tests passed.
- Management encrypted input proofs, collection upload grouping and CLI
  continuation tests passed.
- Complete jobs and controller-system suites passed on Go 1.27.1.
- The first broader persistence suite failed six cases. Its format/header/test
  publication assertions were repaired; the focused six regressions passed.
  A complete final-source persistence rerun is still required.
- Tagged notification/recovery, exact worker receipt/start and detached
  connection observation checks passed on Go 1.27.1.
- Complete HTTP server, authentication and local administration suites passed.
- The generated localhost installation authenticated, created a disabled monitor,
  stopped, removed its obsolete bootstrap policy and recovered the same committed
  monitor identity on restart. No provider operation was enabled by that fixture.
- SDK operation inventory and discovery consumer checks passed.
- Dashboard type checking and lint passed. The complete 521-test run passed 519;
  the remaining two fixture assertions were corrected and their 29-test suites
  passed on rerun. Tests cover explicit access discovery without write downgrade
  and exact original-operation non-admission evidence. Generated asset rebuilds
  and required real-server browser campaigns remain open.
- Static driver configuration and real gRPC/Docker production-path fixtures
  passed on Go 1.27.1. Frozen file-profile conformance remains unchanged. Existing
  SDK client checks passed; an obsolete non-existent `sdk/go/legacy` target was
  rejected by the test runner and is not reported as a passing module check.
- Generation ownership and Linux retirement tests passed, including five real
  Store restart/snapshot cycles, pinned reads, interrupted retirement and failed
  catalog replacement before intent removal. Focused generation race checks
  passed. Generation state API checks, dashboard diagnostics tests and current
  frontend type checking passed. Native macOS/Windows execution remains pending.
- Focused generation/restart checks passed on Go 1.25.0 as well as Go 1.27.1.
- Provider correlation and asynchronous Opsgenie processing fixtures passed;
  status-read failures after acceptance remain unknown. The mock-contract
  receiver and its twelve request-oracle tests were updated and pass. These
  fixtures are not provider-account delivery evidence.
- The independent checkpoint review identified three notification/replay
  findings, now reproduced and repaired. Controller maintenance cancellation
  survives a real Raft restart and prevents calls from already queued workers.
  Warning/outage/recovery dispatches keep PagerDuty/Opsgenie provider identity.
  Old envelopes reject all newly introduced identity fields and malformed
  notification versions. Focused replay/policy suites and complete controller
  system/job suites pass. See the [checkpoint review](release-repair-review-2026-10-12.md).
- Re-review closed the correlation and format findings and exposed a missed
  maintenance window across restart/stall. Durable original queue times now
  prevent that delayed delivery, including an already admitted worker and a
  retry due after the window. Current-time result processing also suppresses
  new notifications during maintenance. Focused default/tagged checks pass;
  source re-review of this extension remains open.
- Missed-window source review passed. A further batch-one cancellation
  regression was reproduced and repaired with command-count/encoded-byte
  prefixes and continuation on the next owner turn. Its restart/no-effect/
  storage-health regression passes; independent source re-review at `e9bc6c5`
  closed the finding without a further concrete defect.
- Artwork/release archive contracts, asset freshness contracts and frontend
  type checking passed after the licensing changes. The 702-test release-script
  run passed 700; its failures were an absent compiler on the check's PATH and
  an old discovery operation count. Archive and native event-reader regressions
  passed after correcting the environment/count. Documentation reference copies
  are stale after the source repairs and must be regenerated before strict
  documentation qualification.
- All 526 frontend tests, type checking and lint passed after the display work;
  the two subsequent review edge-case fixes passed in the eleven-test app suite.
  The reviewer confirmed the notices path without a pixel-fit/browser claim.
  Complete CLI tests pass with descriptive monitor output and terminal escaping.
- The complete pinned dashboard rebuild passed with 528 tests, type checking,
  lint, parser generation, embedded notices and asset freshness metadata. Source
  review closed the two dashboard edge cases. The remaining CLI snooze finding
  is repaired: pending or missing control observations render unavailable,
  rather than asserting that a monitor is not snoozed. The complete CLI suite
  passes with separate missing, pending, unsnoozed and snoozed regressions.

An independent reviewer agent reviewed checkpoint `b59d5b8` at the user's
request. Correlation and format repairs passed source re-review; a further
missed-window maintenance and bounded cancellation repairs passed source re-review;
this is not a review
of a final frozen release. No public branch, tag,
release, module, image, chart or documentation promotion has been performed.

## External gates

The user confirmed on 2026-10-12 that no native runner fleet, private Kubernetes
environment or designated external provider/agent accounts are configured.
Those qualification gates remain open; fixture passing is not account evidence.

The 14 October campaign preflight measured 18,711,650,304 physical C: free bytes, below the initial
35 GiB comparative-campaign prerequisite. The large campaign has not started.
Task-owned scratch remains subject to the 6 GiB cap and 64 MiB reserve.

The 24-hour endurance run remains cancelled. The release endpoint remains
`v0.1.0-rc.1`; stable publication requires a separate release decision.

## Safe diagnostic checkpoint

Typed error classification, canonical message/format validation, real Raft
snapshot/restart and retained-history checks pass. An actual HTTP check receiver
returns a private body; the committed state and subsequent production webhook
retain only the safe HTTP-status cause. Notification prototypes remain unchanged
between deliveries. Complete jobs, controller-system, CLI and SDK API suites pass
on Go 1.27.1, alongside focused default/tagged replay and worker-result checks.
Focused diagnostic race checks pass. The 51-test frontend observation/history/
action regression run and type check pass. Focused Go 1.25.0 diagnostic checks
pass. The pinned rebuild regenerated the embedded dashboard, parser and notices
with all 533 frontend tests, type checks and lint passing. Independent source
review for this checkpoint remains required.

The final snooze correction at `23989e3` passed independent source re-review.
The reviewer also confirmed the embedded notice path and manifest digest, without
claiming a real-browser or final-release pass.

The diagnostic source review identified a missing browser projection shape.
That defect is reproduced and repaired through the actual resourceView path;
generation now rejects missing references instead of dropping them silently.
The regenerated dashboard passes 534 tests, types and lint. This follow-up awaits
independent source re-review.

## Provider rejection checkpoint

Provider-specific response classification now replaces generic HTTP 429 replay.
Documented retry delays are bounded, committed and preserved across snapshot/
restart without extending the original deadline or changing logical delivery
identity. Pushover quota rejection does not retry; Teams' documented throttling
body is recognized even with HTTP 200. Generic webhook, Datadog, Mattermost and
VictorOps non-success responses remain unknown without sufficient rejection
proof. See [response contracts](notification-response-contracts.md).

Complete jobs/controller-system suites, focused tagged provider checks, real
Raft restart, Go 1.25 and focused race checks pass. Earlier fixture failures
are retained: required acknowledgement bodies were missing, the strict decoder
required explicit optional-field tags, and one restart fixture selected a
different endpoint. Their corrected checks pass; no failed run is attributed
as a success. Independent source review remains required for this checkpoint.

Source re-review of the dashboard projection correction at `b0bb4b8` closed the
missing FailureDiagnostic shape with no new concrete finding. It did not execute
tests or claim browser qualification.

## Operator problem reporting checkpoint

CLI and dashboard management failures now include bounded reviewed problem
codes, valid correlation IDs, HTTP status and at most ten source field errors.
Raw detail, field messages/values, unknown codes and unsafe identities are not
rendered. Collection continuation controls identify validation/activation/
cancellation failures while retaining the original operation and explicit-action
policy. The non-admission CLI wording applies to the rejected request rather
than claiming every failure was operation allocation.

The complete CLI suite and 119 focused frontend regressions pass. A hostile
metadata fixture first reproduced the dashboard's broader operation-ID helper;
problem metadata now requires the actual bounded UUID/operation-handle shape.
The corrected fixture passes. Frontend type checks passed while generating
`tsconfig.tsbuildinfo`; that build-cache mutation is recorded in source inventory
and is not a frozen-source pass. Embedded regeneration and independent source
review are still required for this checkpoint.

The pinned dashboard rebuild now passes all 536 tests, type checking and lint,
and regenerates the embedded assets/parser/notices. Independent source review
remains required; no real-browser qualification is implied.

The operator source review found two surrounding paths that bypassed the new
problem formatter: error-sourced operation links and handled collection reads.
The real resource-route reproduction exposed both text and URL leakage for
hostile handles. Strict error-handle validation is now shared across those
links and collection adoption. Manual/polled validation and progress-read errors
retain safe problem metadata and phase. The corrected pinned rebuild passes
all 540 tests, types and lint, with updated embedded assets. Intermediate type
errors and an old synthetic non-UUID interrupted-response fixture are retained
as failed runs; realistic identity fixtures pass after correction. Final source
re-review of these fixes remains required.

Provider source re-review closed the case-alias correction at `29b7ad8` and the
Opsgenie acceptance/processing correction at `aaba1f3`, including unknown holds,
no second POST, and retained snapshot/restart state. No further concrete defect
was reported in those finite repairs. The reviewer ran no tests or provider calls.

## Latest reviewed checks

The safe provider response/receipt repairs, diagnostic projection, operator
problem reporting and result-batch timer changes passed finite independent
source review. The complete pinned dashboard rebuild passes 540 tests, types
and lint. The required Go 1.27.1/default real-server browser campaign passes all
eight required tests without skipped coverage; its own source inventory remained
consistent. The outer development check records documentation edits during the
run and is not a frozen-release pass.

The three paired short idle comparisons and separate pilot profile are recorded
in [idle observations](idle-profile-2026-10-12.md). The roughly one-third CPU
reduction is limited to empty/one-monitor Linux/WSL runs; no capacity or memory
claim follows. The complete queue suite, focused Go1.25 and race/drain/demand
checks pass. Further owner-loop scheduling work remains open.

## Verification report follow-up

The broader application run found that the verification report lost the HTTP
status for generic webhook failures after those effects became unknown. The
report now records the observed status separately from accepted, rejected or
unknown effect certainty. A documented Slack rejection remains rejected and
is invoked only once; HTTP 400/404/429/500 webhook responses and transport
failures remain unknown. Accepted operations still require an independent
observer before the case passes. Complete drivertest and jobs suites pass on
Go 1.27.1. The initial added test omitted an import; that failed compile is
retained separately from its corrected passing check. Independent review of
this follow-up remains required.

The broader run also found invalid shutdown and collection-routing fixtures.
The webhook fixture supplied an unsupported per-endpoint timeout, while the
post-borrow routing fixture omitted its required log destination and therefore
never reached the callback. Both now use valid static configurations; their
intended cancellation, expiry and unknown-delivery checks pass three repeated
runs. No production shutdown or routing guard was relaxed. The broader run
remains failed evidence, not a passing final-source matrix.

Source review closed both deployment-command corrections at `b431ec9`. It
identified three verification follow-ups: notification status was being used
as a health-check cause, body rejection codes could replace received HTTP
status, and body-read errors discarded received headers. Response metadata
now wraps the safe cause separately; it never grants retry permission. The
report retains received status and logical rejection code independently.
Real Telegram HTTP 200 rejection and delayed-body fixtures, tagged Teams
throttling and committed webhook-unknown action checks pass. A first delayed
receiver fixture left its request body unread and timed out during teardown;
draining that body repairs the fixture, and the corrected check passes. Canonical
persisted diagnostic messages and format versions are unchanged. Independent
source re-review remains required.

The broader default application run passed the root package and most leaf
packages, but failed the three fixture/report checks above and timed out the
persistence package at the locally supplied ten-minute limit. Its alarm fired
during a fresh recovery case, not an established deadlock. The repository's
existing suite budget is 45 minutes; a complete persistence rerun uses that
budget. Neither the timed-out run nor mixed working-source inventories count
as final-source qualification.

## Log formatting and caller checkpoint

Captured development console and JSON output no longer contains ANSI level
colour. Shared logger calls identify the application line, including controller
and system adapters, instead of attributing every record to the sink wrapper.
Structured controller initialization no longer creates unused fallback files
in the working directory. Direct/derived logger, wrapper, JSON and no-file
regressions pass, including focused race checks. The initial colour/caller
reproduction failed as expected and is retained. Independent review remains
required. Raft/pool integration and correlated lifecycle records remain open;
this checkpoint does not complete the unified logging ticket.

The logger/controller/server suites pass at the initial caller checkpoint.
Source review closed the notification metadata and fixture-validity fixes, and
found an extra frame in performance helper calls. Direct and system performance
and component-state caller regressions now exercise that path. The first
fixture left debug verbosity disabled; the corrected enabled fixture reproduced
the wrong performance caller. Helper-specific skips repair it, and focused race
checks pass. Source re-review remains required for this final caller correction.

Source re-review at `e2be211` closed the helper caller correction with no
further concrete finding in that finite scope.

## Complete persistence run and digest boundary

The documented-budget persistence run completed in 803 seconds. It reported
one failing field-projection guard: its reviewed exception inventory omitted
the execution-only Failure and RetryDelay fields. The corrected guard checks
their exact types/tags and preserves zero-field historical JSON bytes. A separate
reproduction showed that the internal operation-digest helper silently omitted
nonzero execution-only fields; it now rejects them before projection. Public
reservation already validates command shape, and no historical digest bytes or
storage format were changed. Focused golden/replay/diagnostic checks pass with
race and externaljobs. Independent review and a complete corrected final-source
persistence run remain required; the earlier full run is still failed evidence.

## Current documentation preview

Current release notes describe implemented development behavior and pending
qualification instead of retaining the memory-only preview's limits. September
runtime/provider notes remain dated archives. Source metadata identifies the
preceding implementation checkpoint and an uncommitted documentation refresh;
it is not a release attestation. MkDocs keeps archive pages buildable outside
user navigation through explicit not_in_nav patterns. Other navigation, link,
anchor, traversal and symlink guards remain active.

Twenty-five documentation-tool tests and strict source checks pass. The pinned
private site build renders 166 content pages and 16 redirects, with 29,316
internal links/assets checked and no errors. Its first strict run found twelve
removed heading anchors; current pages now preserve those inbound anchors and
the corrected strict build passes. The separate gh-pages checkout is untouched.
Independent source review remains required. The private artifact and dependency
inventory remain in task-owned scratch; this is not published-site evidence.

Source re-review closed the digest correction at `7ae5204`. Documentation review
found an EOF case that left a stale broad not_in_nav exemption or a duplicate
inline setting. Block/inline inputs with and without final newlines reproduce
the defect; complete-section replacement now accepts EOF, and all 25 tooling
tests pass with duplicate-setting rejection intact. Source re-review of this
small correction remains required. Focused notification, logger/caller,
projection/replay and diagnostic checks also pass on Go 1.25.0.

Source re-review at `d0cd5b3` closed the EOF archive-setting correction.

## Runtime log integration

Committed incident/action-result diagnostics now carry stable correlation and
safe cause codes. Production no longer filters all informational lifecycle
messages at the controller wrapper. Pool output uses the application sink and
pool identity, and Raft diagnostics no longer disappear into io.Discard.
Bounded Raft fields exclude arbitrary text/objects and classify errors without
their paths; panic payloads are not printed. Duplicate results and real
snapshot/reopen do not re-emit live lifecycle logs.

Complete controller, controller-system, queue and logger suites pass on
Go 1.27.1. Focused real-Raft, output and lifecycle race checks pass, alongside
Go 1.25.0 checks. Intermediate printf-vet/interface compile failures are retained.
The existing go-hclog dependency is now direct; no new dependency version or
storage format was added. See [runtime logging](runtime-logging.md).
Independent source review and final-source matrices remain required.

The finite runtime logging review found a typed-nil standard logger regression:
changing the constructor argument to an interface made a nil pointer pass its
old nil guard and panic at startup. The retained reproduction fails at Start.
Both pool/router boundaries now normalize typed nils, and optional router/error
diagnostics check availability. The complete queue suite, focused race checks
and Go 1.25.0 checks pass. Re-review of this correction remains required; the
review found no other concrete defect in its finite lifecycle/Raft scope.

The refreshed private documentation build renders 167 content pages and 16
redirects. It checks 29,409 internal links/assets with no errors, and the later
source-copy check agrees with its preview. The separate documentation checkout
and public site remain untouched.

Source re-review closed the typed-nil correction at `fdb6b14` and found no
concrete defect in the finite owner-waiting change at `748fe4d`. Deadline/wakeup
tests, default/tagged complete controller/system/scheduler/queue suites and
focused race checks pass. Go 1.25 passed three packages but failed a maintenance
fixture that crossed the wall-minute boundary during initialization. Its controlled
one-shot boundary passes three repeats and the complete system rerun; the failed
run remains failed evidence.

The short owner-wait comparison completed twelve process measurements: empty
CPU averaged 8.43% to 4.80% of one core; one HTTP monitor at 60-second cadence
averaged 9.00% to 4.17%. All empty measurement windows received zero requests,
all one-monitor windows received one, and all processes stopped cleanly. Normal
builds, exact source/input and executable identities, host interference and
measurement limits are recorded in the [dated observation](owner-wait-observation-2026-10-13.md).
This is not the comparative fleet campaign or an endurance result.

Six primary user guides now describe current architecture, explicit apply,
strict validation, durable recovery, measured SLO feedback and protected access
instead of the old main preview. The strict private build renders 168 content
pages plus 16 redirects, checks 29,521 internal links/assets and reports no
errors. Twenty-five documentation-tool tests pass. Selected source references
were regenerated from the working implementation; the preceding implementation
commit in source metadata is not a release freeze.

The frontend accessibility campaign initially failed before sign-in because
its HTTP fixture did not implement the new access discovery response. Adding
the management/authentication-required response repairs the fixture. Fifteen
automated browser scans, keyboard/dialog checks and thirty dashboard-tool tests
pass. This campaign explicitly uses fixture responses, invokes no providers,
and does not replace the real-server browser/compiler/tag campaigns or establish
full WCAG conformance. The failed artifact is retained. Independent source
review of this fixture and documentation correction remains required.

Source review closed the finite documentation/accessibility fixture correction
at `37ee8e6`. The reviewer inspected the CPU script/counters but did not rerun
the comparison or browser campaigns.

Ordinary 35/50-monitor imports now run through freshly built native CLI
subprocesses against normal TLS/Raft startup. Default Go 1.27.1, Go 1.25.0,
matching externaljobs and race-server cases pass complete original-operation
results, per-monitor projection and graceful restart identity checks. The final
race run also explicitly checks controller/storage readiness. The initial
50-monitor attempt hit the fixture's 12-second wait limit and remains failed;
the completed cases use a 30-second operation wait and 45-second process budget.
No provider call occurs. See the [dated import observation](collection-import-observation-2026-10-13.md).
Independent source review remains required for this extension.

The [alert path audit](alert-path-audit-2026-10-13.md) confirms that normal
executable startup supplies a Store and installs the committed owner. The
nil-Store batch/alert path remains in internal tests, and its objects are still
constructed before branch selection. Meaningful regression migration/removal
is open; no broad package change or speculative deletion is made.

A race-build peak exceeded the 6 GiB task scratch cap by an observed 117.3 MiB.
After compilation ended, 806.7 MiB of identified derived Go cache was removed;
source, modules, tools, executables and retained evidence stayed intact. The
remaining race fixture finished with source inventory unchanged. This budget
excursion is recorded rather than reported as continuous cap compliance.
A fresh physical C: read returned 25,503,776,768 bytes, about 23.8 GiB, below
the 35 GiB initial comparative-campaign prerequisite. No large campaign ran.

Independent source review closed the finite test/audit extension at `dfec8ab`.
The reviewer confirmed unchanged default process/request budgets, complete
original-operation assertions, retained restart identity, cleanup and accurate
audit boundaries. Tests, disk readings and elapsed times were not independently
rerun by that reviewer.

The final private documentation check renders 170 content pages and 16 redirects,
with 29,692 internal links/assets checked and no errors. After all Go verification
processes completed, task-owned derived cache cleanup removed 3,520,208,896
allocated bytes (about 3.28 GiB) across 30,012 entries. Two unrecognized/shared
entries were preserved; tools, modules, source, fixtures, payloads and evidence
remain. Task scratch plus its workspace node_modules measured about 1.91 GiB.
Physical C: then reported 26,388,611,072 bytes (about 24.6 GiB); reclaiming WSL
files is not a promise of immediate virtual-disk compaction or host-space recovery.

## Outstanding work at this checkpoint

- Natively verify macOS APFS generation retirement; implement and natively
  verify Windows retirement; complete allocated-storage accounting and Windows
  mapping-growth/footprint checks.
- Migrate meaningful nil-Store/batch/alert regressions onto active owner/view
  paths before removing the unused executable path's implementation.
- Complete slow-upload/contention and interruption tests across collection
  phases, plus the full process-crash, corruption, backup/restore and upgrade matrix.
- Freeze a reviewed final source only after remaining changes, then run all
  application/SDK/worker compiler/tag, race, vulnerability, generation, browser,
  reproducibility and packaging checks against that identity.
- Qualify all native platform/service/package targets, both OCI architectures,
  Compose and Helm 3/4/storage/rotation/restore behavior on configured infrastructure.
- Verify real effects for all required provider/agent roles using designated
  accounts/resources; missing configuration remains not verified.
- Complete the matched comparative fleet campaign after physical-host space
  and measured headroom meet the prerequisite. The 24-hour run stays canceled.
- Assemble, read back, sign and publish the qualified RC/modules/chart/docs only
  after the private gates pass. No public promotion has occurred in this checkpoint.

## macOS generation-retirement checkpoint

The shared Linux/macOS retirement engine preserves the existing Linux native
identity check, strict intent format, current/previous retention and pinned
reader protection. The macOS extension requires local APFS identity, private
owner/mode, single-link files and descriptor-verified absence of an extended ACL.
Unverifiable objects remain untouched. Shared primitive/process/Store tests
now compile for Darwin; required native Linux/macOS jobs fail on missing
identity evidence instead of satisfying the gate through a skip.

Focused Linux/WSL checks pass on Go 1.27.1, its race build and Go 1.25.0, with no
skipped cases. Go 1.27.1 cross-compiles both macOS test architectures; Go 1.25.0
cross-compiles amd64; Darwin persistence vet passes. This is not native macOS
execution. See the [implementation and evidence record](macos-collection-retirement-2026-10-13.md).
Independent source review found no actionable defect in the finite extension.
The tagged Linux suite also passes. Windows retirement, allocated-size accounting
and all complete frozen-source gates remain open. The reviewer ran no tests,
builds or native macOS operations.

## Native smoke authentication fixture

The native executable smoke harness now generates one URL-safe token from 32
random bytes instead of its old 25-character static token. The new token meets
the printable-length requirement and is reused across readiness/state reads and
both process starts. No startup, source-identity or publication guard is changed.
Independent source review found no concrete defect in the finite correction.

A private real-process startup fixture reproduced refusal of the old short token,
then authenticated readiness, graceful shutdown and stable node identity across
a second start using the 43-character generated token. The retained development
executable was used; 111 main/authentication/server source inputs match the
current branch. This is not execution of the frozen native release harness and
does not establish final-candidate or other-platform qualification. No jobs were
configured. The existing 61 release identity/recipe/native-service contract tests
pass. The reviewer did not execute them or independently verify the input hashes.

After all Go builds/tests stopped, a further task-owned derived-cache cleanup
removed 2,533,785,600 allocated bytes across 21,649 regular single-link entries.
Four unrecognized/shared entries were preserved; tools, modules, source,
executables and evidence remain. Scratch plus task-owned node_modules measures
about 2.03 GiB before the final documentation build. A fresh physical C: read
then reports 27,639,676,928 bytes, about 25.74 GiB, below the initial 35 GiB
comparative-campaign prerequisite. This
cleanup does not imply that WSL's virtual disk was compacted.

The refreshed private documentation build passes with 171 content pages, 16
redirects and 29,799 internal links/assets checked without errors. Its source
inventory is unchanged during the build. The version notice records implementation
checkpoint `2d2e355` before this documentation metadata refresh; it is not a
frozen release identity. The separate gh-pages checkout remains untouched.

## Windows retirement checkpoint, 14 October

Commit `9bd89cb` adds local NTFS retirement and shares the existing bounded
engine/tests across Linux, macOS and Windows. Review found a final-intent
replacement bug; the corrected engine preserves the original proof through
catalog publication and refuses substitution before removal. The finite source
review and bounded test follow-up are closed.

Native Windows x64 tests passed on Go 1.27.1 and Go 1.25.0 with four explicitly
omitted symlink-creation fixtures. The full requested native run remains failed
evidence because this account lacks that privilege; a corrected test also removes
an invalid single-maintenance-step assumption. Retention, pins, five Store
restart/snapshot cycles and four forced-termination boundaries passed. Linux
minimum, tagged and race checks pass; Windows arm64/macOS amd64 checks are
cross-builds. Windows vet passes. See the
[implementation and evidence record](windows-collection-retirement-2026-10-14.md).

At that checkpoint, allocated-size accounting and Windows growth/footprint
tests were the next storage work. Native platform/service/link cases, complete recovery/packaging
matrices, provider accounts and comparative performance remain open. No public
promotion or 24-hour run was performed.

## Collection allocation checkpoint, 14 October

Commit `df82cf8` adds bounded metadata sampling of one generation per maintenance
step. State reads are cached. Apparent bytes, filesystem-reported allocated
bytes, expected/measured generation counts and the oldest sample time are
exposed through the API, regenerated SDK, CLI and dashboard. Partial/unavailable
coverage is explicit; unknown/incomplete files and other storage are excluded.
No persisted format changed. Finite independent source review is closed.

Linux focused default, minimum, tagged and race checks pass. Windows x64
subsets pass on Go 1.27.1 and Go 1.25.0 with the same four explicit link-fixture
exclusions. Both measured approximately 64 MiB initial NTFS allocation under a
1 GiB logical quota. macOS amd64 is cross-build only. SDK generation matches
checked-in outputs from two independent roots. The full pinned dashboard build,
type checking, lint and all 541 tests pass; embedded assets and notices are
regenerated. See the [measurement record](collection-storage-accounting-2026-10-14.md).

Snapshot-overlap growth, native platform/service/link environments, complete
backup/corruption/distribution matrices, final source freeze, provider accounts
and comparative performance remain open. File subtotals do not certify total
physical disk usage. Publication remains withheld and the 24-hour run cancelled.

The private documentation build passes with 173 content pages, 16 redirects and
30,351 internal links/assets checked without errors. Source inputs remained
unchanged during that build; the version notice identifies `df82cf8` as an
implementation checkpoint, not the frozen release source.

After all compilers/tests stopped, task-owned Go caches were cleared again.
Scratch measures about 2.45 GiB, plus the small task-owned dashboard dependency
directory, within the 6 GiB budget. Tools, dependency inputs and evidence remain.
A fresh host C: read reports 19,493,507,072 free bytes, about 18.16 GiB, below the
35 GiB initial comparative-campaign prerequisite. No WSL disk compaction or
large comparative run was performed. Native temporary fixtures were removed.

## Snapshot-overlap growth, 14 October

The production ledger opener now has a real growth regression covering a frozen
snapshot reader and concurrent writes beyond the Windows 64 MiB mapping. Native
Windows x64/NTFS runs pass on Go 1.27.1 and 1.25.0; the corrected Linux race
fixture passes. Independent review corrected two initial fixture gaps and closed
the finite source review. No storage algorithm or format changed. See the
[scoped growth record](collection-snapshot-growth-2026-10-14.md).

The old temporary evidence directory was absent when this continuation resumed;
its committed summaries remain historical, without a current raw-log availability
claim. New evidence is kept in a private persistent task directory. The complete
untagged persistence suite passed before the growth fixture's final strengthening;
it is not represented as final-source qualification. Full native/service/link,
recovery/distribution, provider and performance gates remain open. Publication
remains withheld; the 24-hour run remains cancelled.

## Active controller path, 14 October

The nil-Store batch/alert implementation is removed after production/tag
reachability analysis and regression migration. Startup requires a Store even
in memory mode. Useful recovery, single-flight, endpoint fanout, confirmed retry,
maintenance, TLS, sizing, lifecycle and projection assertions now exercise the
committed owner and incremental view. Unused per-monitor alert state is removed;
no persisted format or active package name changes.

Default, externaljobs, Go 1.25 and race controller/fleetview suites pass within
the scope recorded in the [audit follow-up](alert-path-audit-2026-10-13.md).
Independent source review identified two coverage gaps; corrected fanout
scheduling and pending-code aggregate assertions now pass and the review is
closed. The full compiler/tag/build matrix still awaits the final source.

## Original collection continuation, 14 October

Native CLI interruption tests exposed two defects: ordinary file profiles were
rejected by the SDK, and a read-only original-input proof inherited upload-only
append restrictions after validation started. Both are repaired without changing
durable formats or mutation/claim guards. Interrupted compiler work remains
terminal under the original operation; the CLI now explains that disposition.

Five native CLI scenarios, focused Go 1.25/default/externaljobs checks and race
regressions pass. Independent source review is closed. The real application
owner restart in the CLI fixture is graceful; separate coordinator tests kill
a real child process. See the [full scoped record](collection-continuation-2026-10-14.md)
for failed baselines, exact evidence, conditional timing and remaining gates.

The pinned dashboard rebuild passes all 541 tests, type checking and lint;
parser, notices and embedded assets are regenerated. All eight required
Go 1.27.1/default real-server browser tests pass with race detection, no skips
and an unchanged campaign source inventory. Canonical documentation checks pass
for 175 pages and 89 selected source references. These are local development
checks, not the remaining frozen compiler/tag/platform release matrix.

The full release-script contract suite passes: 702 tests on the private working
source. This checks the build/evidence/publication tooling, including rejection
of incomplete evidence; it does not execute the unavailable distribution or
provider qualification environments. Its log SHA-256 is
`5fbbc8a255b4e28a4a786bba00ba66737d3c90a898d6e2b12709c2e9cd9787cd`.

The private strict documentation build passes: 175 content pages, 16 redirects
and 30,550 internal links/assets checked without errors. Its metadata identifies
implementation checkpoint `cffe22e`, not a frozen release. The separate
documentation checkout is untouched.

## Collection contention and dependency preparation, 14 October

Real Raft/bbolt writer contention, cancellation and exact-prefix recovery checks
pass. A separately damaged ledger remains unavailable. The reviewer identified
two containment/cleanup gaps in the initial fixtures; bounded child execution
and checked close results repair both. Final Go 1.27.1 race and Go 1.25 checks
pass; independent source re-review closed both findings. See the
[scoped contention record](collection-contention-2026-10-14.md).

A clean private checkout of `cffe22e` was used to prepare the existing module
graph. Go-generated proposals refresh the unpublished SDK content checksum and
complete the application checksum inventory. Only root and worker `go.sum`
change; all three `go.mod` files and all SDK source/module inputs are unchanged.
The SDK module checksum is
`h1:Dnr4IXxwLb/kNfKAZbR76ot6mgJMk02sRItsETb58hQ=`. Preparation verifies both
private downloads and all module graphs, but does not establish public module
publication or frozen-source qualification. Proposed files were copied only
after their recorded original and resulting hashes matched. Independent review
recomputed the private SDK ZIP and module digests and matched its 118 archived
files to the current SDK. No dependency versions changed; no public download or
release qualification is claimed.

## Frozen documentation identity, 14 October

The canonical synchronization command previously copied a committed checkpoint
identity while the release workflow required the final source commit. Editing
the site identity manually then failed canonical copy validation. The new
`--release` option derives the site's metadata from matching clean `RELEASE.json`
inputs, checks recorded source bytes and the exact documentation file set, and
preserves the committed checkpoint metadata. Both generated metadata copies and
the site configuration agree. The workflow checks canonical content using that
same option before building the site.

All 38 documentation-tool tests and eight release-evidence/workflow tests pass
on the working source. Changed or missing inputs, extra archived documents,
dirty metadata and drift in generated copies fail without an attempted repair
in check mode. The canonical documentation/source-reference check also passes.
Independent source review closed the metadata correction without another
concrete finding; the reviewer did not run tests. A real render from the newly frozen source
and the complete updated matrices remain required; no public documentation
checkout or publication was changed.

## HTTP status and current-source references, 14 October

The final local acceptance audit found missing F32 regression coverage, rather
than a demonstrated HTTP execution defect. Nine real HTTP-check cases now use
the production constructor and transport to cover default/empty-list 2xx
success, default redirect rejection, explicitly accepted 302/307, mismatched
redirect codes and replacement of the default status rule. Each requires one
request to the configured origin and zero to the redirect destination. They
pass on Go 1.25 and Go 1.27.2 with `externaljobs` and race detection. The HTTP
reference now explains expected statuses, redirect handling and direct
connections without ambient proxies.

Five current guides also linked an older schema revision missing the health/TLS
fields they documented. Those links now use the existing local source-download
mechanism. Fourteen additional authored source/example files are included by
exact path; their recorded bytes, sizes and hashes match the originals.
Historical revision links and private evidence metadata remain unchanged.
The reference inventory contains 103 selected files; it is not a complete
source distribution or a qualification result.

All 39 documentation-tool tests pass, including stale-copy rejection,
preserved historical links and rejection of unapproved operator/credential
examples. Independent source review closed both finite findings without another
concrete issue; the reviewer ran no tests or builds. Canonical and rendered-site
checks are retained separately. Final frozen-source qualification and the
dependency-security decision remain open.
