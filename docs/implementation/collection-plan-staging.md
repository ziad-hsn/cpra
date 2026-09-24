# Inactive collection plan staging

Status: private implementation prerequisite, 19 September 2026. This extends
the [plan artifact codec](collection-plan-artifact.md) and uses
[snapshot format 5](collection-plan-snapshots.md). Public collection Validate
and Activate are still unavailable. No collection resource is applied by these
commands.

## What is committed

An uploaded collection can retain one intended plan header and descriptor. They
bind the operation, upload, original actor, input identity format, content digest,
committed input digest/count, compiler version, observed catalog index and plan
identity. The complete intended artifact descriptor is computed before staging;
an existing operation cannot acquire a replacement plan or a replacement suffix.

The internal state sequence is `uploading -> validating -> validated`.
`validating` means that an inactive artifact is being staged. `validated` here
records successful structural verification and immutable finalization of that
artifact. It is not the public whole-collection validation verdict, an
authorization grant, a capability seal or permission to execute a catalog write.
The public coordinator still has to bind the compiler result, current authority
and server capabilities and retain an immutable successful or failed outcome.

`plan_begin` requires the complete original upload. `plan_append` admits only
the next canonical typed fragment or an exact retry of an earlier fragment.
Each fragment is at most 1 MiB; the full framed artifact is at most 32 MiB.
Input and plan records share the existing 1 GiB logical ledger quota. Up to
64 inactive collection headers can exist. These are implementation bounds,
not tested fleet capacity or process RSS guarantees.

Cross-fragment checks enforce the codec's ordering, guards, dependency ordinals,
row endings and footer. Every resource row also has to match its original input
ordinal, kind/ID and source coordinates. A syntactically valid but different
input identity is rejected before it becomes a committed plan fragment.

`Store.VerifyCollectionPlan` reads bounded frames under short locks, checks
the original input bindings and exact complete descriptor outside those locks,
and returns a conditional internal fence. No lock or bbolt transaction crosses
the later submission. `plan_finalize` compares that exact fence against the
committed header and prefix. A successful retry returns the original result
without changing its timestamp or extending its lifetime.

The derived fragment database and parser cache do not authorize execution.
Credentials and resource bodies remain in the encrypted input namespace; the
plan contains only identifiers, source coordinates and version/dependency
metadata. Verification performs no decryption and no provider operation.

## Rejection, recovery and cleanup

Invalid proposed rows, changed retries, skipped fragments, wrong descriptors
and quota exhaustion do not advance plan progress. An out-of-range proposed
input ordinal is a conflict. A missing in-range committed input or a failed
materialization write is a storage failure and stops admission. These different
outcomes prevent a malformed proposal from unnecessarily disabling the store.

The incremental parser cache is discarded after an invalid attempt, finalization,
cancellation, terminal cleanup or snapshot installation. A later append can
reconstruct it from the committed prefix. Explicit backup restore invalidates
inactive plans and discards their parser caches. Reconstruction can parse up to
the artifact's 32 MiB bound inside an Apply call; it is not yet qualified against
the controller's latency targets. Public admission and startup work must account
for that cost before making a control-latency claim.

After ordinary owner restart, a complete retained artifact can be verified and
finalized using its original identity. An incomplete artifact needs its original
remaining fragments. A fresh compilation may be used only if the entire result
matches the original intended descriptor, including the original guards; it
cannot refresh a conflicting guard. If those bytes cannot be recovered exactly,
the future coordinator must report interrupted/conflicting original work and
require a new operation for new intent. The current subprocess tests retain the
original artifact in the parent fixture. They do not implement a durable server
coordinator capable of recovering unavailable plan bytes.

Cancellation and inactivity expiry prevent further staging/finalization.
Cleanup removes at most 256 fragments or 4 MiB of plan records per command,
then removes encrypted input in bounded steps. The collection header survives
until both namespaces are empty. Cleanup compares original identity, progress,
removed totals and the finalization instant; equivalent timestamp time zones
do not turn a valid cleanup fence into a conflict. Original totals and digests
remain audit identity. Terminal cleanup does not make a shortened artifact
executable or reclaim physical bbolt allocation immediately.

## Evidence and remaining work

The implementation is checked through actual durable submission, memory/disk
ledger fixtures, snapshots, stopped backup validation and forced child-process
termination. The process tests cover a format-3 input snapshot followed by
partial format-5 plan logs, a partial format-5 snapshot followed by more plan
logs, and a committed finalization whose reply is lost. Recovery compares the
original prefix, continues only the retained original suffix and leaves active
catalog resources, monitors and ordinary operations empty.

Scoped evidence is recorded under `bin/verification/collection-plan-ledger/`,
`collection-plan-snapshots/`, `collection-plan-crash/` and
`collection-plan-integration/`. These local records identify source hashes,
commands, failures, review and observation limits. Broader checks must be read
from their recorded results rather than inferred from this source document.

Remaining activation work includes immutable validation failures and item
outcomes, capability and durable authorization fences, principal identity
non-reuse, atomic per-resource catalog mutation plus outcome/progress, dependency
failure propagation, controller completion, cancellation after partial progress,
SDK/CLI contracts and the connected browser Apply flow. See the
[activation contract](collection-activation.md). Provider-account evidence,
native packaging and the million-monitor endurance campaign remain separate
gates. Candidates remain private until all agreed gates pass.
