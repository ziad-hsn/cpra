# Bounded validation of encrypted staged collections

Status: internal storage reads, scoped resource lookup, encrypted input
verification and bounded whole-graph validation implemented on the private
branch. This document also describes the remaining work over the committed
collection ledger. It does not claim that public collection activation,
resumable validation, or large collection application is available. Inactive
cancellation is now covered by [the admission boundary](collection-admission.md).
The [durable activation boundary](collection-activation.md) records the required
plan/outcome persistence and reverse-guard compatibility constraints.

The existing request-local preflight remains useful and retains its current
limits. The staged path must validate collections larger than its 32 MiB retained
plaintext allowance without increasing that allowance or retaining a decoded
resource slice proportional to the upload.

## Existing reusable boundaries

- `internal/persistence/collection_ledger.go` already supplies ciphertext pages
  bounded by count and 4 MiB, ordinal lookup, and an identity-to-ordinal index.
  The durable Store additionally supplies `CollectionValidationView` for complete
  uploads, with bounded Page/Item/Find operations and a final Check fence. It
  detaches ciphertext before decoding and never renews inactivity on reads.
- `internal/persistence/catalog.go` supplies `CatalogView`, an immutable copy-on-write
  live generation with point lookup. Its current reverse-dependent query is a
  separate live, bounded query, not part of that immutable view.
- `internal/management/collection_validation.go` implements input authorization,
  identity/conflict checks, omitted credential preservation, desired comparison,
  source attribution, dependency order, and original-version guards.
- `internal/management/collection_validation_graph.go` implements final-union and
  ordered-prefix validation, including affected consumers outside the input.
- `internal/management/staging.go` demonstrates encrypted iteration and
  per-closure validation. It is bootstrap staging: its create-only metadata,
  generated identities, separate authority, and retained 32 MiB closure make it
  unsuitable as the collection implementation without this refactor.

The present memory bottlenecks are the `desired` and `live` resource maps, cloned
prefix/subset maps, and full typed notification catalog. Input streaming alone
does not remove them.

## Internal interfaces

The implemented internal source uses the following ownership boundary. These
are not exported SDK or HTTP APIs.

```go
type stagedItemRef struct {
    Ordinal uint64
    Key durable.CatalogKey
    Source string
    Document, Item uint64
}

type resourceLookup interface {
    // A returned resource is borrowed only for the callback's duration.
    // Implementations authorize the identity before existence or decryption.
    withResource(context.Context, durable.CatalogKey,
        func(*api.Resource) error) (bool, error)
}

// collectionValidationSource also provides a complete inventory walk:
// walk(ctx, func(stagedItemRef, *api.Resource) error) error
// A late error invalidates earlier observations; it never authorizes activation.
```

The source binds the operation handle, restore epoch, upload identity, original
count and digest, and completed uploaded prefix. Every read remains fenced
against expired, invalidated, or otherwise non-executable state. Verification of
all original item MACs and the final inventory identity completes before any
activation. Source paths remain client-local; only existing opaque source
coordinates enter this layer.

Store adapters copy a bounded ciphertext page or one ciphertext record while
holding their existing locks, then release those locks before unwrapping keys,
decoding, or running validation. Do not hold an FSM lock or bbolt transaction
across key-service calls. A long-lived frozen ledger transaction is not required
merely to provide lookup: completed input is immutable and operation fences can
reject concurrent expiration. The implementation must not silently extend the
agreed inactivity lifetime through reads.

Map adapters keep existing graph and runtime callers compatible. The staged
adapter decrypts on demand using the existing binding, strict schema validation,
and authenticated reference-index checks. A callback must not retain its borrowed
resource. Clear owned byte buffers on return and drop references to decoded
strings; Go does not promise erasure of all temporary string copies.

## Separate graph metadata from plaintext

Retain only the information needed to validate and order the graph:

- Key, original input ordinal, opaque source coordinates, and source attribution.
- Old and desired direct reference lists.
- Observed UID, resource version, generation, and reverse-dependent version.
- Create/update/unchanged classification, absent-create guards, and dependency
  state/order.

