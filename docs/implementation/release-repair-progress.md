# Release repair implementation progress

Plan: [approved release repairs](release-repair-plan.md).
Baseline: `2ba7b73f0c9d398bfee2fbcf3896f70ccc1fb37c`.
Branch: `codex/release-repairs`.
Updated: 2026-10-13. The candidate is not frozen or publicly qualified.

All checks below are private development evidence for the inspected working
source. They are not a replacement for the complete final-commit matrices or
independent review. Failed reproductions and intermediate failures are retained.

| Ticket | Implemented in this repair branch | Still required |
| --- | --- | --- |
| 1. Baseline | Approved plan saved; all three reviews mapped to F01-F56 with source and evidence dispositions; other checkouts and prior evidence preserved. | Final finding dispositions and independent review. |
| 2. Notifications | Formats 40/41; episode, logical delivery and stable per-target endpoint identities; obsolete queued cancellation; maintenance cancellation; verification-pending wording; unknown HTTP 5xx; persisted overall deadlines and retry identity; canonical bounded failure causes in current state, intent, outcomes, retained events and operator interfaces. PagerDuty/Opsgenie receive committed incident/endpoint correlation; Opsgenie distinguishes acceptance from processing and closes the original alias. Focused lifecycle and real network receiver tests pass. | Complete process-crash/restart campaign and final-source matrix. Provider retry/receipt, diagnostics and maintenance checkpoints passed independent review; unsupported rejection evidence remains unknown. |
| 3. Collections | Immutable routine health publication without the FSM lock; health-read cancellation does not poison write recovery; contiguous uploads use one synchronous transaction per 64 resources/1 MiB group; exact pre-submission marker classification in API/SDK; explicit `resume` with server verification of original raw and ordered resource identities, including complete uploads; `resume-upload` retains upload-only behavior. | Real 35-50-monitor imports and interruptions across all phases; full contention/restart campaign and final-source review. Existing original-operation dashboard continuation passed the required default browser campaign. |
| 4. Storage | Store-owned completed-generation catalog, atomic reader/snapshot pins and Linux native retirement are integrated. Current/previous completed generations remain protected; retirement catalog publication precedes removal of the durable deletion intent. Sampled path-free counts and cleanup conditions are exposed through state, SDK and dashboard. Windows initial mapping is 64 MiB. Linux retention, restart, process interruption, publication failure and focused race checks pass. Existing lazy allocation, authenticated stopped backup/restore and synchronous writes remain. | Native macOS/Windows retirement implementation and execution; complete allocated-size accounting and Windows growth/footprint evidence; full backup/restore and corruption matrix after these changes; independent review. |
| 5. Access | Go-based `local init --management`, exclusive protected files and localhost CA/leaf certificate; system token-output separation; mode-aware service arguments; minimum 32 printable token characters; explicit access discovery; ignored-bootstrap warning; loopback Host/origin guards; profiling cmdline removal; bounded failed-authentication throttling with 401/Retry-After; CSP/security headers. Local initialization, API/authentication and real application start/create/restart checks pass. Dashboard type checking, lint and tests pass across the full run and focused fixture repairs. | Remaining compiler/tag browser matrix, native system service/ACL checks, trusted-operator documentation and final security review. Current embedded rebuild and required Go1.27.1/default browser campaign pass. |
| 6. Drivers/configuration | File parsers always reject unknown fields and duplicate JSON keys. Shared static driver settings validate required destinations, ranges, gRPC mode and Docker budgets in manifests, management and SDK preflight/new apply, while original-input normalization stays byte-compatible. gRPC health mode uses verified TLS by default, the configured service and SERVING-only success. Docker has separate stop grace and operation budgets. ICMP selects supported privilege mode or rejects the platform. PagerDuty/Opsgenie correlation, closure and severity fixtures pass. Complete jobs/manifest/loader/SDK API/collection suites and focused management checks pass. | Native service-identity ICMP checks; complete driver/provider and process campaign; independent review. |
| 7. Interfaces/diagnostics | Version and canonical schema explanation routes implemented and authorized; SDK inventory and access discovery generated from canonical inputs; unavailable source time remains omitted. Dashboard primary headings, missing routes, empty/filtered/out-of-range page wording, compact health label and controller terminology repaired. CLI monitor lists show observed health; human describe renders safe summary fields and explicit unavailable times/counts. Terminal controls are escaped in table/key-value cells; safe check/action diagnostics are shown consistently. | Remaining browser/compiler/tag matrix, unified logging/caller/color handling and dead-path audit. Safe problem reporting and final dashboard correction passed source review. Measured result-shard idle polling repair passed tests and review; owner-loop wakeups remain a separate open change. |
| 8. Materials/qualification | README begins with protected management initialization. Private compiler/frontend tools installed from recorded checksums. CC BY 4.0 artwork license, Ziad Hassan attribution and separate name-use note added, with archive/package/image/dashboard notice inputs. Security reporting, contribution and conduct guidance added. | Remaining documentation consolidation, generated assets/notices and packaging verification, final source freeze and all release gates. |

## Recorded local checks

The release compiler used for new checks is Go 1.27.1, checksum-pinned by the
repository installer. Earlier Go 1.27.0 checks remain earlier development
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

The last physical C: measurement was 30,977,564,672 bytes, below the initial
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
