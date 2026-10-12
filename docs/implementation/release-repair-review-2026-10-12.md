# Independent repair checkpoint review — 2026-10-12

Scope: `2ba7b73f0c9d398bfee2fbcf3896f70ccc1fb37c` through
`b59d5b8ba60ed82f0a5b6b57857be27a2e380725` on `codex/release-repairs`.
Reviewer: a separate read-only reviewer agent, authorized by the user.
This is source review of a development checkpoint, not final release qualification.

The reviewer traced notification transitions and format migration, local and
tagged worker admission, PagerDuty/Opsgenie correlation, collection grouping and
continuation, access initialization and guards, static driver validation, and
Linux generation ownership and deletion. It ran no tests or provider operations
and excluded concurrent licensing/material changes.

| Finding | Confirmed trigger | Required repair |
| --- | --- | --- |
| R2-1: queued maintenance alerts | The controller prefilter postpones the entire monitor during maintenance. A window between health observations leaves queued notifications intact for delivery afterward. | Commit cancellation before the prefilter can defer code work; test the actual controller with no pulse during the window, including restart. |
| R2-2: warning alert correlation | A delivered warning receives a warning identity. Escalating to an outage replaces it with an incident identity, so eventual recovery closes only the latter provider alert. | Preserve provider correlation across severity escalation while notification episodes remain distinct; test warning, outage and confirmed recovery through provider dispatch. |
| R2-3: old envelope with new fields | Minimum-format selection checks notification version/deadline but misses endpoint policy IDs and other new identity fields. An old configure envelope can enter state that its snapshot reader rejects. | Check every nonzero delivery-format field at the command boundary; test rejection of old envelopes and a current-format snapshot round trip. |

The reviewer found no additional concrete deletion or pin-lifetime defect in the
inspected Linux generation path. Retirement catalog publication precedes deletion
intent removal, and native ledger closure waits for frozen readers. Its source
trace also confirmed that Opsgenie status-read failure after acceptance remains
unknown rather than repeating the accepted POST.

Native macOS/Windows retirement and growth, service permissions, full crash,
browser, compiler, provider, performance and distribution matrices remain open.
Those exclusions are not passing evidence. All three findings require repair and
review before the related work can be signed off.

## Repair verification

All three findings were reproduced with failing regression tests before the
behavior changes. The repaired source passes the focused regressions and broader
notification, policy, catalog-format and recovery-notification suites on Go
1.27.1. Complete controller-system and built-in job suites also pass.

Maintenance cancellation now commits in the controller before admission and
wakes even for a notification already waiting in a worker queue. The test opens
a real Raft store, places the maintenance window between health observations,
observes cancellation with zero receiver calls, and reopens the store to verify
that the cancellation survived. PagerDuty and Opsgenie tests execute warning,
outage and confirmed recovery through the controller, committed action guard and
production network driver; every trigger and final close uses the original
provider identity. Old envelope tests cover each new monitor/action identity
field, malformed versions, and the current-format snapshot path.

These are private development checks. A second independent review of the fixes
and final release matrices remain required; local receivers are not provider
account qualification.

## Re-review and missed-window repair

Source re-review of `ea81ade` closed the warning-correlation and format-gate
findings, and found no concrete defect in the inspected licensing/release
materials. It identified an additional maintenance schedule: the owner stops
or stalls for an entire short window and releases a retained queued alert on
resuming after the window. The first regression observed the window and restarted
only after cancellation, so it did not cover this schedule.

The subsequent repair persists the original notification queue time across
retries, checks absolute and cron windows across that interval, and cancels only
covered deliveries before start. A newly queued delivery after the window remains
eligible. Missing queue identities in older development snapshots cancel unsent
work conservatively; started, completed and unknown actions remain intact.
Known intervention results inside maintenance retain their outcome while
suppressing new notification intent.

Regressions now cover a live owner missing the entire window, a worker already
waiting in the local queue, reopening Raft before cancellation, a retry due after
the window and a new outage afterward. The focused default and `externaljobs`
checks pass on Go 1.27.1. Independent source re-review of this final maintenance
repair remains required.

The next re-review closed the missed-window mechanism on source inspection but
found that cancelling multiple destinations could exceed a valid configured
batch size of one and mark usable storage unavailable. That regression was
reproduced. Cancellation now respects both `CommandLimits` and the conservative
encoded-byte bound, reconciles a committed prefix, and schedules the remaining
suffix for another bounded owner turn. The two-destination/batch-one regression
passes with healthy storage, zero receiver effects and retained cancellations
after reopening. This final batching change still requires source re-review.