Do not retain resource specs, credential values, resolved driver configuration,
provider errors, or plaintext fingerprints in graph records. The existing
encrypted original input remains authoritative; do not replace its payload with
normalized or credential-filled preparation. An omitted Credential value is
resolved from the original captured live version while validating or preparing
that resource.

Use separate enforced budgets for ciphertext pages, active plaintext and decoded
copies, graph nodes/edges/bytes, expanded routing, and total validation work.
Credential substitution can expand a small reference-bearing driver into a large
resolved configuration, so charge those output buffers too. Count and byte limits
must be checked before allocations that can exceed them.

The implemented source reserves **64 MiB for simultaneous borrowed input and
validation scratch**. This permits an approved 1 MiB input resource alongside
conservative encoding/decoding allowances. Sequential resources release their
reservations, so aggregate upload bytes do not consume this allowance forever.
It is a logical allocation budget, not a bound on total Go heap or process RSS.
The existing ephemeral preflight's separate 32 MiB retained-input limit remains
unchanged. Initial source tests walk more than 64 MiB of encrypted input, with
peak raw/decode reservations below 4 MiB; notification lookup tests independently
validate more than 64 MiB of endpoint specs with peak combined reservations below
20 MiB. These component results do not yet qualify complete staged activation.

Start without a plaintext cache. If measurements justify one, bound it by bytes,
scope it to one validation run, and key each entry by its origin and immutable
identity: staged ordinal/content identity or live UID/resource version. Prefix
views must never return a cached desired version where the old version is still
selected. Never spill decrypted resources to disk.

For collections exceeding the first slice's metadata budget, use a derived disk
index for keys, edges, traversal state, and item outcomes. Keep it reconstructible
from the original ciphertext ledger and recorded guards. It cannot become an
independent source of truth after a crash. Large outcome sets need bounded pages;
the current `CollectionValidation` result slices are not the final large-fleet
interface.

## Refactor validation without changing semantics

1. Split `resolveDriver` in `graph.go` into a lookup-based internal implementation
   and retain the map wrapper. Fetch credential resources only while constructing
   one bounded resolved driver; preserve string-versus-structured field handling.
2. Add lookup-based internal notification resolution in `routing.go`, retaining
   existing map wrappers. Validate individual shared definitions, then monitors.
   Preserve destination ordering, filtering by `notifyType`, duplicate rejection,
   empty-group rejection, and validation of unreferenced definitions. A pure
   validation sink does not need to retain all resolved delivery targets.
3. Split `validateSet` into per-resource validation plus iteration over metadata
   keys. Validate all desired resources and all affected old consumers against a
   selected lookup view. Driver validation must remain free of job construction,
   provider transport calls, or external operations.
4. Extract the current input normalization/conflict logic so request-local input
   and staged input use the same rules. Compare desired and old resources while
   those two values are borrowed; keep only comparison results and guards.
5. Reuse deterministic dependency ordering and source-attributed safe issues.
   Context cancellation and work limits must remain visible within reference and
   notification expansion, not only between resources.

## Final union and ordered prefixes

The final view selects staged data for every included key and captured live data
for authorized omitted keys. Validate that entire union before activation.

The prefix view starts with captured live resources. For each dependency-ordered
item, replace its graph edges and select its desired resource. Included consumers
whose turn has not arrived still select their old versions. Traverse affected
reverse consumers and validate them with the current prefix view. Metadata alone
selects old versus desired data; a plaintext state map is unnecessary.

Both old and desired dependency closures matter. A final valid union can still
have an unsafe intermediate prefix, for example when changing a shared endpoint
before a later monitor changes its selected notification type. The existing
`unsafePrefix` behavior must remain. Unchanged items retain their existing
semantics and do not create fictitious mutations.

## Reverse dependents and conditional activation

`CatalogDependents` currently reads a live map, sorts the complete bounded result,
and returns a `DependentsVersion`. The existing comparison against the captured
record is sound within its 10,000-dependent bound. Do not reinterpret a truncated
result as a complete dependency set.

