# Collection operations in the ordinary operation list

**Status: implemented and qualified for the private operation-browsing slice.**
`GET /api/v2/operations` has a unified ordinary/collection observation path,
actor-aware collection detail reads, and dashboard browsing of original retained
validation results. The [progress record](dashboard-implementation-progress.md)
records completed scoped race, minimum-Go, HTTP and connected browser/restart
checks with source boundaries. This is not complete collection activation or
shipping qualification. Candidates remain private.

This extends the [approved management API plan](api-management-plan.md) and the
implemented [validation coordinator and result reader](collection-validation-coordinator.md).
It adds no collection activation, browser refresh/reselection recovery, or CLI
application workflow. Provider-account and endurance gates remain separate.

## Implementation seams

| Layer | Implementation and boundary |
| --- | --- |
| Durable logical snapshot | [`ManagementOperationSnapshot`](../../internal/persistence/management_operation_view.go) captures bounded shared ordinary receipts and owned collection metadata at the same Store/FSM/history boundary. It uses the existing operation epoch, allocation high-water mark, history watermark, retention/index generations and segment set. |
| Terminal collection iteration | [`management_operation_history.go`](../../internal/persistence/management_operation_history.go) merges ordered primary evidence and verifies the receipt indexes. It reads metadata only. |
| Collection detail | [`CollectionOperationAs`](../../internal/persistence/collection_operation_access.go) checks the original actor and reads detached active or retained terminal metadata. It does not decrypt inputs or load validation-result rows. |
| Management projection | [`ManagementOperationView`](../../internal/management/management_operation_observations.go) chooses the existing ordinary or collection DTO projection. [`OperationAs`](../../internal/management/operation_access.go) preserves shared ordinary reads while restricting collection detail. |
| HTTP | [`management_operations.go`](../../internal/httpserver/management_operations.go) enforces current access, signed cursors, response limits, detached-read capacity and retained snapshot quotas. |
| Dashboard | [`OperationsList`](../../dashboard/src/components/OperationsList.tsx) distinguishes collections; [`OperationDetail`](../../dashboard/src/pages/OperationDetail.tsx) exposes the separate, explicit [`CollectionValidation`](../../dashboard/src/components/CollectionValidation.tsx) reader. |

No new Raft snapshot format or persisted list snapshot was introduced. These are
short-lived read views, not a durable execution plan or an authorization grant.

## Visibility and permissions

Ordinary resource/control operation receipts keep their existing shared read
visibility. Collection observations belong to their immutable original actor.
The list first requires `ListOperations`; detail requires `GetOperation`. Including
or reading a collection additionally requires the current supported-resource read
floor: `Get` permission for every kind returned by `management.ResourceKinds()`.
A caller without that floor can still read ordinary receipts; its list excludes
collections. A foreign collection handle returns not found rather than revealing
its status. Reading retained facts does not require the authorization revision
that originally admitted validation.

Every request and continuation rechecks current authority. Cursors bind the
principal and authorization generation, so token/policy replacement can invalidate
an old query. A permitted new token for the same permanent actor can start a new
query. The dashboard checks the exact `GetOperationValidation` permission before
offering a retained-result read; it does not infer permission from the actor's
role or the operation phase. Server authorization remains authoritative.

## One snapshot and explicit ordering

The view owns detached, allowlisted receipt metadata from the bounded live set,
including at most 64 collection headers. It does not own encrypted input,
provider values, ledger rows, file paths, result-row arrays or executable state.
Snapshot allocation is charged before copying, including deduplication keys.

Pagination traverses these phases in order, continuing across a phase boundary
when the page and work budget permit:

1. Ordinary live receipts, ordered by operation handle.
2. Ordinary retained terminal receipts, ordered by committed event position.
3. Frozen owned collection headers, ordered by operation handle.
4. Retained owned collection terminal receipts, ordered by operation handle.

This is not a globally chronological feed. Later upload, validation, cancellation
or cleanup does not rewrite the phase/progress already captured by a cursor.
Live IDs copied into the view suppress a later terminal copy of the same
operation. New allocations and history above the captured watermarks are
excluded. Retention/index changes and restore invalidate the cursor explicitly.

The exact `monitorID` filter preserves single-monitor operation semantics and
excludes collection-wide receipts. There is no retained collection-membership
index that could truthfully answer which historical collections mentioned that
monitor after their input is removed. No membership is inferred from names,
current manifests or dependency relationships.

## Bounded history work and corruption handling

Disk iteration merges at most 64 segment heads. Within each segment it compares
the primary `events` prefix `collection/op.<epoch>.` with the
`collection_receipts` point index, checks their identities and bytes, and rejects
duplicate or contradictory terminal evidence. Missing primary or derived rows,
unreadable segments and incomplete legacy audit-only evidence are explicit
availability failures. Old audit records are not promoted into complete receipts
by inventing missing inventory metadata.

