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
| N39 | IDEA.md and duplicated WASM/fonts | IDEA.md is a tracked authored description draft and is deliberately retained; WASM/fonts are required licensed source/distribution copies and intentionally retained. The separate .idea/ editor output is ignored/untracked. |
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

Repository hygiene disposition: `IDEA.md` is a tracked authored product-description
draft, deliberately retained under the preserve-source constraint. It is not a
generated build cache. The separate `.idea/` directory is ignored and has no tracked files.
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

The next run at `2b5d934` exposed six file-mode A2A restart checks still reading
retained actions from `/api/v1/state`. The helper now reads the bounded v2 action
list, checks the original monitor incarnation and execution revision, and retains
its checks for encrypted connection recovery, held outcomes, credential exclusion
and no resending or redirection. Two worker drain cases had the same projection
assumption; their assertions now inspect the indexed record and its actual hold.
`worker-drain-ledger-before-1` preserves the four action-category failures;
`indexed-action-fixtures-after-2` passes the corrected A2A and worker cases under
the race detector. The public API reference distinguishes current execution state
from retained outcomes. These corrections change tests and documentation, not
production execution or storage formats.

The interrupted matrix is retained as incomplete. Its recorded cleanup succeeded.
Private runner checks now cover cancellation during accounting, process discovery,
initial ownership, repeated signals during cleanup, and fixture assertion failure.
`signal-checks-2` passes all five real-process scenarios; separate source/evidence
review found no remaining scoped cleanup defect. Only two identified, inactive Go
build directories were removed after verifying no live process or descriptor used
them; original logs and failing fixture evidence remain available.

Independent source review found the indexed-action test changes preserve the
original safety assertions. Its noted v1 redaction coverage boundary is covered
separately by `TestStateRedactsActiveAgentConnection`: a committed started action
retains its sealed connection internally, while the authenticated state response
omits it. `active-agent-state-redaction-1` passes under the race detector. This
check invokes no provider and does not poll the current projection for historical
outcomes.
The added redaction test's partial-start cleanup was corrected after independent
review. `active-agent-state-redaction-2` passes; the reviewer closed that finding.
`http-ledger-regression-1` passes the broader tagged HTTP-server suite under the
race detector (300.5 seconds). These focused results precede the next frozen
candidate; they do not replace its full matrix or required browser campaigns.

### Frozen fixture corrections after `c673d0b`

The next matrix exposed two test defects. The queue UTC test changed the global
`time.Local` pointer while the worker library's background timers could read it.
The two-test reproduction fails on both Go 1.26.0 and 1.27.2. A signal trace
locates the observed crash in the race detector's symbolization callback; the
exact runtime failure remains separate from the confirmed unsafe fixture write.
The corrected test compares location identities without modifying global time
configuration. A three-pass full queue race run with that correction passes;
an isolated mutation removing UTC conversion still fails the timestamp checks.
Original failures, traces and the deliberately failing mutation are retained.

The collection preflight fixture also used an absolute notification filename
under its private configuration directory. It now configures a separate log root
and submits only a relative filename. Its unchanged-resource, operation identity,
zero provider call, no file creation, restart and secret-exclusion checks remain;
the response privacy assertion also covers the actual relative input value.
The corrected real-application preflight passes with race instrumentation.
Independent reviewers confirmed both corrections and their retained assertions.

The `c673d0b` matrix is incomplete, with both original failures recorded. It was
stopped before the remaining persistence package finished, and cleanup completed
without a recorded error or surviving test process. Its static dashboard,
generation, documentation and private archive-consumer checks passed. The Linux
source vulnerability scan found no imported or called vulnerable package in all
seven variants; the built-in variants retain the dependency-only OpenPGP advisory
GO-2026-5932, whose packages are absent from their dependency inventories. These
results are scoped to that commit and do not replace the corrected final matrix.

`fixture-repairs-go1272-after-1` and `fixture-repairs-go1260-after-1` pass the
actual checkout's corrected preflight, UTC and subprocess tests with the race
detector. The reviewed diff changes only these two tests and this status record.

### Companion-check corrections after `6605bdf`

The frozen matrix was stopped before editing the candidate after required
companion checks found a frontend dependency advisory and an actual keyboard
focus escape. The interrupted matrix remains incomplete; its guard recorded
successful process cleanup. Its completed package checks, static bundle and
seven-variant Go vulnerability scan remain evidence for that source only.

