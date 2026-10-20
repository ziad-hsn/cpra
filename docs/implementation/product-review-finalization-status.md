# Product-review finalization status

Started 20 October 2026 at clean `0a22680c48e59e054fbf298d6f99c5ac62342e5a`.
This is an execution inventory, not release qualification. See the
[approved addendum](product-review-finalization-plan.md). The ticket tables and latest checkpoint below distinguish completed development
checks from the final frozen matrix and external release gates.

## Tickets

| Ticket | Status | Evidence/review |
| --- | --- | --- |
| 1 Effect classification and deadlines | Implemented; focused checks passed | Independent source review found no remaining blocker; final matrix pending |
| 2 Episode eligibility, Ignore and blue | Implemented; focused checks and independent source review passed | Base/tagged lifecycle and worker checks, real Raft snapshots, dashboard checks; frozen matrix pending |
| 3 Retained action ledger and formats 44/45 | Implemented; base full suite and tagged regression checks pass; independent source review closed | Indexed history, snapshot joins, migration and 0/500/5,000-record checks pass; race/frozen matrix and native platforms remain separate |
| 4 Configuration and CLI diagnostics | Implemented; focused checks passed | Base/tagged validation, local CA preparation, CLI/init and parser checks; independent source review; final browser/example matrix pending |
| 5 Reservations and destination fairness | Implemented; focused checks and separate source review pass | Local network scenarios show healthy progress; aggregate fleet SLO failures retained; final-source rerun pending |
| 6 API/SDK/CLI/dashboard agreement | Implemented; focused real-server flows and independent source review pass | Frontend build/type/lint and 594 tests; blue closure, offline/mobile, field errors and conditional rule replacement pass; complete frozen campaigns pending |
| 7 Documentation/security/hygiene | Documentation/filtering and first-run checks pass; independent source review closed | Strict private site build, 47 docs tests and 17-command newcomer walkthrough pass; repository administration gates remain open |
| Frozen candidate matrix and merge handoff | Pending | No new candidate qualified |

## Review findings

| ID | Finding | Disposition / ticket |
| --- | --- | --- |
| B1 | Continuing outage silent after maintenance/Unignore | Repaired; ticket 2 episode reassessment and suppression regressions |
| B2 | Unknown recovery has no failure-trigger notification | Repaired; explicit blue with separate closure/delivery facts; ticket 2 and real-server browser evidence |
| B3 | Public private-vulnerability channel disabled | Confirmed readback in checked assessment; ticket 7, admin gate |
| M1a | Healthy manual Ignore falsely recovers | Repaired; qualifying-outage state and recovery-threshold regressions in ticket 2. |
| M1b | Maintenance outages count toward automatic Ignore | Implemented selected policy; outside-maintenance threshold and count-once tests in ticket 2. |
| M1c | Ignore entry/exit not visible in logs | Repaired; committed Ignore transition logging and ticket 2/7 checks. |
| M1d | Counter resets and automatic Ignore recovery | Intentional approved policy; retain and document |
| M1e | File observation mode cannot mutate Ignore | Intentional read-only mode; document stopped/local versus management operations |
| M2 | Unknown intervention blocks later conflicting recovery | Intentional hold; improve classification and visibility in tickets 1/2/6 |
| M3 | Proven unsent/local failures become unknown | Ticket 1 implemented: positive pre-send evidence is definite; transient failures alone permit bounded retry. Partial writes, generic 500 and lost acceptance remain unknown. |
| M4 | Retained actions copied every check | Repaired; current projection visits no historical records at 0/500/5,000 retained records; ticket 3 |
| M5 | CLI masks local causes | Repaired; distinct safe input/token/transport causes; ticket 4 CLI tests. |
| M6 | Missing CA configuration pages as an outage | Repaired; local preparation records configuration status without changing observed health; ticket 4 tests. |
| M7 | Management errors/field locations/UI mismatch | Repaired; source-attributed field errors and input focus; tickets 4/6 tests and browser flows. |
| M8 | Public process narration/stale docs | Repaired source/public filtering and references; ticket 7 strict site and newcomer checks. |
| M9 | Notification column, false verifying, offline crash | Repaired delivery counts, verification phase and offline routes; ticket 6 real-server browser checks. |
| M10 | Late correlated latency starves healthy work | Reservations/fairness repaired; local late-onset runs show healthy progress, aggregate SLO misses retained; ticket 5 |

