# Product-review finalization status

Started 20 October 2026 at clean `0a22680c48e59e054fbf298d6f99c5ac62342e5a`.
This is an execution inventory, not release qualification. See the
[approved addendum](product-review-finalization-plan.md). All repairs/checks below
are pending unless explicitly updated with current evidence.

## Tickets

| Ticket | Status | Evidence/review |
| --- | --- | --- |
| 1 Effect classification and deadlines | Implemented; focused checks passed | Independent source review found no remaining blocker; final matrix pending |
| 2 Episode eligibility, Ignore and blue | Implemented; focused checks and independent source review passed | Base/tagged lifecycle and worker checks, real Raft snapshots, dashboard checks; frozen matrix pending |
| 3 Retained action ledger and formats 44/45 | In progress: ledger, pinned readers, installation identity and snapshot protection implemented; hot-state compaction pending | Focused Linux checks and independent source review passed; macOS cross-build only; final matrix pending |
| 4 Configuration and CLI diagnostics | Implemented; focused checks passed | Base/tagged validation, local CA preparation, CLI/init and parser checks; independent source review; final browser/example matrix pending |
| 5 Reservations and destination fairness | Pending | Pending |
| 6 API/SDK/CLI/dashboard agreement | Pending | Pending |
| 7 Documentation/security/hygiene | Pending | Pending |
| Frozen candidate matrix and merge handoff | Pending | No new candidate qualified |

## Review findings

| ID | Finding | Disposition / ticket |
| --- | --- | --- |
| B1 | Continuing outage silent after maintenance/Unignore | Confirmed; ticket 2 |
| B2 | Unknown recovery has no failure-trigger notification | Confirmed; explicit blue in ticket 2; generic 500 stays unknown |
| B3 | Public private-vulnerability channel disabled | Confirmed readback in checked assessment; ticket 7, admin gate |
| M1a | Healthy manual Ignore falsely recovers | Confirmed; ticket 2 |
| M1b | Maintenance outages count toward automatic Ignore | Approved policy correction; ticket 2 |
| M1c | Ignore entry/exit not visible in logs | Ticket 2/7 |
| M1d | Counter resets and automatic Ignore recovery | Intentional approved policy; retain and document |
| M1e | File observation mode cannot mutate Ignore | Intentional read-only mode; document stopped/local versus management operations |
| M2 | Unknown intervention blocks later conflicting recovery | Intentional hold; improve classification and visibility in tickets 1/2/6 |
| M3 | Proven unsent/local failures become unknown | Ticket 1 implemented: positive pre-send evidence is definite; transient failures alone permit bounded retry. Partial writes, generic 500 and lost acceptance remain unknown. |
| M4 | Retained actions copied every check | Confirmed; ticket 3; never delete held evidence to cap memory |
| M5 | CLI masks local causes | Confirmed for input/token cases; ticket 4; not every CA error is generic |
| M6 | Missing CA configuration pages as an outage | Confirmed; ticket 4 |
| M7 | Management errors/field locations/UI mismatch | Ticket 4/6 |
| M8 | Public process narration/stale docs | Ticket 7 |
| M9 | Notification column, false verifying, offline crash | Ticket 6 |
| M10 | Late correlated latency starves healthy work | Retained trace confirmed, not rerun; ticket 5 |

## Selected minor findings