The frontend lockfile now selects `source-map-js` 1.2.2, which fixes
GHSA-68fv-2mgg-jv7q. The rebuilt dashboard passes types, lint and all 594 component
tests. The verified-TLS audit reports 387 dependencies and zero advisories. The
release recorder and reader both validate the original audit payload, require
production, development and optional inventories, and reject nonzero counts even
when the command returns zero. Explicit selection arguments prevent inherited
production-only or optional-exclusion settings from narrowing the audit.
`frontend-audit-selection-1` demonstrates all 387 dependencies under those
adversarial settings; `frontend-audit-gate-unit-after-3` passes 22 reader/recorder
regressions, including incomplete and filtered zero-exit reports. Independent
source and retained-output review closed both audit findings.

The Dialog focus trap now handles a focused validation summary outside the
sequential tab order. `companion-accessibility-after-2` passes 15 scenarios,
including automatic error focus and both Tab directions, with the original
pending-operation and background-inert assertions retained. The earlier failed
focus case remains available. This fixture accessibility campaign is distinct
from the required real-server browser matrix. Separate source and evidence
review closed the focus correction without executing new tests.

Local provider helpers now configure a dedicated log root, expect the corrected
Datadog title and verify the exact outcome classification for each response.
They refuse existing or dangling report/evidence paths before starting fixtures;
suite summaries are exclusively created and updated through the owned stream.
`companion-fixture-unit-after-2` passes 21 regressions;
`companion-verification-local-after-1` passes four production-path local cases;
`companion-verification-contracts-after-1` passes 84 HTTP contract cases; and
`companion-verification-protocols-after-1` passes 14 protocol cases. These are
local fixture results, not provider-account or health-RPC qualification.
Independent source review closed the fixture corrections without running tests.

The corrected source requires a new freeze and final affected matrix. No result
above qualifies publication. Only the inactive, task-owned Go compiler temporary
directory from the interrupted run was removed after checking process mappings
and open descriptors; logs and failing evidence remain retained.

### Final acceptance-coverage corrections after `39d5d86`

The release guide now agrees with the durability reference on formats 44/45,
retained actions and the complete generation/catalog backup inventory.
The real-server control scenario additionally performs mobile Snooze/Unsnooze
at 390×844, asserting the required reason, exact revision precondition, one
request per write, committed control state, audit history and actual check pause.
`browser-mobile-write-after-1` passes under Go 1.27.2 race instrumentation
(79.4 seconds). Existing reader, navigation and draft-discard assertions remain.
Independent source review closed the additions; this focused run is separate
from the complete frozen browser matrix.

`TestNotificationRefusedReceiverRecoversThroughOwnerRetry` exercises an actual
refused TCP connection, the committed controller start/result paths and the
owner's retained retry schedule. The same reserved socket then accepts HTTP:
two dispatches produce exactly one POST, preserve the delivery identity and
original deadline, and admit no third action after further unhealthy checks.
Duplicate result submission is idempotent. `notification-refusal-owner-after-1`
passes under the race detector; independent source and output review closed this
previously missing combined scenario. Its Store is memory-backed; separate
restart, snapshot and disk tests retain their own evidence scopes.

The concurrent `39d5d86` matrix, browser and release-script checks reached the
6 GiB scratch guard and were terminated with successful owned-process cleanup.
They are incomplete, not passing or reproduced application failures. Subsequent
heavy campaigns run sequentially. Only inactive compiler directories and two
superseded task binaries plus the old queue diagnostic executable were removed;
their paths/sizes and binary hashes are recorded with the cleanup evidence.
The original logs and failing baseline records remain retained.

The private newcomer runner now includes actual target shutdown/rebind and
incident opening/closure with the documented 30-second cadence and thresholds.
It still requires execution against newly built, hash-matched candidate binaries.
Final evidence must not attribute a binary to HEAD merely because the runner
records HEAD. Likewise, the 0/500/5,000 action-growth test proves bounded hot-path
work using failed/rejected actions; unknown/review/blue and authenticated restore
semantics are separate smaller tests, not that entire scale/lifecycle combination.

### Browser evidence inventory correction after `b6e86dc`

