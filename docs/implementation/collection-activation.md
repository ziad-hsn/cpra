# Durable collection activation boundary

Status: the original-plan execution contract below is implemented in the private
candidate, including conditional item commands, background execution, retained
results and source cleanup. The [public activation integration](collection-public-activation.md)
now connects the existing route and SDK/CLI/dashboard consumers. Cross-process
reselection and the full shipping gates remain open. A finalized validation plan
alone is not an activation grant. The approved management API plan remains the
authority for behavior.

## Preserve the reviewed input and resource versions

The existing staged validator authenticates every encrypted original item,
validates the final resource union and each dependency-ordered prefix, and
returns original live version guards. Activation must preserve those decisions.
Ordinary `Catalog.Prepare` observes fresh state and generates fresh identities;
calling it again after restart is not a valid interpretation of a frozen plan.

A private plan row records the original input/source ordinal, resource identity,
create/update/unchanged classification, original target condition, the exact
resource footprint used to validate its prefix, earlier included dependencies,
and the old/new references changed by this row. Resource specs, credentials,
provider parameters, keys and source filenames do not belong in plan metadata.
The original encrypted item ledger remains the source of resource bytes.

The implemented [private plan artifact](collection-plan-artifact.md) can stream
that metadata in bounded fragments. Private durable staging commits the complete
intended descriptor before its first fragment and reconciles only exact retries.
The codec itself grants no authority. Separate durable plan/result staging now
finalizes exact descriptors, and retained reads expose the original sealed verdict.
Partial artifacts remain provisional; neither a structural plan nor a successful
validation summary executes its resources.

`Requires` is satisfied by the earlier item's successful original **conditional
catalog acceptance** in this same activation: an accepted create/update or a
conditionally verified unchanged result. Consumers need not wait for controller
application. Unchanged input still requires its own recorded conditional result;
the existence of matching older live bytes is insufficient. A failed conditional
dependency produces `dependencyBlocked`, never fallback to its older live version.
Independent rows can continue after an unrelated conflict; a global catalog
generation fence is not the per-item execution contract.

This follows the existing separation between desired catalog state and observed
application: the [compiler](../../internal/management/collection_plan.go) records
`Requires` and `Touches` from catalog observations, while the
[owner](../../internal/controller/systems/catalog_runtime.go) reconciles committed
resources and reports their exact-version application separately. Controller
pending, applied, failed, or superseded outcomes remain separately visible. None
authorizes refreshing a CAS guard, repeating an accepted mutation, rolling it
back, or substituting a different resource version. An outside edit still fails
the later item's original-plus-own guards.

Current create semantics remain create-if-absent at commit time. They do not
promise that an absent identity never had an intervening create/delete cycle.
Updates retain exact incarnation and resource-version preconditions.

## Reverse-dependent guards have a versioned prerequisite

Historical format-2/3 `CatalogRecord.DependentsVersion` values are Raft log
indices. They suffice for request-local observation/commit boundaries, whose
reads cannot observe a partially applied log entry, but cannot identify a future
collection executor's own mutations:

1. Collection item A changes a consumer of shared dependency D.
2. An outside mutation B changes another consumer of D in the same batched log
   entry. Both mutations can succeed with D's unchanged UID/resource version.
3. Both set D's reverse version to the same log index. Substituting A's commit
   index for the original reverse guard would also accept B's outside change.

The executor must receive an unambiguous reverse-change identity from each
atomic own mutation, or prove the exact original-plus-own reverse membership
and versions. `Touches` and a commit index alone do not establish that proof.
Do not refresh a reverse guard from current state to make a conflict disappear.

The implemented [format-4 prerequisite](catalog-mutation-format.md) preserves
those old replay semantics and assigns each accepted new catalog mutation its
own `CatalogMutationSequence`. A target touched by two mutations in one entry
therefore receives two distinct tokens. The first token exceeds the preceding
image index; subsequent tokens increment only after the mutation's conditions
succeed. `CommittedIndex` keeps its original meaning. Downgrade commands,
conflicts and overflow cannot change the catalog or consume a token.

This gives the future executor an atomic own-mutation identity. It does not
implement substitution, child outcomes or activation. The
executor must still prove that each substituted token came from the exact
successful prior item and reject any intervening outside mutation.

The next atomic command must also check the token **before** overwriting it.
For a general plan, an outside edit could touch D, then an own item could drop
its old reference to D and overwrite that token. A later guard that blindly
substitutes the own item's token would conceal the intervening outside edit.
The current compiler explicitly orders resource kinds from credentials through
monitors; a group dropping a recipient does not move before that recipient.
That example is a general execution-contract hazard, not a demonstrated ordering
bug in the current compiler.