Memory mode maintains an ordered tree over primary collection evidence, with
matching receipt-index checks. It does not sort or clone the entire retained
collection map for each page. The shared per-page work budget is 10,000
conservatively charged source/index inspections across the ordinary and collection
phases. Collection history also bounds retained metadata bytes per page. A long
foreign-actor prefix can exhaust that budget without producing an owned row:
such an empty page carries an advancing cursor and is not the end of the list.

The API defaults to 100 receipts and allows at most 500. Serialized pages have an
8 MiB ceiling; an oversized page fails rather than losing its continuation through
truncation. Listing never decrypts input, consults a provider, scans monitor state,
allocates an operation or renews an upload.

## Original seals, expiry and truthful status

Inactive staging has a 24-hour inactivity deadline. A finalized validation result
has a separate 30-day retention deadline. When staging time elapses, the read
projection can report `expired` while preserving the original identity and
allowing still-retained validation evidence to be read separately. That is an
observation of elapsed time, not a newly committed expiry event or renewed lease.
Committed cancellation and interruption keep their own recorded dispositions.

Only a `validated` or `rejected` observation with the original sealed result can
set `validated: true` or `validated: false`. Pending and interrupted observations
omit that field. An expected but missing seal is unavailable, not a fabricated
rejection. The summary check uses the original result expectation and watermark
without reading its item rows. Expired/canceled operation progress and an original
retained verdict remain separate observations; a successful verdict does not
establish activation.

The result endpoint is `GET /api/v2/operations/{id}/validation`. The dashboard
fetches it only after an explicit request, keeps one page of at most 100 rows,
and pins the original inventory and sealed summary across next-page and refresh
reads. It shows opaque source tokens, document numbers and item positions; it
cannot reconstruct discarded local filenames. Unknown identity formats remain
bounded read-only metadata without an unsupported result request. Authorization
failures clear visible rows; sign-out, navigation or stopping a read aborts the
outstanding GET. No read retries validation, activates configuration or cancels
the operation.

## HTTP locks, deadlines and quotas

List and detail reads have a ten-second request budget and share a capacity of
eight detached reads. They capture authorization briefly, release the policy
lock, perform bounded snapshot/history work, then recheck current authority
before publishing. Page reads do not hold the shared HTTP cursor lock across
history access. Waiting for the relevant locks respects cancellation.

List cursors last five minutes and bind principal, authorization generation,
page size, operation-list kind and exact monitor filter. Snapshot counts share
the management limits of 64 globally and 16 per principal. Ordinary and collection
snapshot copies share a 64 MiB global and 16 MiB per-principal byte budget.
In-flight capture reservations count before allocation and are released on
failure; publication rechecks current quotas. An existing cursor continues its
original view rather than constructing another copy.

Snapshot capture holds the ordered Store/FSM/history read locks only for bounded
metadata capture. Subsequent page history access uses the detached view and its
captured fences. Collection detail releases Store/FSM locks before acquiring
history locks, then verifies owner/store epoch and health again. These boundaries
avoid holding authentication or shared cursor locks across slow disk access;
they do not remove all synchronization from the durable store.

## Qualification status

The frontend source review passed independently, and its full suite passed 387
tests across 26 files, generated-contract consistency, TypeScript, lint and the
Vite build. Private evidence is in
`bin/verification/dashboard-operation-browsing/result.json`. Those tests use the
real dashboard session transport with fixture responses; they are not a substitute
for the connected server/browser campaign.

Durable and HTTP checks passed for mixed visibility, frozen phase transitions,
cleanup, bounded foreign-history scanning, corruption, exact expiry, quota
reservation and cancellation/authority changes during reads. Cross-phase page
filling was corrected during this qualification. The connected regression extends
the real TLS/Raft main process, embedded Worker/WASM import and SDK restart read
through the retained list and operation detail. That campaign passed release-Go
race and Go 1.25 with the original seal preserved, zero additional browsing writes
and zero provider calls. The complete corrected web-server race suite passed;
final affected durable, minimum-Go, tagged read-contract and vet checks passed.
The broader durable race run predates a final scalar work-budget correction,
which has separate final focused evidence. No release-wide claim is made here.

The [progress record](dashboard-implementation-progress.md) preserves exact
durations, the initial failures, independent reviews and the still-separate broad
management run. Local reports are under
`bin/verification/management-operation-view`, `operation-access`,
`management-operation-integration` and `collection-operation-browser`.

Collection activation, browser reselection after refresh, CLI collection
application, production-account verification and the million-monitor 24-hour
campaign remain outside this implemented read path and unfinished release gates.