The default application matrix at `b6e86dc` completed module verification, vet
and race tests across 32 packages, with 5,752 passing test cases. Its 16 skipped
entries are separately invoked browser tests and subprocess helpers; they are
not evidence of browser execution. The following optional-driver vet reached the
scratch reserve and was stopped with successful owned-process cleanup. That
matrix is incomplete, not an application failure. Subsequent heavy variants run
separately with task-owned compiler cache cleanup between them.

The browser campaign had nine required tests and 18 screenshots, while the release
reader still required eight and 11. The reader now requires the complete current
inventory. A parity regression compares ordered test selection, the exact image
set and duplicate image names. All 25 focused reader tests pass; independent
source review confirmed missing/skipped coverage, missing screenshots, tampered
inputs and incompatible source identities still fail. This correction also makes
the notification browser test a recognized companion in the Go matrix reader.
The completed Go outputs retain their original source identity and are not
relabeled as results for the next frozen candidate.

`browser-inventory-release-regression-1` passes all 715 release-tool regressions
with this correction (202.5 seconds). This is development evidence; the frozen
run must still execute the browser campaigns and their retained-input readers.

Only the inactive task-owned compiler cache and abandoned compiler temporary
directories were removed. Cleanup records retain directory identities and sizes;
evidence, source artifacts and other worktrees remain unchanged. The system's
pre-existing non-dumpable user-session services were identified separately from
the inspected task builders; cleanup does not claim access to their protected
process details.

### SLO observation correction after `52c2ae1`

The completed private static, generation, frontend, documentation, archive-consumer,
provider-fixture and four browser campaigns retain source identity `52c2ae1`.
The corrected 20-command newcomer walkthrough also passed against that commit's
hash-matched executables. Its first attempt mistakenly treated a pending incident
as already open; the corrected helper waits for the same incident to open before
restoring the target and then waits for closure. The failed attempt is retained.

Inspecting the real-network scheduling telemetry exposed a remaining N47 defect:
the dashboard's v1 SLO endpoint emitted year-one gap boundaries, although the v2
mapping already omitted them. The late-HTTP sequence was stopped before source
editing, with owned-process cleanup confirmed. Its interrupted result remains
incomplete. The two completed 90-second scenarios retain their original progress
results and aggregate latency-target failures; they do not qualify a later commit.

The observation view now omits absent gap timestamps and projects known times to
UTC without rewriting persisted state. The dashboard uses its existing local-time
formatter, suppresses invented ranges and identifies a missing endpoint in a
partially known range. Coverage, counters, histogram arithmetic, snapshot encoding
and formats 44/45 are unchanged. Fresh, restored, partial and offset-input Go
regressions reproduce the defect before the repair and pass afterward. The v1
authenticated handler assertion covers the actual dashboard route. Go 1.26's full
SLO package and focused Go 1.27.2 race checks pass; the rebuilt dashboard passes
types, lint and all 600 tests. Independent source review closed the correction.
`slo-gap-browser-after-1` passes the actual recovery-review browser scenario with
race instrumentation, including UTC/absence checks on the response and rendered
SLO panel. Restored and partial boundaries are covered by the Go and component
cases. The complete new frozen checks remain required.

The independent scheduling review also corrects earlier wording in this record:
the action fixture exercises six healthy actions sharing one healthy destination
alongside one stalled destination, separately for notification and intervention
pools. It does not establish six independent healthy destination flows. Scenario
pass flags assert progress; retained telemetry must separately establish sampled
capacity bounds and measurement availability. Aggregate SLO misses remain visible.

A fresh public GitHub API readback on 20 October reports private vulnerability
reporting disabled; GitHub still detects no security policy on the default branch.
The candidate's policy is not published by these private checks. Repository
administration remains an external gate.

### Worker admission, result delivery and snapshot validation after `76b324e`

The frozen `76b324e` static/frontend/documentation, browser, provider-fixture,
newcomer, vulnerability and bounded scheduling results retain that exact source
identity. The complete matrix was not qualified. The separate-process worker
load companion exposed additional admission and result-delivery defects;
subsequent corrections require a new frozen candidate.

Pure worker observation timeouts now use the read-health classification rather
than creating unresolved-write recovery. Existing unresolved writes still close
admission until reconciled, and permanent storage failure remains unavailable.
Worker polling prepares only enough eligible, wire-fit offers for the requested
capacity, while preserving committed ordering, suppressed-offer cancellation
and bounded continuation. A unique preparation identity prevents an older
concurrent poll from replacing a newer cursor. Expired-action reconciliation
reads the original retained action from the ledger rather than assuming it must
remain in the live monitor map. `worker-admission-after-3` passes 28 focused
race test cases. The independent source review is recorded separately.