Use a bounded index derived from the complete sealed plan: collect the original
resource tuple and reverse token for every target/guard with a nonnil reverse
condition, together with the last ordinal needing that condition. Reject
contradictory original observations. Before a mutation touches a key with an
outstanding reverse condition, compare its original-plus-successful-own tuple
and token atomically. Verify the plan's `Touches` equals the actual old/new
reference union. Only successful checked mutations advance this derived chain;
failed and unchanged outcomes do not. The original plan plus compact accepted
outcomes can then reconstruct it without another large stored proof artifact.

Resource version substitution and reverse-token substitution remain separate.
Changing D's own configuration preserves its incoming-edge token; its mutation
sequence stamps D's referenced targets. Neither a fresh catalog read nor a
client-supplied assertion can certify these conditions. The index is disposable
derived metadata, never independent execution authority.

For a general plan, a later reverse-guarded prefix can also assume an earlier
own removal that failed. The unchanged original token would then match while the
old consumer still exists. Require the successful conditional acceptance of each
earlier planned touch whose effect that guarded prefix assumes; derive these
predecessors from the original sealed metadata. A failed predecessor yields
`dependencyBlocked`, rather than a fallback to an older own token or current
catalog. This is an execution-contract requirement, not evidence of a current
layered-compiler ordering defect.

## Commit plan, mutation and outcome consistently

The [format-5 staging prerequisite](collection-plan-staging.md) now retains
bounded inactive plan fragments separately from the encrypted input rows.
The [format-6 result prerequisite](collection-validation-staging.md) retains
separate immutable validation-result rows, exact owner observations and original
plan binding. Finalized results now publish to retained history before staging
cleanup. Public validation coordination and original bounded result endpoints
are now integrated. A scoped connected release-Go TLS/browser/restart campaign
passes on release Go and Go 1.25 with the original sealed result and zero
activation. Later checkpoints implemented activation item-outcome rows and the
execution coordinator; their public integration has a separate connected campaign.
Their database is a materialization reconstructed from a self-contained snapshot and committed logs. Neither its selected local
generation nor retained event history is an execution authority; either can be
ahead of the image being replayed.

The command sequence is bounded plan staging, immutable plan finalization,
explicit activation, conditional item application and controller-result
observation. Finalization binds the original operation/upload identity, complete
input digest and count, plan identity/content, validator capability profile and
current authorization. An incomplete, expired or invalidated plan authorizes no
catalog write; a stale item guard prevents that item and blocks its dependents.
Authorization and shutdown admission must still be checked at actual writes;
the private compiler does not provide an authorization grant.

Internal `plan_finalize` structurally verifies the original complete artifact and
input fence; requested operations also preserve their claim/authority fences. It
does not publish a completed verdict. Separate validation finalization binds the
original plan, authority and capability profile, and bounded history publication
seals the publicly readable result. That original result remains inactive and
provides no grant for future catalog changes.

An operation has exactly one immutable finalized validation outcome. Repeated
Validate requests reconcile that original success or failure; a new validation
intent requires a new operation. With no replacement plan under an existing
handle, the current path-only Activate request is unambiguous and needs no new
generation parameter. If replacement is introduced later, it requires a separate
reviewed precondition contract.

Background item execution must authorize the original principal against current
durable authentication state. A process-local policy generation or a retained
bearer token is not a durable execution grant. Unrelated incident attention and
snooze controls remain separate from catalog version guards and are preserved.

The background authority check must derive its actor from the activated
operation and compare the committed authentication epoch and revision again
inside the item command. It must reject reset, anonymous, legacy, absent,
revoked, expired or non-operator authority. Observation time is supplied by the
caller after preparation and checked at admission; replay must not consult a
wall clock. A policy change invalidates an in-flight revision fence, even when
the original actor remains authorized after a fresh observation.

The versioned durable [principal lifecycle](authentication-lifecycle.md) now
enforces permanent IDs and immutable revocation tombstones within an epoch.
Current upload creation captures that ownership boundary; historical ownerless
uploads remain ineligible for background validation. The executor must preserve
the same owner and recheck current authority at each actual mutation. No token
hash supplies actor identity or a retained execution credential.

Each item command atomically changes the catalog, records its original child
operation identity/outcome, and advances parent progress. A separate later
"mark successful" command creates a crash gap. UUIDs, encrypted payloads and
observation times are generated before submission, never during replay.
Uncertain replies reconcile the same item and child identity. A retry cannot
silently allocate a replacement resource version.

Parent collection IDs and ordinary child operation IDs remain distinct. Current
snapshot validation forbids their namespaces from overlapping. The existing
4,096 pending ordinary-operation limit requires bounded child admission and
backpressure while the controller reconciles committed resources. Configuration
committed and controller applied remain separate measurements. Parent progress
must retain pending and completed child evidence independently of ordinary
history expiry; a later controller outcome must update the original item rather
than change whether its catalog mutation was accepted.