## Selected minor findings

| ID | Finding | Disposition / ticket |
| --- | --- | --- |
| N01 | Old open incident loses recovery green on upgrade | Ticket 3 focused upgrade regression passes: require provable started exposure and original correlation |
| N02 | Fresh healthy check cancels restored queued recovery | Intentional approved cancel-on-health safety rule |
| N03 | Compiled-driver fixed capacity split | Repaired; configured eligible demand replaces compiled-driver fractions; ticket 5 reservation tests. |
| N04 | Capacity oscillation and frequent tuning logs | Repaired bounded growth/floors/healthy reduction and transition-based logging; ticket 5 checks. |
| N05 | Raw queue durations / epoch timestamps | Repaired human duration rendering and absent-time representation; ticket 6 component/API tests. |
| N06 | Running workers exceed requested reduced capacity | Intentional running-worker drain; requested/active/draining are separate, global bounds retained; TestDemandChangePreservesLeasesAndGlobalBound. |
| N07 | Small fleets lack 1,000 control observations | Intentional insufficient-evidence rule; ticket 7 documentation |
| N08 | Driver aggregate SLO differs from one monitor | Intentional aggregation; ticket 7 documentation |
| N09 | Independent installations share provider correlation | Ticket 3 implementation and focused regression pass: persisted installation namespace; existing action/provider identities unchanged |
| N10 | Mixed machine zones / zero times | Repaired UTC wire times and explicit local presentation; ticket 6 browser/component checks. |
| N11 | Bootstrap file changes/deletion ignored after import | Intentional import-once; clarify output/examples in tickets 4/7 |
| N12 | Negative webhook recovery timeout | Repaired shared recovery-timeout validation; ticket 4 validation corpus. |
| N13 | Same-document aliases / BOM rejected | Repaired bounded same-document aliases/BOM; TestStreamingYAMLAliasesAndBOM and collection/parser goldens. |
| N14 | CA plus skip silently ignored | Repaired contradictory CA/skip validation; ticket 4 pure validator tests. |
| N15 | Relative CA path base undocumented | Documented process-working-directory base and absolute service paths; ticket 4/7 docs checks. |
| N16 | API field spellings replace YAML origin | Repaired original field locations and spelling; TestSemanticErrorOriginalFieldPositions plus SDK collection tests. |
| N17 | Top-level version / notification trigger undocumented | Documented schema version and notification triggers; ticket 7 reference/site checks. |
| N18 | File inline credentials versus management references | Intentional secret policy; actionable conversion guidance in tickets 4/7 |
| N19 | Examples require absent log-directory runtime settings | Repaired runtime log-root examples and initialization; ticket 4/7 first-run checks. |
| N20 | Protected log root fails late without cause | Repaired protected-root static checks and safe cause; ticket 4 CLI/runtime tests. |
| N21 | Recovery webhook defaults to empty body | Intentional configured-body contract; document, do not silently change |
| N22 | Email Date / Message-ID absent | Repaired stable Date and Message-ID fields; ticket 2 production SMTP/format tests. |
| N23 | Log notification lacks correlation IDs | Repaired structured correlation IDs; ticket 2 log-notification tests. |
| N24 | Failed-recovery red lacks context | Repaired bounded recovery-failure context; ticket 2 notification contract tests. |
| N25 | Misleading pre-sizing warning / 200 ms target | Repaired startup target/reserve and routine warning; ticket 5 model/reservation tests. |
| N26 | Routine Raft noise / mixed JSON-console output | Repaired shared process logging defaults/overrides; controller initialization regressions and final first-run JSON assertion. |
| N27 | Listen line advertises v1 only | Repaired listener message and supported API descriptions; ticket 6/7 source/reference checks. |
| N28 | Missing Ignore/restart-unknown logs | Repaired committed Ignore and restart-unknown correlation logs; tickets 2/7 regressions. |
| N29 | Diff lists update without fields | Repaired bounded preflight field-only diff; ticket 4 CLI and management tests. |
| N30 | Apply output contains too many internal counters | Repaired compact apply summary with original handle/progress retained; ticket 4 CLI tests. |
| N31 | Control revision formats differ | Opaque conditional token, not a UUID contract; document, preserve correctness |
| N32 | Revision-required errors lack discovery guidance | Repaired revision-discovery guidance; conditional writes unchanged; ticket 4 CLI tests. |
| N33 | Init lacks port and repeatable connection output | Repaired init --port and printed connection instructions without token values; ticket 4 and first-run walkthrough. |
| N34 | CLI health vocabulary inconsistent | Repaired observed health labels; TestMonitorObservationCLIUsesAccessModeWithoutWritableResources and ticket 6 interface checks. |
| N35 | Client auth environment breaks validation | Repaired incidental client-auth environment isolation for validation; ticket 4 main-process tests; startup authority checks retained. |
| N36 | Snapshot inner permissions too broad | Ticket 3 protection added before state writes; focused Linux tests and source review pass. Native macOS/Windows checks pending |
| N37 | docs-check traceback / historical rewrites | Repaired concise docs-check failures and explicit historical regeneration opt-in; ticket 7 docs tests. |
| N38 | Tracked tsbuildinfo / local agent files | Removed tracked tsbuildinfo; ignored local agent/build outputs; git inventory and ticket 7 review. |
| N39 | IDEA and duplicated WASM/fonts | IDEA output ignored/untracked; WASM/fonts are required licensed source/distribution copies and intentionally retained; ticket 7 review. |
| N40 | Remote branches/issues/PRs | Outside cleanup authority; do not delete |
| N41 | Repository description / unsupported alertmanager topic | Open external gate: no configured repository-administration capability to correct metadata. |
| N42 | Desktop buttons/badges and mobile tables/IDs clip | Repaired desktop/mobile wrapping, scroll focus and table navigation; ticket 6 real-server accessibility checks. |
| N43 | URL placeholders / inappropriate secret toggles | Repaired placeholders and secret-control relevance; ticket 6 form tests/browser validation. |
| N44 | Attempt/retry/failure threshold labels overlap | Repaired distinct attempts/retries/threshold labels and help; ticket 6 forms and help tests. |
| N45 | Shared settings/configuration icon | Repaired distinct resource/sidebar icons; ticket 6 component source and browser layout review. |
| N46 | First failure and incident opening timestamps differ | Repaired explicit first-failure versus incident-open labels; ticket 6 observation and timeline checks. |
| N47 | Zero coverage-gap times / inconsistent display times | Repaired absent/UTC wire timestamps and local display; ticket 6 API/component/browser checks. |
| N48 | Observation-mode Basic-auth prompt undocumented | Documented observation-mode Basic credentials; ticket 7 public reference checks. |
| N49 | Every status chip is a live region | Removed per-chip live regions; ticket 6 component/accessibility checks. |
| N50 | Endpoint/monitor/recipient/color terminology | Repaired configured-notification/delivery terminology; tickets 6/7 interface/reference checks. |
| N51 | Stale unsupported Kubernetes-kind comments/copies | Corrected current Kubernetes comments and public copies; ticket 7 source-reference parity; dated records preserved. |