The worker delivers results through at most four concurrent protocol calls,
further bounded by configured handler concurrency. Each execution remains
serial. Original deadlines order ready results; they are never renewed, and an
expired delivery does not discard its original outcome. Reconciliation rotates
through a bounded subset of held records so it does not make a complete retained
inventory of network calls before considering fresh outcomes. Fatal journal or
server-identity errors close admission, cancel sibling calls and join them before
journal closure. Lost replies retain the identical durable outcome. This does
not promise a maximum delivery delay during a result backlog or stalled calls.
`worker-result-concurrency-after-2` passes all 141 worker-module race cases.
`worker-result-concurrency-smoke-1` passes the real-process 60-second workload.
Independent source review closed the implementation and test cleanup findings.

The load fixture keeps each outage eligible until its independently observed
recovery/yellow/green effects complete. The receipt statistics distinguish HTTP
replies from accepted, finalized or unknown dispositions. Original concurrency,
operation budgets and measurement length remain unchanged. Both
`worker-result-concurrency-load-1` and `worker-load-restore-diagnostic-1` completed
the five-minute measurement but failed the final restart's 20-second observation
deadline. They are failed complete checks, not load passes. The diagnostic helper
initially captured Linux stacks after that unchanged timeout, with a bounded
two-second diagnostic interval and existing owned-process cleanup. Captured
stacks showed active retained-state verification. The later readiness-only
allowance and unconditional-restart correction are described below.

A private copy of the completed, immutable 10,761,631-byte fixture snapshot
contains 64 monitors and 2,163 retained worker executions. It is a decode fixture,
not a complete backup. The first profiling attempt used an unsuitable directory
and failed the directory protection check. The corrected race-instrumented
`worker-snapshot-decode-before-2` completed decoding and validation in 8.697 seconds.
The CPU profile confirmed repeated full execution scans during retained-action
validation. A pass-local, quota-bounded action-to-execution index now preserves
exact binding/fencing/outcome checks without repeatedly scanning that inventory.
Duplicates and contradictory retained receipts fail; only absent retired receipts
may use the existing terminal-action allowance. Reverse joins and full execution
validation remain intact. No persisted format or replay envelope changed.

`worker-action-validation-after-1` passes 45 focused race cases, including existing
native snapshot corruption cases for unknown results/finalization. Independent
source review closed the new lookup and its tests. The same snapshot's post-change
decode took 7.720 seconds in `worker-snapshot-decode-after-1`; that invocation also
ran the separately scoped forward-join benchmark afterward, so its complete CPU
profile includes both. These are local race-instrumented diagnostic measurements,
not fleet or native-platform capacity claims.

The post-index `worker-load-restoration-after-1` still failed the shared 20-second
observation deadline during authenticated worker verification. Source review
confirmed that this was a fixture deadline, not the application's startup
contract. A separate private diagnostic allowed 60 seconds only for server
readiness, but its workload retired no check receipts, so the old conditional
final-restart branch did not execute. `worker-startup-diagnostic-1` therefore
proves load/drain only; it supplies no final restoration measurement.

The test path now always restarts after the load, independently of
receipt-retirement counts. Only server readiness has a 60-second allowance;
receipt and observation waits remain 20 seconds. Original action deadlines,
handler duration, concurrency and the five-minute measurement remain unchanged.
It records process-launch-to-observed-readiness and emits its final evidence only
after restart, any applicable explicit-expiry checks and graceful shutdown pass.

`worker-load-restoration-after-2` passes with race instrumentation on Go 1.27.2:
300.001 seconds of measurement, 64 monitors, 828 check effects, 740 notification
effects and 382 recovery effects; peak concurrency 16. Independent accounting
finds zero duplicate invocations or unreconciled outcomes. The journal drains to
zero records, outcomes, unknown actions and live/reserved bytes, with 131,072
allocated bytes. All 57 retired check receipts return authoritative expiry after
restart. Final launch-to-observed-readiness is **39.061 seconds**. The test takes
395.75 seconds; the guard, including compilation, takes 483.82 seconds and peaks
at 4,768,313,344 task bytes, with no limit interruption or cleanup error.

