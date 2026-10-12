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