## Evidence boundaries

The checked assessment retained baseline reproduction evidence without changing the
source. Its performance conclusion comes from recalculating a retained receiver
trace, not a fresh campaign. Its 10,673 versus reviewer-summary 10,462 arrivals
have different/unreconciled boundary definitions; do not reuse either as a new
candidate measurement. The reported 16-hour run remains the other review's evidence.

Every completed ticket needs scoped tests and separate reviewer-agent findings.
Native platforms, live provider accounts, packaging and large fleet qualification
remain distinct gates. No publication is authorized by this implementation result.

## Ticket 1 development checkpoint

Changes on the baseline record effect classification separately from retryability,
preserve the first automatic attempt's deadline, and reject expired starts.
New writes use formats 44/45; historical envelopes keep their original semantics.
Migration cancels queued notifications without a recorded deadline instead of
manufacturing a new execution budget. A separately audited manual recovery is a
new authorization; it does not replay an unknown action.

Private evidence is under the local task evidence directory, identified by the
labels below. Each JSON record includes the baseline commit, patch and untracked
file digests, compiler, command, exit status and bounded resource accounting.
These are development checks, not frozen-candidate or provider-account evidence.

- `effects-jobs-6`: full jobs package with AWS/Kubernetes/systemd/Twilio tags,
  Go 1.27.2, passed. Actual local HTTP/TLS/SMTP fixtures cover refusal, custom CA
  roots, partial writes, lost replies, accepted SMTP data and HTTP 500 after an
  effect. Kubernetes retry-after and Docker/AWS SDK paths invoke once.