The retained final-startup CPU profile covers 39.55 seconds and shows decoding,
action/worker validation and significant race-instrumentation work. Worker-page
reads account for 0.48 cumulative sampled seconds of 39.29 total sampled CPU
seconds; this does not justify treating pagination as the whole startup cost.
No optimized-build, native-platform or larger-fleet startup claim follows from
this single instrumented measurement.

The evidence reader now requires a finite positive final-restart measurement and
rejects a combined load/restart duration exceeding the raw test completion time.
Both new regressions fail before the reader change; all 29 worker evidence tests
pass afterward in `worker-restart-evidence-after-1`. Prior reports without that
measurement cannot satisfy the strengthened final campaign contract. Independent source review closed the restart helpers and reader, and verified
the retained successful load-log hash. The new frozen application/SDK/worker
matrices remain required.

`worker-action-base-validation-after-1` passes 61 untagged action-projection,
retained-action and ledger race cases on Go 1.27.2, including rejected imported
executor evidence and snapshot restoration. This remains development evidence;
the final clean source needs both compiler/build-tag matrices and their required
companions before a merge-readiness claim.

### Candidate checks and worker dependency checksum after `5479764`

Frozen `5479764` passed release-tool tests (717), support-script tests, documentation
synchronization/rendering and dashboard-harness tests. These results retain that
source identity. An abandoned negative-compilation fixture from an earlier
scratch-limited run initially made package discovery include its intentionally
invalid program. Its exact source and process ownership were checked before
removing only that disposable fixture; the failed result remains retained.

The subsequent parallel race matrix failed the existing backup test's collection
cleanup allowance. Three unchanged isolated race repetitions passed, including
authenticated backup/restore, retained generations and action/history checks.
Independent review identified a possible repeated-preparation liveness risk under
the 25 ms cleanup budget. Those isolated passes do not establish a cleanup repair
or erase the contention failure. Production cleanup limits and assertions are
unchanged. A serial package matrix preserves race instrumentation, concurrent
test behavior, original deadlines and the full selected package inventory.

Archive preflight then found that `examples/sdk/go.sum` still described the
previous unpublished worker payload. The serial matrix was stopped with owned
process cleanup before changing source; it remains incomplete. The canonical
module archive and an isolated `GOWORK=off` Go download reproduce the checksum
rejection. Removing only the obsolete private-worker checksum from a copied
consumer lets Go generate its replacement; no module version, module definition,
SDK checksum or third-party dependency changes. The downloaded archive matches
the selected source bytes. The public worker-tag lookup returned 404, and no tag
or artifact was published. `worker-checksum-preparation-1` retains the original
failure, Go-produced proposal and exact download identity.

The checksum correction requires a new frozen candidate. Full archive-consumer
verification precedes the remaining matrices so workspace resolution cannot hide
this distribution mismatch. The collection contention result and external
native-platform/provider/packaging/security-administration gates remain visible.

### Collection retirement preparation and owner cancellation after `0bbcb4f`

Further source review established a repeatable no-progress schedule: successful
intent/directory synchronization could consume the 25 ms budget, causing the
next unlink to be rejected on every turn. Missing-directory finalization had the
same boundary. `cleanup-preparation-starvation-before-1` retains failing native
fixtures for those two phases on Go 1.27.2/Linux amd64 with race instrumentation.
The source defect is consistent with the earlier contended backup failure, but
that old run has no phase timings and is not attributed to this cause.

The correction retains one deadline from the owner's turn and checks it before
admitting one bounded durability/unlink/checkpoint unit. Successful synchronization
is not cached across turns. Fresh native identities, ownership, selection, pins,
intent bytes, synchronous writes and the one-unlink limit remain mandatory. True
caller/shutdown cancellation can still veto mutation; a completed unlink still
receives its required checkpoint. Store maintenance uses its lifecycle context,
and shutdown cancels and joins it before releasing exclusive storage ownership.
Discovery and sampling retain their original deadline; successful retirement
transitions return without further scanning or sampling. An overrun is observed,
not described as a hard 25 ms bound or an atomic filesystem transaction.

