# Inactive collection validation results

This private implementation does not register public Validate or Activate routes
or enable browser Apply. The [shipping plan](dashboard-shipping-plan.md) keeps
every candidate private until all gates pass.

Storage format 6 adds a validation-result namespace alongside encrypted input
and successful-plan fragments. All three are frozen at one FSM index and included
in a self-contained snapshot, including mandatory empty streams. Formats 1–5
retain their original interpretation; older readers reject format 6. Stopped
backup validation and ordinary restore use the same decoder.

Format 7 retains those same streams and adds bounded original request/claim and
interruption metadata. Older format-6 readers cannot read format-7 state. The
[private coordinator](collection-validation-coordinator.md) commits its request
before claiming/compiling, preserves original authority and profile on every
artifact write, and never recompiles an already claimed attempt. Normal main now
owns a bounded worker and retires abandoned claims during exclusive Store startup.
Public validation/result routes and activation remain unfinished.

## Ownership and immutable results

New collection admission captures the named operator's durable epoch, revision
and ID inside authenticated admission. The FSM checks it before allocating an
upload. An exact admission-ticket retry preserves the original owner. Historical
uploads without an owner observation remain readable and cancellable, but cannot
acquire background authority because a textual actor name is later reused.

The [principal lifecycle](authentication-lifecycle.md) makes IDs permanent within
an epoch. Rotation preserves ownership. Validation may observe a fresh revision
for that same owner and epoch, then every staging command checks that exact
revision. Policy changes, revocation, expiry and explicit restore prevent
continuation. No bearer token or process-local counter supplies this authority.

`validation_begin` fixes one result ID, original input count/digest, authority,
capability-profile digest and complete intended result descriptor. A successful
verdict also identifies the already-finalized original plan. A rejection has its
own disposition; it cannot be represented by an incomplete successful plan.

The private adapter freezes the actual compiler result in original input order.
Rows contain resource keys, opaque source tokens, document/item positions,
classification, allowlisted issues and available incarnation/version observations.
Resource bodies, provider errors, paths, credentials and keys are excluded.
Unvisited rows are `notEvaluated`. Cancellation, authorization loss, storage
failures and unknown errors cannot become deterministic rejection claims.

Graph validation is bounded to 10,000 inputs. A larger declared inventory gets an
explicit `validationLimit` summary preserving the original count/identity with
zero result rows. This is distinct from truncating a result list. Other
deterministic failures within the graph limit preserve every input row.

`validation_append` accepts at most 256 rows with exact byte-preserving retries.
New rows must match original encrypted-input metadata; successful classifications
must also match the original plan. A malformed final row rejects the entire
submitted batch before its ledger transaction changes anything.
`validation_finalize` requires the complete original descriptor. It modifies no
catalog resource, invokes no provider and installs no controller job. Changed
descriptors or refreshed authority cannot replace the original validation intent.

## Recovery, cleanup and verification boundaries

Items are bounded to 4 KiB canonical JSON; the result artifact to 32 MiB. Input,
plan and result wrappers share the existing 1 GiB logical quota and 64-operation
staging limit. These are distinct from allocated bbolt size and process RSS.
Quota rejection preserves accepted data; missing committed rows or failed writes
make storage unavailable.

Restore verifies framing, original source coordinates, plan classifications,
phase rules, counts, bytes and digests. A rejected header requires its finalized
invalid result. Snapshot bytes are frozen against later appends. A derived plan
index is discarded at finalization, cancellation, cleanup and restore; restart
rebuilds it from original committed fragments, never current catalog versions.
Cold reconstruction is bounded but runs under FSM ownership; its maximum-size
latency remains unmeasured.

Finalized results are copied to retained history in pages of at most 256 rows.
The last page and its immutable summary seal are committed together; history
must be synchronized before that log position can be acknowledged or compacted.
Every result event uses the original finalization time, so retries and delayed
publication do not extend its 30-day retention. A partial publication is not a
readable completed verdict. Explicit late expiration also cannot claim a seal.

Result reads use bounded ordinal pages (100 by default, 500 maximum and 4 MiB)
and the committed history watermark. They retain the original descriptor after
the upload's separate 24-hour inactivity deadline and staging cleanup. Missing
unexpired evidence reports history unavailable; the fixed 30-day boundary and
monotonic retention cutoff report expiry even if the clock subsequently moves
backward. Explicit late expiration synchronizes that cutoff before the expired
header can be reclaimed; a failed catalog replacement stops admission and leaves
the previous header intact. Expiry reads recheck the committed marker across
their two protected observations. An independently retained cancellation remains
observable after its nested validation result expires. This protected storage
method is not yet a public HTTP endpoint.

Maintenance publishes one finalized-result page before reclaiming inactive
staging. Cleanup fences the exact progress and removes bounded result tails
before plan/input tails. Finalized rows cannot be removed until their history
is sealed or explicitly expired. Timestamp equality compares instants. A
canceled operation retains its original validation summary, including when
cancellation preceded the final publication page.

An explicit backup restoration keeps terminal canceled/expired outcomes while
changing the operation epoch. Metadata-only publication can finish those
original results and reclaim their staging after authentication is reprovisioned.
Ordinary operation reads and execution remain fenced by the new epoch. Impossible
partial publication offsets are rejected by header, cleanup and snapshot checks.

Process tests force-kill a child after commitment, then resume or reconcile the
original descriptor and bytes retained by the parent. Publication tests also
kill the process after a partial-history snapshot or the final row/summary log
commit, then check exact event identities, cancellation, cleanup and another
restart. This establishes snapshot and log recovery, not automatic regeneration
of an uncommitted suffix. An
interrupted coordinator needs a durable disposition or the original artifact;
recompiling against newer resource versions is not a resume. The private lifecycle
worker now records `coordinatorRestarted` for eligible abandoned claims at startup
and preserves their original committed prefixes. It permits first compilation only
for never-claimed requests, and preserves finalized and terminal results. Its
real-process restart evidence is separate from these artifact-replay fixtures.

The operation projection withholds `validated` until the retained result is
sealed and readable. Bounded worker ownership, cooperative cancellation, startup
interruption and main-process shutdown are implemented privately. Remaining work
includes public result pagination and the asynchronous Validate contract. The
[coordinator integration note](collection-validation-coordinator.md) records the
verified ordering and remaining contracts.
Activation additionally needs conditional item mutations/outcomes, dependency
failure propagation, cancellation and controller-applied progress. SDK, CLI and
dashboard apply/resume must exercise those actual endpoints.

Scoped reports are under `bin/verification/collection-validation-*` and
`bin/verification/authentication-lifecycle`, with source hashes, exact commands,
failed attempts and independent reviews. These checks establish no provider
account, native packaging or million-monitor endurance claim.