- `effects-regression-5`: broader tagged storage/controller/management check;
  controller and management passed. Two persistence fixture families required
  correction: new timeout policy must match changed endpoint lists, and a
  historical retry test must explicitly select its historical envelope format.
  The failing output remains retained.
- Release recipe/current-reader consistency and publication-lock unit checks
  passed. Publication remains locked.
- `effects-focused-6` and `effects-base-focused-2`: corrected tagged and base
  regression cases passed, including the worker rejection/snapshot/retry case,
  unchanged automatic recovery deadlines, historical envelope behavior and
  migration that invents neither retry permission nor execution time.
- Independent reviewer `effect_safety_review` inspected the final source and
  found no remaining effect-safety blocker. This was source review, not an
  independently executed test matrix. Its requested worker rejection/snapshot/
  retry regression was added to protect original-deadline propagation.

The full application/module, minimum-compiler, race, browser and documentation
matrices still belong to the final frozen candidate, after tickets 2–7.

## Ticket 2 development checkpoint

The current episode is reassessed after suppression instead of tying eligibility
only to incident creation. Maintenance-only outages do not count for automatic
Ignore, and healthy manual Ignore requires a subsequent qualifying outage.
Blue uses explicitly configured destinations and an independent review condition;
health closure leaves the intervention's unknown outcome and hold unchanged.
Prepared messages and external assignments are fenced against a changed closure
meaning. Retiring an ungranted stale assignment yields a terminal old start and a
replacement with the original remaining deadline, never a renewed budget.

- `blue-base-contracts-10`: matching focused base persistence, management, HTTP,
  controller, jobs and SDK API checks passed.
- `blue-contracts-9`: focused tagged persistence, management, HTTP, controller,
  jobs, SDK API and worker tests passed. Includes fixed historical worker identity
  projections and local provider blue correlation, email Date/Message-ID checks.
- `blue-protocol-snapshot-8`: real HTTP worker preparation, offer, terminal start,
  replacement grant, authenticated-context rejection, and real Raft blue
  snapshot/restart tests passed. Local fixtures do not prove provider accounts.
- `blue-dashboard-2`: exact Node 24.21.0/pnpm 11.22.0 build, type check, lint and
  all 567 frontend tests passed. The failed `blue-dashboard-1` record is retained:
  the new fixture needed to await capabilities, and old collection fixtures had
  expired on the wall clock. The corrected tests freeze their intended clock;
  production expiry and conditional-write guards are unchanged.