Before any item acceptance is enabled, the implementation needs terminal-child
hooks at catalog supersession, controller success/failure and explicit restore.
Persist the original child disposition before removing its pending relationship
or ordinary receipt. A new collection mutation that supersedes an older linked
child must commit that observation in the same ledger transaction as its own
acceptance and prepared-slot consumption. Reserve completion capacity when the
child is accepted; later quota pressure must not discard a known outcome. Keep
pending relationships bounded by the existing operation limit. Do not add
fallible I/O to the current blind receipt-deletion helper.

The implementation order is a pure catalog-delta preparation step, a no-I/O
installer, and a bounded index of the original sealed plan; then prepared work,
acceptance, child observations and their complete snapshot/restore/cleanup
handling together. All fallible ledger work precedes installation under the same
FSM lock, followed by the existing history barrier. A write failure makes the
machine unavailable before publishing a usable projection. Recovery reconstructs
from the snapshot and committed logs, never from a materialization that happens
to contain later rows.

Snapshots must freeze the catalog, input, plan, outcomes and progress at one
index, and validate every reference/count/byte boundary before installing the
restored generation. Missing outcome data is corruption, not an empty set of
unattempted items. The format change must update online replay, stopped backup
validation, restore and older-reader rejection together; changing only the
`LatestFormatVersion` alias would break recognition of existing format 3.

## Next implementation slices

These separate completed admission work from the remaining execution
prerequisites. No new storage-format number is assigned by this document.

1. **Private activation foundation.** The
   [format-8 admission boundary](collection-activation-admission.md) now persists
   one immutable activation identity bound to the original operation/upload,
   input commitment, sealed successful validation result, finalized plan
   descriptor, owner and capability profile. It admits zero item mutations.
   The next step is bounded parent-linked prepared-item identities and outcome storage, with
   complete snapshot, replay, offline validation and cleanup fences. Exact retries
   reconcile those identities. A refreshed authorization observation may permit
   the same permanent owner after token rotation; it cannot replace the plan.
2. **Atomic conditional item command.** Resolve `Requires`, `FromOrdinal` and
   reverse-token substitution from original successful parent outcomes. Recheck
   authority and all target/dependency guards, then commit catalog mutation,
   child identity/outcome and parent progress together. Record unchanged,
   conflict and dependency-blocked outcomes without fabricating resource writes.
3. **Protected executor.** Read original input/plan fragments through bounded,
   authority-fenced views; retain prepared identities across uncertain replies;
   resume the original activation after ordinary restart. Integrate bounded
   child admission, shutdown, cancellation and controller-result reconciliation.
   Do not call ordinary `Catalog.Prepare` to derive fresh replacement guards.
4. **Public integration and qualification.** Preserve path-only Activate and
   original-owner authorization. Add bounded item-result projection/pagination,
   integrate API/SDK/dashboard progress, and register discovery only after the
   storage/execution boundaries and crash tests pass. The existing path remains
   unambiguous because its operation has one immutable result and plan.

Two concrete constraints apply before the atomic command and public integration:

- A logical plan row may exceed the **4 MiB commit limit**, although its typed
  fragments are individually bounded. Do not serialize all its guards into one
  `CatalogMutation.Conditions` command or expand the whole plan into JSON. Bind
  preparation to the original committed row/descriptor and use bounded verified
  fragment/index handling. Final conditional acceptance must check the complete
  guard set atomically with the mutation; checking fragments in separate commits
  would allow intervening edits.
- Public `ApplyResult.ID` alone cannot distinguish identical IDs in different
  resource kinds. Add resource `kind`, original input `ordinal`, opaque source
  token/document/item coordinates, and an optional distinct child-operation
  handle. Keep the resource ID and child handle separate; unchanged or blocked
  items must not invent a child mutation. Preserve old/new versions and separate
  catalog-committed versus controller-applied availability and outcomes.

## Cancellation, expiry and recovery

Cancellation stops remaining items and preserves previous commits, unknown
outcomes and pending controller results. No rollback is implied. The inactive
24-hour timeout must not expire an actively applying operation. Authoritative
completed child outcomes remain available until the parent is terminal, even
when a long-running parent outlives an ordinary child's history retention.
Continuation never depends on retained history. Retained terminal item results
follow the agreed 30-day history contract.

Cleanup must include plan identity/phase and outcome progress in its fences,
preserve unresolved children, and reclaim rows in bounded steps. Explicit
restore invalidates every executable old-epoch phase while preserving already
committed catalog resources; ordinary owner restart resumes only the original
recorded plan.

Qualification must include lost item replies, snapshot-plus-log recovery
between two rows, failed dependencies, unchanged-item conflicts, outside edits
within a batched log entry, cancellation after partial commit, controller
completion/supersession, missing outcome streams, partial cleanup restart and
explicit restore. Validate, retained-result reads and Activate are now registered.
The [public integration record](collection-public-activation.md) distinguishes
connected activation verification from the earlier validation-only campaign.