For larger operations, introduce an ordered paginated reverse index. Either
capture it with the catalog generation or require the same guarded dependent
version on every page, rejecting changes. Do not scan the entire fleet or
repeatedly skip prefixes of an unordered map to construct pages. Authorization
must precede opening each discovered dependent resource.

Before reporting successful validation, recheck original observed resource and
reverse-dependent versions and create-absent conditions. These observations do
not lock configuration. Later activation still uses per-resource CAS.

Activation may substitute only this operation's own confirmed predecessor
receipts for their originally observed versions. Preserve all outside resource
and reverse-edge guards, including changes introduced by the operation's own
confirmed edges. If an included dependency fails, its consumers become blocked;
they must not fall back to an older live dependency. Calling the existing
`Catalog.Prepare` against a fresh snapshot per item without carrying these guards
would silently rebase the operation and is insufficient.

## First implementation slice and acceptance

The first slice supplies lookup-based validation, adapters, and a staged validator
with a **10,000 retained-version bound**, **32 MiB metadata allowance**, and
**100,000 traversal visits**. Old and desired versions count separately: this
does not qualify 10,000 arbitrary updates merely because browser parsing permits
10,000 input resources. Metadata/work exhaustion returns an explicit limit error.
It supports large total input bytes without claiming arbitrary cardinality,
durable activation, or resumable validation. Keep the ephemeral endpoint's
protocol and request-local validation limits intact.

The private implementation is in `staged_validation.go` and
`staged_validation_graph.go`. It retains identities, references, versions and
guards, and borrows plaintext only within scoped callbacks. Normalization releases
the old resource and its scratch before nested driver/routing validation. Final
catalog conditions and the source expiry/restore fence are rechecked before a
successful observation; later activation still needs its own atomic guards.

Focused checks cover request-local parity, omitted values, unsafe intermediate
prefixes, outside edits and reverse edges, denied identities, malformed final
input, cancellation, expiry, graph/work quotas and zero active/provider effects.
A 72-resource inventory exceeding 64 MiB retained about 19.5 MB of accounted
borrow/scratch at its peak. Near-1 MiB updates remained below 54.5 MB, and an
actual encrypted notification closure exceeding 32 MiB remained below 31.4 MB.
These are explicit accounting measurements, not heap or RSS. Go 1.25, tagged
Go 1.27.1 and vet checks pass with independent review. The full default management race suite also passed in 854.713 seconds,
including the large encrypted closure tests and source snapshot/log restart. See the private evidence at
`bin/verification/staged-collection-validation/result.json`.

Acceptance:

- More than 32 MiB and at least 64 MiB of valid staged resources validate without
  retaining their combined plaintext. Assert an explicit active plaintext budget
  in the resolver, including decoded/resolved copies; measure representative heap
  behavior separately rather than treating a logical counter as total RSS.
- A supported notification dependency closure larger than 32 MiB validates by
  lookup without materializing that closure. Oversized individual resources or
  configured graph/work-budget violations fail explicitly.
- Existing final-union, unsafe-prefix, outside-dependent, omitted-credential,
  conflict, duplicate, and capability cases produce equivalent safe outcomes.
- Denied identities are rejected before existence checks or decryption, including
  absent resources and outside consumers.
- A malformed final item causes zero active catalog changes and zero provider
  operations. Validation does not alter original staged bytes or their MACs.
- Concurrent outside edits, reverse-edge changes, operation expiration, restore,
  cancellation, corruption, and limit exhaustion cannot produce a successful
  stale validation. Buffers and scratch resources are released on every exit.
- No test silently raises the existing limit, skips a remaining page, stores
  plaintext on disk, or treats a partial graph as successfully validated.

Subsequent slices add disk-backed graph metadata, paginated reverse lookup and
outcomes, and restartable validation progress before claiming SDK-scale
cardinality. Activation and cancellation remain their own durable protocol work.