- Canonical contracts and SDK/dashboard outputs were regenerated. Blue has a
  separate text label and review icon; cyan says health verification pending.

Independent reviewer `notification_eligibility_review` found no remaining safety
blocker in the final ticket-2 source. Review was read-only; no tests were run by
that agent. The provider-event identity finding, worker stale-context finding and
old-format digest gap were repaired and covered by the checks above.

Ticket 3 still must remove retained actions and old review conditions from the
hot monitor projection. Ticket 6 still includes real-server browser coverage and
the broader observation, error, offline and layout repairs. These development
checks do not close either ticket or qualify a public release.

## Ticket 3 foundation checkpoint

Committed transitions now materialize action records into the Store-owned bbolt
generation. Records retain monitor/incarnation, episode, review and action
indexes, replay positions and predecessor versions. Snapshots freeze the ledger
at the exact committed progress marker, validate its inventory and restore it
with the collection generation. Complete stopped backups include that generation.
API/action readers pin immutable sequences without retaining a native read
transaction between requests. Independent close ownership and cancellable reads
prevent expired HTTP cursors from invalidating an in-flight response or waiting
indefinitely behind a writer during request cleanup.

Fresh installations commit a distinct namespace before admitting monitor
configuration. Complete restoration preserves it. Existing provider keys,
action IDs and started notification exposure are not rewritten. In particular,
upgrading an existing blue condition cannot open a second alert under a new key.
Snapshot state files are protected before the FSM writes them; stopped
administrative access preserves an existing service-owned snapshot directory.

- `ledger-readers-3`: persistence, management, HTTP, local administration and
  controller checks passed on Linux with Go 1.27.2.
- `ledger-reader-close-1` and `ledger-pin-cancel-1`: cancellation while a writer
  owns the ledger, final-reader release, and canceled pin creation passed.
- `installation-regression-1`: broader persistence snapshot/restore/review and
  notification-format regressions passed.
- `installation-snapshots-2`: independent installation identity, preserved
  started exposure, original blue identity, restore and Linux permissions passed.
- `ledger-darwin-crossbuild-1`: macOS arm64 persistence test executable compiled.
  This is cross-build evidence, not native execution or ACL qualification.
- `ledger-windows-crossbuild-1`: Windows amd64 persistence test executable
  compiled, including the service-grant preservation fixture. Native execution
  remains pending.
- `ledger-tagged-foundation-1`: focused ledger, reader, installation and snapshot
  tests passed with `externaljobs` on Linux.
- Failed intermediate checks are retained in the private evidence directory;
  the corrected checks have different labels and source fingerprints.
- Independent reviewer `action_ledger_review` inspected the ledger, reader
  ownership, migration identity and native permission branches. Its reported
  lifetime, old-blue identity, administrator ownership and Darwin metadata
  findings were corrected. The final source review found no remaining blocker
  in this foundation checkpoint; the reviewer ran no builds or tests.

**At this earlier foundation checkpoint M4 was still open.** The later projection
checkpoint below replaces historical monitor copies with current references,
aggregates and deferred indexed assessment. Native permission qualification and
the frozen matrix remain separate requirements.

## Ticket 4 development checkpoint

Validation retains original input locations and YAML field spelling, rejects
contradictory TLS settings/negative recovery budgets, and accepts bounded
same-document aliases and UTF-8 BOM. New collections use file profile v3;
v1/v2 remain available for original-operation continuation. No input is activated
before whole-collection validation. Local CA preparation problems retain actual
health facts and suppress configuration-induced recovery; they are not remote
outages. Original action deadlines remain authoritative.

CLI output separates input/token/TLS/authentication/staging/readiness failures,
shows bounded field-only diffs, and summarizes application without hiding its
original operation handle. `local init --port` defaults to 8060; generated runtime
settings drive the service and connection examples without printing tokens.