| ID | Finding | Disposition / ticket |
| --- | --- | --- |
| N01 | Old open incident loses recovery green on upgrade | Ticket 3 focused upgrade regression passes: require provable started exposure and original correlation |
| N02 | Fresh healthy check cancels restored queued recovery | Intentional approved cancel-on-health safety rule |
| N03 | Compiled-driver fixed capacity split | Ticket 5 |
| N04 | Capacity oscillation and frequent tuning logs | Ticket 5 |
| N05 | Raw queue durations / epoch timestamps | Ticket 6: presentation and documented wire units |
| N06 | Running workers exceed requested reduced capacity | Verify draining semantics; ticket 5, not automatically a leak |
| N07 | Small fleets lack 1,000 control observations | Intentional insufficient-evidence rule; ticket 7 documentation |
| N08 | Driver aggregate SLO differs from one monitor | Intentional aggregation; ticket 7 documentation |
| N09 | Independent installations share provider correlation | Ticket 3 implementation and focused regression pass: persisted installation namespace; existing action/provider identities unchanged |
| N10 | Mixed machine zones / zero times | Ticket 6 |
| N11 | Bootstrap file changes/deletion ignored after import | Intentional import-once; clarify output/examples in tickets 4/7 |
| N12 | Negative webhook recovery timeout | Ticket 4 |
| N13 | Same-document aliases / BOM rejected | Ticket 4, bounded parsing |
| N14 | CA plus skip silently ignored | Ticket 4, explicit conflict |
| N15 | Relative CA path base undocumented | Ticket 4/7; preserve cwd behavior |
| N16 | API field spellings replace YAML origin | Ticket 4 |
| N17 | Top-level version / notification trigger undocumented | Ticket 7 |
| N18 | File inline credentials versus management references | Intentional secret policy; actionable conversion guidance in tickets 4/7 |
| N19 | Examples require absent log-directory runtime settings | Ticket 4/7 |
| N20 | Protected log root fails late without cause | Ticket 4 |
| N21 | Recovery webhook defaults to empty body | Intentional configured-body contract; document, do not silently change |
| N22 | Email Date / Message-ID absent | Ticket 2 |
| N23 | Log notification lacks correlation IDs | Ticket 2 |
| N24 | Failed-recovery red lacks context | Ticket 2 |
| N25 | Misleading pre-sizing warning / 200 ms target | Ticket 5 |
| N26 | Routine Raft noise / mixed JSON-console output | Ticket 7 |
| N27 | Listen line advertises v1 only | Ticket 6/7 |
| N28 | Missing Ignore/restart-unknown logs | Ticket 2/7 |
| N29 | Diff lists update without fields | Ticket 4 |
| N30 | Apply output contains too many internal counters | Ticket 4 |
| N31 | Control revision formats differ | Opaque conditional token, not a UUID contract; document, preserve correctness |
| N32 | Revision-required errors lack discovery guidance | Ticket 4; retain mandatory conditional writes |
| N33 | Init lacks port and repeatable connection output | Ticket 4; no bearer values in output |
| N34 | CLI health vocabulary inconsistent | Ticket 6 |
| N35 | Client auth environment breaks validation | Ticket 4; retain startup authority validation |
| N36 | Snapshot inner permissions too broad | Ticket 3 protection added before state writes; focused Linux tests and source review pass. Native macOS/Windows checks pending |
| N37 | docs-check traceback / historical rewrites | Ticket 7 |
| N38 | Tracked tsbuildinfo / local agent files | Ticket 7 |
| N39 | IDEA and duplicated WASM/fonts | Inspect publication/reuse; preserve useful sources/assets/licenses, ticket 7 |
| N40 | Remote branches/issues/PRs | Outside cleanup authority; do not delete |
| N41 | Repository description / unsupported alertmanager topic | Ticket 7, metadata correction where authorized access exists |
| N42 | Desktop buttons/badges and mobile tables/IDs clip | Ticket 6 |
| N43 | URL placeholders / inappropriate secret toggles | Ticket 6 |
| N44 | Attempt/retry/failure threshold labels overlap | Ticket 6 |
| N45 | Shared settings/configuration icon | Ticket 6 |
| N46 | First failure and incident opening timestamps differ | Ticket 6: name each event correctly |
| N47 | Zero coverage-gap times / inconsistent display times | Ticket 6 |
| N48 | Observation-mode Basic-auth prompt undocumented | Ticket 7 |
| N49 | Every status chip is a live region | Ticket 6 |
| N50 | Endpoint/monitor/recipient/color terminology | Ticket 6/7 |
| N51 | Stale unsupported Kubernetes-kind comments/copies | Ticket 7; preserve dated historical scope |

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

**M4 remains open.** `Monitor.Actions` still retains historical records. The
next step is to reduce that frequently copied projection to current execution
references and aggregates, including historical blue conditions, and prove zero
historical visits/copies per ordinary successful check at 0/500/5,000 retained
records. Native permissions, tagged/race checks and the final frozen matrix also
remain open. This checkpoint does not mark ticket 3 complete.

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