Source re-review at `e9bc6c54a11fa4a40d09b629dcebeeb6df18876c` closed the
batching regression with no remaining concrete finding in that finite change.
It verified count/byte/guard bounds, prefix reconciliation, suffix scheduling
before dispatch, final queued-job authorization and pending-work restoration on
submission error. It ran no tests and does not qualify the final release.

## Dashboard checkpoint

Source review of `5e820d7` found two display edge cases: an overview failure had
no primary heading, and an empty later filtered page could claim no matches
despite a positive filtered total. Both now have repairs and passing application
route regressions. No concrete defect was found in the other inspected missing
route, finite formatting, controller terminology, artwork attribution or
embedded-notices paths. The complete 526-test frontend run, type check and lint
passed; the two subsequent repairs passed in the eleven-test app suite. Final
source re-review of those changes remains required.

Compact `100%` text is verified in the DOM. Responsive pixel fit, browser
accessibility and embedded regeneration remain separate gates.

Source re-review at `0fbeb69` closed both dashboard edge cases. It found that
human monitor description asserted "not snoozed" while configuration projection
was pending. Snooze is now unavailable without matching status and control
identities; an observed zero deadline means not snoozed. Missing, pending and
confirmed control-state regressions pass in the complete CLI suite. Source
re-review of this final correction remains required.

The pinned dashboard rebuild passed all 528 tests, type checking, lint and
embedded-asset regeneration. This remains development evidence; the real-browser
and final frozen-source qualification gates remain open.

## Safe diagnostic checkpoint

The final snooze correction at `23989e3` passed independent source re-review.
The subsequent diagnostic implementation uses canonical cause codes/messages,
format 40/41 gates, immutable queued notification causes, and separate action
failure outcomes. Current observations, retained events, SDK types, CLI and
browser views share that contract. Raw provider bodies and error text are not
copied into these public records.

Focused Go 1.25.0/1.27.1, default/externaljobs and race checks pass, including a
real Raft snapshot/restart and an HTTP check followed by the production webhook.
Complete controller, jobs, CLI and SDK API suites pass on Go 1.27.1. The pinned
frontend rebuild passes 533 tests, types and lint. Independent source review of
this new checkpoint remains required; these are not frozen release matrices.

The source reviewer found one production dashboard gap at `382c65e`: the explicit
monitor shape registry lacked the new FailureDiagnostic reference, so normal
resource loading dropped lastFailure. The direct component fixture did not cover
that path. The reviewed generated projection reproduced the missing field after
the fixture's argument order was corrected; both intermediate fixture failures
are retained as failed checks, not successful reproductions.

The registry now includes the diagnostic. Generation rejects unresolved monitor
references, with the existing category-aware DriverConfig projection as its only
explicit exception. A resourceView-to-render regression preserves the returned
failure and removes it after success. The seven generator tests and 47-test
monitor regression suites pass. The rebuilt embedded dashboard passes all 534
tests, type checks and lint. Source re-review of this correction remains pending.

Source re-review of `b0bb4b8` closed the diagnostic projection gap. The reviewer
verified the explicit shape, generation guard and embedded notice/asset path;
no additional concrete finding was reported. It ran no checks.

## Provider response checkpoint

Review of `68d9804` found a mixed-case JSON alias defect: the SDK's exact
duplicate-key guard precedes Go's case-insensitive struct decoding. An additional
`RETRY_AFTER` could overwrite `retry_after`, authorizing a shorter retry than
the provider specified. Equivalent aliases could change Telegram/Pushover
outcome evidence. Four regression cases reproduce the defect; their failed run
is retained separately.

The classifier now rejects non-exact aliases of its recognized top-level and
nested parameter fields before binding typed values. Duplicate, depth, trailing
value and body-size guards remain intact. The complete jobs suite passes after
this correction. The provider repair's identity/deadline/replay checks had passed
before it; source re-review of the final correction remains required.

Source re-review of `29b7ad8` closed the mixed-case classifier finding and found
no concrete defect in the inspected retry-delay identity/deadline/replay paths.
It found an adjacent older Opsgenie receipt decoder that could report success
from duplicate or case-aliased processing evidence, or poll a substituted request
ID. Accepted-POST/contradictory-GET regressions reproduced all three processing
cases; those failed checks are retained.

Opsgenie acceptance and processing receipts now retain strict whole-body
duplicate/depth/trailing guards and reject aliases of recognized exact names.
Request-ID contradictions stop before polling. Real HTTP regressions pass,
including committed unknown action state, no repeated POST, and a real Raft
snapshot/restart retaining the hold. Focused jobs race checks pass. The initial
controller fixture failed compilation and then selected the wrong action kind;
the corrected correlated fixture passes and those failures remain recorded.
Independent source re-review of this final Opsgenie repair remains required.