- `configuration-modules-3`: full SDK, CLI, local administration, runtime config,
  loader, jobs and SLO checks passed on Linux/amd64 with Go 1.27.2.
- `yaml-roots-2`: full collection/loader tests passed after retained parser
  regressions were corrected. Source-only independent review found no remaining
  parser blocker; golden/browser-WASM normalization checks also passed.
- `main-config-t4`: real-process runtime port, explicit override, protected log
  root and validation/environment checks passed.
- `collection-source-causes-1`: source access and private staging errors remain
  distinct; the original operation is consulted before resume input is opened.
- `configuration-tagged-2`: focused all-built-in plus externaljobs storage,
  controller, jobs, SDK API and collection checks passed, including local-read
  cause preservation without exposing private paths.
- `dashboard-build-t4-4`: sanitized pinned toolchain rebuild, parser goldens,
  frontend build, type checks, lint and 569 tests passed. Embedded assets and
  notices were regenerated. This does not replace real-server browser campaigns.
- `source-read-causes-2`: immediate and delayed YAML/JSON read failures retain
  only known local causes; arbitrary reader errors remain sanitized. Independent
  source review confirmed the final correction.
- `windows-ca-compile-t4`: Windows cross-build only; native execution is pending.
- `configuration-tagged-1` and `dashboard-build-t4` were stopped by the scratch
  guard, not test failures or passes. Logs remain retained. Only identified
  task-owned compiler caches/temporary outputs were removed before sequential
  reruns. Earlier failing parser and output fixtures remain in the evidence log.

Independent source reviews by `notification_eligibility_review` (YAML) and
`effect_safety_review` (configuration/CLI) informed the fixes. These are private
working-tree checks with source digests, not frozen qualification. Final example,
real-server browser, minimum-compiler and race checks remain in the final matrix.


## Current uncommitted development checks

The following results extend the earlier checkpoints. They identify their inputs
through the private runner's HEAD, patch hash, untracked-file hashes, command,
compiler and bounded resource accounting. They are not the final frozen matrix.

- `review-ledger-boundaries-1` passed the dedicated 4 MiB action/review frame
  bounds, both stream formats, exact historical revision binding and substituted
  immutable-header rejection. Collection frame limits remain unchanged.
- `review-history-1` retained a failing near-2 MiB historical revision restart
  reproduction. The history reader's old 256 KiB general cap rejected data that
  earlier command writers could admit. The repaired decoder allows the exact
  revision within the existing 4 MiB envelope, keeps the remaining event body
  within 256 KiB, and excludes operation/collection families. It rejects padding,
  unknown-member and duplicate-member inflation on the exceptional path.
- `review-history-3` passed the real-store migration, fresh observation, blue
  delivery start/result and snapshot restoration scenario, plus memory/disk
  history pagination. Tests cover interleaved daily files, expired-record byte
  accounting, cursor retries, frozen upper bounds, 65 retained daily segments,
  and bounded index migration. A page retains only small next-key metadata per
  existing segment; that metadata cost still grows with segment count.
- `history-http-1` passed actual authenticated v1/v2 HTTP and SDK reads of the
  large original execution revision under the existing response limit.
- Separate reviewer `action_ledger_review` found no remaining blocker in the
  scoped history repair after its three pagination/family findings were fixed.
  This was source review, not an independently executed test matrix.
- `persistence-base-t3-3` failed historical-format and stopped-administration
  fixtures. The corrected `replay-formats-4` subset passed. The full rerun is
  pending; these corrections do not permit administrative opens to run lifecycle
  migration or emit provider work.
- `dashboard-build-t6-6` passed the pinned production build, type checks, lint and
  all 594 frontend tests. The new `review-browser-3` actual-server scenario
  exercised blue opening/closure, continued action holds, later-outage blue,
  offline navigation and missing routes, then failed its mobile accessibility
  gate on the timeline's unfocusable scroll container. That defect is repaired;
  the browser campaign is not yet recorded as passing.
