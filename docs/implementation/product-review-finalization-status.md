# Product-review finalization status

Started 20 October 2026 at clean `0a22680c48e59e054fbf298d6f99c5ac62342e5a`.
This is an execution inventory, not release qualification. See the
[approved addendum](product-review-finalization-plan.md). All repairs/checks below
are pending unless explicitly updated with current evidence.

## Tickets

| Ticket | Status | Evidence/review |
| --- | --- | --- |
| 1 Effect classification and deadlines | Implemented; focused checks passed | Independent source review found no remaining blocker; final matrix pending |
| 2 Episode eligibility, Ignore and blue | Pending | Pending |
| 3 Retained action ledger and formats 44/45 | Pending | Pending |
| 4 Configuration and CLI diagnostics | Pending | Pending |
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
| N01 | Old open incident loses recovery green on upgrade | Ticket 3: require provable started exposure and original correlation |
| N02 | Fresh healthy check cancels restored queued recovery | Intentional approved cancel-on-health safety rule |
| N03 | Compiled-driver fixed capacity split | Ticket 5 |
| N04 | Capacity oscillation and frequent tuning logs | Ticket 5 |
| N05 | Raw queue durations / epoch timestamps | Ticket 6: presentation and documented wire units |
| N06 | Running workers exceed requested reduced capacity | Verify draining semantics; ticket 5, not automatically a leak |
| N07 | Small fleets lack 1,000 control observations | Intentional insufficient-evidence rule; ticket 7 documentation |
| N08 | Driver aggregate SLO differs from one monitor | Intentional aggregation; ticket 7 documentation |
| N09 | Independent installations share provider correlation | Ticket 3 |
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
| N36 | Snapshot inner permissions too broad | Ticket 3 defense in depth; private parent already restricts traversal |
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