`cleanup-preparation-starvation-after-2` passes the focused primitive race tests.
`cleanup-owner-budget-after-1` also passes owner, real background, shutdown,
restart, snapshot, native-identity, protected-generation and interrupted-process
regressions. The persistence package takes 28.892 seconds. The background fixture
retires one generation, accounts for exactly 16,805,965 apparent bytes, retains
the current/previous ledgers and committed input, and stays ready. Its final
observed step takes 115.488 ms, with `budget_exceeded` reported. Apparent unlinked
bytes are not measured physical free space. The subsequent test refinement
requires the held-lock failure specifically and avoids holding the owner mutex
across a collection read; its follow-up checks remain separately recorded.

The ongoing all-built-in-driver matrix at `0bbcb4f` was deliberately stopped
before editing. Its guard confirms interruption, unchanged source and completed
owned-process cleanup; it is incomplete. The completed default matrix and four
browser campaigns retain their original source identity. This correction needs
a new frozen candidate and affected checks. No storage format, command envelope,
SDK/worker archive payload or publication state changes in this repair.

The tagged follow-up `cleanup-owner-tagged-backup-after-2` passes the persistence
regressions and both collection backup/restore scenarios, but the complete
invocation fails an unrelated backup-format assertion. That fixture committed
only built-in state and incorrectly expected the binary's maximum supported
format. The committed state correctly remains format 44, including in a tagged
binary; committing optional JobType state requires format 45. Production format
selection, backup inspection and restoration are unchanged.

The corrected fixtures explicitly exercise both formats through log-only,
snapshot-only and snapshot-plus-newer-log recovery. They retain authenticated
manifest tampering, unsupported-format, restore-authority-reset and unchanged
backup assertions. `cleanup-backup-format-after-1` passes both fixtures together
with the owner/background/shutdown race regressions in the tagged build. Package
times are 10.252 seconds for persistence and 10.840 seconds for local administration;
the guard takes 68.489 seconds with a sampled peak of 4,407,320,576 task bytes.
The background case again retires exactly 16,805,965 apparent bytes and reports
its 110.183 ms final step honestly. `cleanup-backup-format-base-after-1` separately
passes the shared fixture in an untagged race build, taking 5.843 package seconds.

Independent source reviews close the cleanup correction and the backup-fixture
correction without remaining findings in those scopes. The reviewers did not
execute tests; the recorded invocations supply the separate execution evidence.
These focused checks are private development evidence and do not replace the
new frozen candidate's affected matrix or native-platform qualification.

## External-worker blue fixture correction

The combined built-in/external-worker matrix at `076f738` exposed two older test
fixtures that still classified blue notifications as invalid. The production
validators already accept the approved blue slot. The interrupted matrix retains
both failing assertions and remains failed/incomplete; shutdown completed before
the source was edited.

The binding fixture now authenticates blue alongside every other configured
color and checks its exact source and pinned JobType identities. Unsupported
purple remains a rejected input. The descriptor fixture accepts blue only for
the notification category and rejects both a recovery-category blue slot and
an unsupported color. Existing identity fences, immutable records, cancellation,
parameter-copy and secret-redaction assertions remain intact. No production
implementation or storage format changed.

`blue-worker-bindings-after-1` and `blue-worker-bindings-external-after-1` each pass
all 21 selected cases under Go 1.27.2 with race detection, covering combined and
external-only tags respectively. Independent source review closes the fixture
correction. These focused working-tree checks do not replace the updated frozen
candidate's affected matrix.

The same review corrected N39's inventory wording: `IDEA.md` is deliberately
retained authored source, distinct from ignored `.idea/` editor output.

## Historical JobType snapshot fixture correction

The complete combined-driver race run at `72b69bc` finished all 32 application
packages. Its retained output contains 7,051 passing test/subtest entries, 25
skipped entries and one failing historical snapshot assertion. The overall run
is failed; skipped browser and load campaigns still require separate execution.

That fixture deliberately omits current startup migration and commits a format-16
JobType operation. It now checks both the original and decoded image version and
the format-16 snapshot header. Existing ledger-byte, collection-state, JobType-state
and row-validation checks remain unchanged. The ordinary-open test continues to
require format 45. No production behavior or storage format changes.

`jobtype-snapshot-combined-after-1` and `jobtype-snapshot-external-after-1` pass both
tests together under Go 1.27.2 with race detection. Independent review found no
other mismatched historical expectation among the snapshot assertions changed
alongside this one. These focused working-tree checks retain their original
scope; the updated frozen candidate's affected matrix remains required.