- `docs-check-t7-2`, 46 docs-script tests, seven SDK-guide tests, SDK/reference
  regeneration and `docs-render-t7-1` passed. The strict render checked 73 HTML
  pages and 18,545 links/assets in a task-owned private copy. The separate
  documentation checkout and public website were not modified.

Scheduling development evidence remains scoped: the retained late-onset local
network runs measured healthy-subset maximum gaps of 10.219/5.228 seconds at
10/5-second cadence. **Aggregate fleet p99 remained above the selected target**
(5.3/5.84 seconds); healthy-subset progress is not fleet-wide SLO attainment.
The local action-destination scenario passed with independent receiver counts,
24 slow actions sharing one destination and six healthy destinations per pool;
healthy maximum delays were 1.145/0.991 seconds for notifications/interventions,
and the slow destination peaked at one active call. Final-source reruns remain
required. No million-monitor or endurance campaign ran.

Repository hygiene disposition: `.idea/` is ignored and has no tracked files.
The source parser WASM and embedded hashed copy serve independent source-build
and `go install`/archive requirements; the brand, documentation and embedded font
copies serve their own distribution paths. Preserve these generated, licensed
inputs and outputs. The tracked `dashboard/tsconfig.tsbuildinfo` is removed.

The private-vulnerability reporting form readback remains unavailable (404).
No configured repository-administration capability is available here to enable
it or correct repository description/topics. B3/N41 remain explicitly open;
`SECURITY.md` and a working reporting channel must reach the default branch before
announcement. No application, module, image, chart or documentation was published.


## Final development checkpoint before source freeze

These checks use the current working-tree inventory, not a release identity.
Failed attempts remain in the evidence directory with distinct labels. The final
matrix will identify one frozen commit; passing development checks below do not
substitute for that matrix.

- **Ticket 3:** `persistence-base-t3-4` passes the full default persistence suite.
  `tagged-integration-regressions-3` passes the corrected tagged command-format,
  crash, receipt, review and replay regressions. `worker-ledger-review-fixes-4`
  passes native bbolt snapshot import and concurrent cold-read failure checks.
  Unknown worker receipts must retain their exact action dependency; failing
  ledger reads close admission under the exclusive FSM lock. Independent source
  review found both reported defects closed. `worker-fixture-race-4` passes the
  focused race run, including native restart/restore, failed ledger reads,
  snapshot receipt dependencies and automatic blue admission by the actual Store
  maintenance loop. Test-only provisioning uses isolated owners instead of
  changing an immutable production mode while maintenance is running. The earlier
  race report and the corrected result-command assertion failure remain retained.
- **M4:** `TestActionProjectionHistoricalGrowthDoesNotEnterChecks` exercises
  0/500/5,000 retained actions, removes ledger access while applying a healthy
  observation, and checks unchanged clone allocations and retained inventory
  after snapshot import. All action/history assertions now use indexed reads;
  production monitor observations retain only active execution references.
- **Cross-ticket integration:** `affected-default-t3-t6-2` passes controller,
  management, HTTP server, CLI, local administration, queue and jobs packages;
  its systems test compilation failure is retained. The corrected full systems
  suite passes separately in `controller-systems-ledger-5`. These changes keep
  provider-call counts, original deadlines and canceled/failed action facts in
  regression coverage rather than assuming history remains in the live map.
- **Ticket 6:** `dashboard-build-t6-16` passes the production asset build, type
  checks, lint and 594 frontend tests. `browser-final-flows-t6-17` passes the
  actual-server blue/outage/health-closure/held-action/offline/mobile flow and
  conditional notification replacement. The replacement preserves concurrent
  names, disabled state and blue rules; incompatible recipients cause no writes.
  One provider closure deliberately returns 500 and stays unknown. Independent
  source review found no remaining scoped safety blocker. Deferred closure with
  changed health, action-review closure while unhealthy, and online age-based
  staleness have backend/component coverage, not browser evidence in this pass.
