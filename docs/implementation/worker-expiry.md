# Worker action expiry before dispatch

Private implementation checkpoint: 29 September 2026. Tagged storage format 29
adds cancellation of an original recovery or notification action whose execution
window expires before a worker assignment commits. Public external configuration
remains closed pending the complete worker execution checks.

The controller anchors the deadline to the action's original scheduled time and
the authenticated, immutable JobType timeout. It does not extend the window when
preparation is delayed, retried or recovered. Expired checks retain their existing
missed-check accounting; this transition applies only to recovery and notification
actions.

## Committed transition

Expiry runs in the Raft state machine. It verifies the controller owner, monitor
incarnation, configuration and control revisions, dependency conditions, JobType,
notification source and original action identity. The action must still be queued,
with no execution assignment, start time or executor identity.

The action becomes `cancelled` with outcome `worker_admission_expired`. Its finish
time and one `action_cancelled` history event commit together. Health results,
recovery counters, cooldowns and other notification endpoints remain unchanged.
In particular, expiry does not create a confirmed provider failure that could
permit an automatic recovery retry. Normal later health observations continue to
drive the incident.

An already admitted action belongs to the existing worker lifecycle, even if it
has not reached Start. Expiry cannot replace that assignment, grant, unknown
outcome or terminal result. Admission and expiry share the state machine's atomic
decision; they cannot both win for one action.

No execution identity, encrypted assignment or worker-capacity reservation is
needed to commit expiry. Exhausted assignment capacity therefore cannot prevent
an otherwise eligible action from expiring. The existing bounded dispatcher owns
preparation, storage submission and shutdown joining.

## Replies, recovery and formats

The original action identity is the idempotency key. Repeating an accepted expiry
preserves its first finish time and creates no additional event. An uncertain
reply remains owned by the dispatcher until a FIFO flush and authoritative read
settle the original submission. If it was not committed, an eligible retry uses
the same configuration, action and deadline. Cancelling the dispatcher does not
discard an unresolved submission.

Committed state and the event survive log replay and snapshots. Replay invokes
no provider. Format 29 is required for the new command and snapshot disposition;
default builds still stop at format 14 and expose no expiry API or tagged state.
Older supported commands keep their existing minimum formats and digests.

## Verification boundary

Focused tests exercise recovery, shared-endpoint notification and inline
notification expiry; original identity guards; admission races; exhausted
execution capacity; repeated replies; owner projection; malformed commands and
snapshot downgrade rejection. Native process tests kill the storage owner before
commit, after commit and after snapshot, then reopen the same data directory and
check state and event uniqueness.

The recorded results belong in
`bin/verification/worker-expiry-2026-09-29/result.json`. These checks do not replace
the separate-process production server/worker campaign. The subsequent
[schedule-accounting checkpoint](worker-schedule-accounting.md) covers ready and
preparing checks. Sustained admission saturation, public external configuration
and independent review remain open. See [controller dispatch](worker-dispatch.md) and
the [release status](STATUS.md).