## Compiler security correction — 21 October 2026

The complete Go 1.27.2 combined application, SDK and worker partition at
`3021ee72d178d000aad57a587ad6e1d81bad043c` passed. Application output records
7,052 passing test/subtest events and 25 classified skips across 32 packages.
SDK default and external variants record 1,280 and 1,422 passing events; the
worker records 141. Those modules have no skipped test cases; the SDK transport
packages have no test files. Separate reviewers verified the retained guards,
logs, source inputs and coverage. These results remain bound to that commit
and compiler, with browser/load companions and other variants still separate.

The subsequent seven-variant vulnerability scan found
[GO-2026-6809](https://vuln.go.dev/ID/GO-2026-6809.json), published in the Go
advisory database on 21 October. It reports a certificate-verification call path
in the application and SDK, and an affected import without a reported call in
the worker. The scan remains a failed security result; no malicious certificate
was presented or accepted in this evidence. The upstream fixes are Go 1.26.10
and Go 1.27.3, released on 19 October according to the
[official release history](https://go.dev/doc/devel/release).

All seven modules now declare Go 1.26.10 and the release recipe pins Go 1.27.3.
Official distribution checksums, developer-image identity, CI selectors,
verification expectations and current documentation move together. The module
minimum alone still permits vulnerable Go 1.27.0–1.27.2, so source-user guidance
requires the patched versions explicitly. Dated records retain their old pins.

Independent review confirmed the twelve archive pins and developer OCI index,
and closed the private dependency-checksum correction: eight generated rows in
three files, with unrelated dependency sums unchanged. Private preparation
verified the two canonical module archives and all four dependent module graphs;
it does not establish public module availability. SDK regeneration changed only
its input record. The rebuilt Go/Wasm parser, dashboard assets and notices use
Go 1.27.3; dashboard build, type checks, lint and all 600 tests passed. These are
working-tree development checks, not the new frozen matrix.

The patched compiler and generated inputs require a new source freeze and
affected verification. The separate module-only OpenPGP advisory remains
recorded. Source publication, native platform/service qualification, provider
accounts and larger comparative qualification remain open. No publication,
million-monitor campaign or 24-hour run was added.

## Compiler requirement in A2A evidence

The frozen release-tool suite at `7cd3435` ran 717 tests and failed with 14
assertion failures and nine errors. Its A2A reader still required the literal
`go 1.26.0` directive, rejecting the patched reference module before later
evidence checks. That failed result remains retained.

The reader now requires the exact escaped minimum from the release recipe.
Source fingerprints, upstream SDK identity, executable compiler, dependency
checksums and evidence boundaries remain enforced. A new regression supplies
fingerprint-consistent older, newer-minor, missing and malformed directives and
requires rejection. All 16 focused A2A and qualification integration tests pass;
this working-tree result does not replace the next frozen suite. Application
behavior, generated assets and storage formats are unchanged by this correction.

## Dashboard development-tool security patch — 21 October 2026

The frontend audit at `e568465` failed on three editor-launch advisories in
Vite 6.4.3: [Windows argument escaping](https://github.com/advisories/GHSA-698v-4r3x-3vcv),
[editor argument interpretation](https://github.com/advisories/GHSA-r825-w8mm-5pxw)
and [Windows path validation](https://github.com/advisories/GHSA-587h-7wmj-h5pm).
The retained audit reports two moderate and one high finding. These concern
development tooling; the audit does not demonstrate exploitation of CPRa's
embedded dashboard.

The patch updates Vite to the upstream-fixed 6.4.4 release. Its lockfile also
updates the resolved build dependencies Rollup, PostCSS and Nano ID within their
existing supported ranges. The dashboard, generated input manifest and dependency
notices must be regenerated and verified together. Runtime Go code, module
requirements and storage formats are unchanged.

The completed `e568465` checks retain their original identities, including both
Go 1.27.3 browser campaigns and the local HTTP connection-reuse regression.
The failed frontend audit remains failed. The changed build inputs require
independent review, a new frozen source and affected verification; no release
or provider qualification is inferred from this patch.

The working-tree rebuild passed all 600 frontend tests, type checks and lint.
All 44 recorded outputs, including embedded assets and notices, are unchanged;
only the package and lockfile input hashes changed in the build manifest. The
updated dependency audit reports no advisories. These development checks do not
replace the next frozen verification.
