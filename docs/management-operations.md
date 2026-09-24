# Operation observations

**Private candidate: collection listing and dashboard detail have scoped
qualification and independent review.** The management API exposes
`GET /api/v2/operations`, `GET /api/v2/operations/{id}`, and the separate retained
result endpoint `GET /api/v2/operations/{id}/validation`. Discovery and current
permissions advertise these reads. Candidates remain private while their release
gates are unfinished.

An operation handle identifies one server-admitted request. It is distinct from
a resource version, monitor control revision, incident triage revision and
execution identity. Inspect the original operation after an uncertain response;
allocating a new operation is not a retry of the original request.

## What a receipt means

Ordinary resource/control receipts may be `reserved`, `committed`, `completed`,
`failed` or `partial`. Reserved handles have not committed a resource or action
mutation. `committed` confirms durable admission; controller application is
separate. A rejected admission can have a retained handle with `failed`, zero
committed and zero applied items. A completed control-plane receipt is not proof
of notification delivery or successful recovery.

Collections retain their original `identityFormat`, `contentDigest`, `itemCount`
and upload progress. They can be uploading, validating, validated, rejected,
interrupted, canceled, expired or invalidated. An empty ordinary `items` array does
not mean a collection's validation evidence is missing: its original resource
results have their own read endpoint. The current collection path reports zero
committed and applied resources because collection activation remains unfinished.

An explicit `validated: true` means the original retained validation passed;
`validated: false` means it was rejected. Pending or interrupted work omits this
field. Retained validation and current operation phase are separate facts. For
example, staging can expire or be canceled while a previously sealed verdict is
still retained. Neither a passing verdict nor a receipt read activates resources.
Receipts contain allowlisted identities, digests and outcomes, not resource
bodies, credentials, private source names or provider diagnostics.

## Visibility

Ordinary resource/control operation receipts keep their shared read visibility.
Collection receipts belong to their original actor. Listing requires
`ListOperations`, and detail requires `GetOperation`. To include or read
collections, the server also requires current `Get` access to every supported
resource kind. A caller lacking that broader read floor still receives ordinary
operations. Foreign collection handles return not found without disclosing their
phase.

Reading validation results additionally requires `GetOperationValidation` and the
same supported-resource read floor. The dashboard uses the exact discovered read
permission to offer that action; it does not infer access from a role or phase.
Current authorization is checked for each read. A new authorized token for the
same permanent actor can start a fresh query, while previously issued cursors
remain bound to their original authorization generation.

## Bounded lists and frozen cursors

Lists default to 100 operations and permit at most 500 per page. The optional
`monitorID` selects operations whose original target is exactly that Monitor. It
includes monitor controls and action operations, and excludes shared Credentials,
notification resources and collection-wide receipts. It does not infer collection
membership from submitted filenames or current monitors. A nonempty `selector`
returns HTTP 501 until operation label-filter semantics are defined.

A query freezes ordinary and owned collection metadata at one durable boundary.
The order is ordinary live handles, ordinary terminal event positions, owned
collection live handles, then owned collection terminal handles. Each phase has
ascending order; the complete list is not a global chronological feed. Pages can
cross phases. Later insertion, validation, cancellation or cleanup does not
rewrite an original snapshot or duplicate a live receipt in its terminal phase.

A page can be empty and still have `nextCursor` when bounded scanning encounters
many other actors' retained receipts. Continue until the cursor is absent. Each
page has a 10,000-inspection budget across source/index work; reading all retained
operations is an explicit traversal, not an implicit fleet-sized response.

Cursors are signed and bind principal, authorization generation, page size,
operation-list kind and exact monitor filter. They expire after five minutes and
share the management snapshot limits of 64 globally and 16 per principal.
Operation snapshots share a 64 MiB global and 16 MiB per-principal budget for
conservatively accounted detached receipt copies. In-flight capture reservations
are charged before allocation. Quota exhaustion returns HTTP 429; an existing
valid cursor does not need another snapshot copy.

Serialized operation pages have an 8 MiB ceiling. Expired cursors, changed
authority, restore/retention invalidation, unreadable history and inconsistent
primary/index evidence fail explicitly. The server does not truncate a page or
replace an original cursor with a fresh snapshot. New queries can obtain a new
view once the corresponding condition permits it.

## Read the original validation result

In the dashboard, open **Operation progress**, select the collection, then choose
**Read original validation result**. Opening the receipt does not automatically
read result rows. The result panel distinguishes pending work, original rejection,
interruption without a verdict, access denial and unavailable/expired evidence.
It does not infer a verdict from the current operation phase.

The panel retains one page of at most 100 resource results and uses a 4 MiB
response ceiling. **Next validation page** advances within the original result;
**Refresh validation from first page** checks the same immutable summary and
inventory. A changed identity or summary is rejected. Source attribution uses
opaque tokens with document and item numbers; discarded local filenames cannot
be reconstructed. Unknown collection identity formats remain read-only receipt
metadata without an unsupported result request.

**Stop reading validation**, hiding the result, navigation and sign-out do not
cancel server work. Pending reads are aborted and late replies are discarded.
Denied access clears displayed rows. The panel never submits validation, retries
a mutation or requests activation. Pending validation can be read again explicitly;
this panel does not poll its result endpoint.

The Go SDK provides `Operations.List`, lazy `Operations.Iterate`,
`Operations.Get`, `Operations.Validation`, `Operations.ValidationItems`, and
`Operations.WaitValidation`. The latter waits by GET only and preserves the
original handle when the caller stops waiting. `POST /operations/{id}/validate`
is a separate, implemented asynchronous admission returning HTTP 202; it is not
part of these observation methods. See the
[validation contract](implementation/collection-validation-coordinator.md).

Inactive staging expires after 24 hours without progress. Original finalized
validation evidence is retained separately for 30 days. Elapsed staging can be
reported as an expired read observation before cleanup commits a terminal event;
reading it does not create such an event or renew its deadline. A retained verdict
can remain available through its own endpoint after staging removal.

## Operational and verification boundaries

Reads remain available while mutation admission drains, subject to storage and
current authorization. They do not decrypt collection inputs, run checks, call
providers or replay unknown outcomes. List/detail reads have a ten-second budget
and a shared eight-read capacity. Slow history reads run outside authentication
and shared cursor locks, followed by fresh authorization and publication checks.
Liveness alone does not imply that retained history is available.

Frontend qualification passed 387 tests, type checks, lint, contract consistency
and a production build, with independent source review. The corrected full
web-server race suite and final affected durable/minimum-Go/tagged read-contract
checks passed. The real TLS/Raft browser/restart campaign passed release-Go race
and Go 1.25, preserving the first original validation seal after draft disposal
and owner restart, with no additional writes or provider calls.
See the [implementation and qualification notes](implementation/collection-operation-listing.md)
for source boundaries and private evidence locations.

Collection preparation, encrypted upload, cancellation and asynchronous validation
have their own implemented routes. This read-path work does not finish collection
activation, browser refresh/reselection recovery or the CLI collection application
workflow. It also makes no production-account, million-monitor endurance or full
shipping claim.