- **Ticket 7:** `docs-render-t7-2` passes the strict private site build with
  73 HTML pages and 18,546 checked links/assets. Public search/output excludes
  the internal engineering records. `docs-check-t7-4`, 47 documentation tests and
  SDK reference regeneration pass. `quickstart-run-t7-3` executes 17 commands:
  protected alternate-port initialization, static validation, startup/readiness,
  apply/wait/list/describe, state/queue/SLO reads, SIGHUP, graceful stop and restored
  configuration after restart. Independent receiver accounting confirms an
  actual check. Browser trust remains manual. Review corrected the follow-on
  browser instructions to use the initializer's printed URL for custom ports.
  The printed URL served the dashboard over verified local TLS. All process-log
  records parsed as UTC JSON, and `process-logging-t7-2` passes the logger and
  controller suites. This verifies the local URL and logging contract, not an
  automated operating-system trust installation or service-manager test.
- **SDK examples:** `sdk-examples-t7-1` passes default/tagged example tests and
  vet, dependency-direction checks and the queue, AWS, Kubernetes and DAO/SMS
  demonstrations. These are private local integration/fixture checks, not live
  provider or published-module evidence.
- **HTTP connection reuse:** `http-reuse-127` measured 12 status-only HTTP/1
  checks on one TCP connection with Go 1.27.2; `http-reuse-126` measured 12 on
  twelve connections with Go 1.26.0. Both pass the slow-unused-body response test
  and lost-response GET/POST webhook no-replay checks. These small local transport
  observations are not fleet benchmarks. Production transport policy is unchanged.
- **Verification inputs:** private `dependency-preparation-2` completed module
  verification, canonical SDK/worker archives and checksum proposals. Only the
  three generated `go.sum` changes were applied; dependency versions did not
  change. Dashboard freshness and formatting checks pass. The full release-script
  unit run retained two stale-fixture failures; corrected parser-profile and
  operation-inventory regressions pass separately. The full suite remains part
  of the frozen checks. The private execution guard passes stream separation,
  deadline, bounded-output, exhausted-quota and fast-launcher-exit child-cleanup
  tests. These checks validate the verification machinery, not CPRa behavior.

The ticket/finding table references these scoped checks and the preceding
checkpoints. B3 and N41 require repository-administration capability that is not
configured here. Native platform, provider-account, distribution and large-fleet
qualification remain open; no artifact or documentation release is published.

## Matrix findings after the first freeze

The first frozen run at `3388789` was stopped after its A2A integration tests
exposed stale fixture expectations. A proven permanent Docker rejection no longer
consumes a second automatic attempt. Exhaustion cases now configure one attempt;
separate two-attempt permanent-rejection cases assert one Docker call and no
agent escalation. The focused race run passes. Terminal worker assertions read
the indexed action record rather than assuming history remains in the operational
monitor projection.

The worker blue-closure check then reproduced two production defects: assessment
shared a mutable notice pointer with the current action, and retiring an ungranted
obsolete worker message did not enqueue another assessment. The repairs detach
the assessment payload and schedule reconciliation for the original intervention
identity. `review-assessment-alias-before-1` and
`review-closure-detachment-after-1` preserve the separate failures;
`review-closure-detachment-after-2` passes both the memory/disk immutability
regression and the real HTTP worker cases under the race detector. The replacement
retains its original deadline, the old offer receives no grant, and the unknown
intervention remains held. The final matrix must run against the subsequent
frozen commit.

On `3388789`, 712 release-script tests, 47 documentation tests, private source
synchronization and a strict 73-page site check (18,549 links/assets) passed.
Those results identify that commit; they are not automatically attributed to the
next freeze. The interrupted application matrix is retained as incomplete.
