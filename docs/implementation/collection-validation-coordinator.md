# Public collection validation coordinator

Status: request/claim storage, one compiler worker, exclusive startup retirement,
main-process lifecycle ownership, public asynchronous Validate admission and
original retained-result reads are implemented in the private branch. SDK
qualification passes; the connected release-Go TLS/browser/restart scenario
passes on both release Go and minimum Go 1.25. Activate remains unfinished.
This document distinguishes those slices from the remaining interfaces; it makes
no release-readiness claim. All candidates remain private until every agreed gate passes.

## Implemented foundations and their boundaries

| Foundation | Actual implementation | Boundary |
| --- | --- | --- |
| Complete encrypted input and current ownership | `internal/persistence/collection_read_view.go`, `collection_owner.go`; management admission | The original protected input view accepts only `uploading`; a separate claimed-attempt view accepts `validating` only before either artifact begins. Both require the complete immutable prefix and fence expiry, restore and storage. Claimed reads also check the original claim and current committed authority. Current uploads retain a durable operator owner; historical ownerless uploads cannot gain validation authority. |
| Whole-union and safe-prefix validation | `internal/management/staged_validation*.go`, `collection_plan.go` | The compiler records original target/dependency/reverse guards and included predecessor requirements. It does not submit commands, seal provider resources or execute jobs. |
| Frozen artifacts | `collection_plan_artifact.go`, `collection_validation_artifact.go` | The complete intended descriptors are computed before staging. The result adapter retains deterministic outcomes and source positions, rejecting cancellation, authorization/storage failures and unknown errors as validation verdicts. |
| Original plan storage | `internal/persistence/collection_plan_apply.go`, `collection_plan_verification.go` | Begin/append/finalize preserve exact fragments and input identity. `plan_finalize` alone is structural state, not a publicly authorized validation success. |
| Immutable result storage | `collection_validation_apply.go` | Begin binds the original input, result descriptor, authority and capability digest; successful results also bind the finalized plan. Every append/finalize checks the captured authority revision. |
| Retained result publication | `collection_validation_publish.go`, `collection_validation_history*.go`, `collection_validation_read.go` | Bounded copying publishes one descriptor-checked summary; staging cleanup is separately fenced. Retained reads expose the original verdict, never authorize execution. |
| HTTP admission pattern | `internal/httpserver/management_collection_admission.go`, `management_collection_cancel.go` | Authentication precedes body handling. `CollectionCommit` rechecks admission at Submit without holding a grant across KMS work. These HTTP closures retain a request and must not become background-worker credentials. |

## Implemented private coordinator

A management-owned coordinator runs separately from the controller/ECS loop.
It selects metadata from the existing at-most-64 staging operations and owns at
most one compiler attempt. It owns frozen artifact memory, cancellation and
shutdown. No owner/FSM lock or HTTP policy lock is held while compiling,
decrypting, serializing or waiting.

Private entry points are implemented in `collection_validation_coordinator.go`
and `collection_validation_worker.go`:

- `RequestCollectionValidation(ctx, id, actor, now, admit)` authenticates the
  original owner, reconciles an existing outcome/attempt, and admits at most one
  job for that operation. It returns an operation observation, not a fabricated
  validation result.
- `runCollectionValidation(ctx, id, runID, now)` performs one claimed attempt.
  It retains no HTTP request, bearer token or process-local policy grant. Its
  source owns scoped plaintext only while compiling.
- `StartCollectionValidationCoordinator(ctx, now)` registers once per opened
  Store, retires abandoned eligible claims before launching its worker, and owns
  that worker until it joins. A second Catalog cannot repeat the startup sweep.

The public read handler in `management_collection_validation.go` now authorizes
before observing the original result and maps the protected durable retained
view into `api.ValidationResultPage`. It binds pagination to the original result,
a fixed watermark, the reader/policy context and page limit.

Storage format 7 now commits a request independently of either artifact
descriptor, followed by one exclusive claim before decryption. The protected
`CollectionValidationAttemptView` checks that original claim before every read.
The marker survives log/snapshot recovery and is selected by the private lifecycle
worker. Public HTTP background acceptance now returns the original operation
receipt; a separate read endpoint exposes only sealed retained results.

The private runner drives the following sequence with a supplied context and
fresh observation clock. Its transport-free admission checks storage readiness;
the lifecycle owner stops new validation admission and cancels/joins the runner
on shutdown. The HTTP contracts below now register validation admission and
retained reads; activation remains undiscovered:

1. Read the original receipt/header without decryption. Check actor, owner epoch,
   complete upload, expiry and current named-operator authority. Reconcile a
   finalized result first; never compile because its history is temporarily
   unavailable. Check submitted-kind write permissions and all consulted-resource
   read permissions before revealing existence or decrypting bytes.
2. Capture one catalog view; open the private source and invoke
   `compileStagedCollectionPlan` once. The current source has a 64 MiB simultaneous
   plaintext/scratch allowance; graph and plan metadata have separate bounds.
3. Prepare **both** immutable artifacts before beginning either artifact. The
   result adapter reopens the matching protected view; for requested input,
   `plan_begin` ends claimed compilation access. Generate
   PlanID/ResultID once, compute complete descriptors, then close/clear the
   source and key. Do not call ordinary `Catalog.Prepare` or create runtime jobs.
4. For success, commit `plan_begin`, stream exact bounded fragments with the
   original descriptor, call `VerifyCollectionPlan`, then commit `plan_finalize`.
   Use a bounded fragment visitor/codec bridge rather than accumulating another
   complete encoded plan. A failed or canceled stream remains provisional.
5. Commit `validation_begin` with the frozen result descriptor, original input
   identity, capability profile and operator authority. A success identifies the
   exact finalized plan; a deterministic rejection has no successful plan.
   Stream original-order result batches through the existing artifact emitter,
   then commit `validation_finalize` only at exact descriptor equality.
6. Let bounded publication produce the retained summary seal. Return a completed
   verdict only through the retained read path. A finalized-but-unsealed result
   is pending; missing history is unavailable; a structural plan is not success.

Current private writes check caller cancellation and storage readiness, with
explicit observation times. Lifecycle shutdown closes validation admission before
joining the worker. Foreground HTTP Submit calls reuse `CollectionCommit`.
Background work derives the actor from committed ownership, observes current
durable policy and carries the original authority fence into Apply. It retains
no `admit` closure or bearer token from the HTTP request.
For format-7 requested operations, every plan/result command now carries the
original request, claim and run identities. Apply checks that fence and the exact
authority revision at a fresh observation time, including finalization retries.
Historical unrequested commands retain their earlier replay semantics.

## Original-plan and restart barriers

Format 7 implements one immutable validation request per original operation, an
exclusive compilation claim, and an audited interruption disposition. The request
binds original input identity, owner authority, capability profile and observation
time. The private runner commits the claim before catalog reads or decryption. Requested operations need the
same request/claim fence on every plan/result command while historical replay
keeps its existing semantics. A request never claimed may begin its first
compilation after restart; acceptance does not promise the catalog at admission
time. A claimed attempt that lost its original frozen bytes must be interrupted,
not recompiled. Recover abandoned claims only during exclusive-store startup,
before workers run; a competing live coordinator must not cancel the winner.

Current operator-revision validation alone is not a resource-permission proof.
Background work must derive supported submitted-write and consulted-read
permissions from committed policy, check access before revealing existence or
decrypting, and sample time again immediately before Submit after blocked KMS
work. Policy/profile changes must stop the original attempt without turning
them into an invalid-resource verdict. An inert interruption receipt must remain
possible after execution authority is stale and must not overwrite cancellation
or an already finalized result.

| Committed state | Safe continuation |
| --- | --- |
| Complete input; request unclaimed | A first compilation may start after the original authority/profile is checked and an exclusive claim commits. Acceptance does not promise the catalog observation at request admission. |
| Claim committed; no plan/result descriptor | A live runner may continue its original attempt. After losing that runner, record an interruption; do not acquire a second claim or recompile under the same operation. |
| Partial plan, or finalized plan with no result intent | Never rerun the compiler to fill missing decisions. Continue only with the exact original frozen artifacts still owned by the live coordinator, or record an audited interruption. Original complete bytes can be structurally verified, but do not by themselves recover an unrecorded capability/result intent. |
| Partial result | The live owner may continue its exact original artifact under the captured authority. Startup retires an abandoned claim; it does not reconstruct a missing suffix from newer catalog state. |
| Complete result, not finalized | The original live owner may finalize under the captured authority. Startup conservatively interrupts the abandoned claim even when all rows exist; it does not silently refresh authority or synthesize continuation. |
| Finalized result, not published | Copy existing facts and publish the original seal. This is execution-inert maintenance; it does not need a new execution grant. |
| Sealed result | Return the original success or rejection. A repeated Validate request never replaces its plan, extends its deadline or runs providers. |

Before request admission, a legitimate credential rotation can obtain a fresh
revision for the same permanent principal and epoch. After request admission,
claim/read/artifact commands require that original exact revision. Revocation,
expiry, reset or a changed revision stops continuation; no result Begin refreshes
the admitted authority. Explicit restore changes
the operation epoch and invalidates old executable handles. Historical ownerless
uploads remain ineligible even if their textual actor now names an operator.

The approved behavior does not require successful validation after every crash.
A conservative audited `validationInterrupted` outcome under the **original**
operation is acceptable: zero activation, original evidence preserved, and a new
operation for new intent. The distinct `validation_interrupt` command now
compares the original claim and complete committed prefixes. It can retire an
unfinished attempt after authority changes, while preserving partial plan/result
bytes, cancellation and finalized outcomes. Its receipt survives bounded cleanup
and restart. Exclusive startup now retires eligible abandoned claims as
`coordinatorRestarted` without recompilation. An interrupted live attempt is
joined and its uncertain submissions flushed before retirement against the
current committed prefix. Expired or already terminal outcomes remain unchanged.

A durable spool containing the complete original artifacts is an alternative if
successful automatic continuation is required later. It is not necessary to
promise that stronger behavior in the first coordinator. No resume path may
refresh guards or use a new plan identity under the same operation.

## Implemented HTTP, pagination and cancellation contracts

`POST /api/v2/operations/{id}/validate` now returns only `202 Operation` on
successful admission or reconciliation. It commits the original request and
leaves compilation to the main-owned worker. The request contains no replacement
plan or validation generation; the operation has one immutable intent. A retry
can reconcile that same intent but cannot recompile a rejected/interrupted one.

`GET /api/v2/operations/{id}/validation` returns a `ValidationResultPage` only after
the original summary seal is available. Pending compilation/publication is
`409 validationPending`, with the original operation identity and Retry-After;
it is never represented by `valid: false`. Interrupted, canceled, expired and
unavailable history remain distinct conditions. The page exposes only bounded
metadata: operation and input identity, result/plan identity, capability digest,
finalized/expiry times, verdict, original input ordinal, kind/ID, opaque source
coordinates, change/issue and the original UID/version pair when available.
It contains no resource body, credential, filename or authority revision.

Pages default to 100 rows, reject more than 500, and have a 4 MiB encoded-response
ceiling. Cursors bind the original result descriptor, fixed publication watermark,
authenticated reader/policy context, limit and next ordinal. A validation-limit
rejection for a declared input above 10,000 resources is explicitly summary-only,
with zero result rows; it is not a truncated ordinary result. Ephemeral Preflight
and the ordinary operation receipt retain their separate contracts.

The SDK's `Operations.Validate` returns the 202 operation. `Validation` reads a
page; `WaitValidation` only polls that read route and preserves the caller's
operation handle on cancellation. A pending problem must identify the original
operation through its actual response header or problem body before polling can
continue. Polling waits at least five seconds and honors a longer Retry-After up
to 24 hours; unsupported intervals fail explicitly rather than polling early.
`ValidationItems` is lazy and pins the original summary/input identity across
pages. Ordinary observations preserve bounded unknown vocabulary.

`collection.Apply` requires the original frozen identity and a known successful
sealed summary before requesting activation. Returned first-page rows must match
original input positions and supported success vocabulary; it does not load or
claim to individually inspect every remaining result row. `Result.Validation`
retains the immutable summary. Resuming validating/validated/rejected operations
reads the original verdict and never resubmits Validate. General `Operations.Wait`
continues waiting on `validated`, because that is not activation completion; it
recognizes rejected, invalidated, interrupted and both canceled spellings as
terminal operation observations. No wait implicitly cancels a server operation.

The management `CancelCollection` adapter accepts validation-in-progress and
validated/rejected inactive input. It preserves the original finalized verdict
and reconciles the original cancellation receipt. The coordinator observes that
committed cancellation, cancels compilation and joins before selecting more
work. Stop Waiting and Cancel Operation remain distinct controls.

Validation admission and retained reads are registered for discovery. Activate
stays absent: validation performs zero active catalog mutations, job installation
or provider I/O. Public async routes and collection-specific browser handling are
implemented. The connected normal-main Chrome/Worker/WASM/TLS/Raft release-Go
race scenario passes, including original SDK result reconciliation after restart.
The same minimum-Go campaign passed in 10.073 s. This scoped actual-server
scenario does not qualify activation or every release boundary.

## Bounded canonical capability profile

The durable `CapabilitiesDigest` field is checked as a hash-shaped value. The
private builder in `internal/management/collection_validation_profile.go` now
computes that identity from build-derived validation inputs. The private request
adapter commits it, and the runner compares the current profile before claiming
and binds the original profile into the result. Earlier
storage tests use fixture hashes. The digest identifies validation semantics,
not a release executable or machine identity.

The private `collectionValidationProfile()` and deterministic encoder cache
immutable build-derived inputs once and return owned values. Profile fields:

| Field | Source / meaning |
| --- | --- |
| `profileVersion` | Explicit canonical encoding version, initially 1. |
| `apiVersion` | `api.APIVersion` (`cpra.io/v2`). |
| `schemaDigest` | SHA-256 of this build's exact embedded `api.Schema()` bytes. The base/tagged schema is selected by its existing Go constraints. |
| `validationPolicyVersion` | Explicit reviewed version for inert semantic checks and resolution rules in `api/validate.go`, management `runtime.go`, graph/routing/credential resolution; bump when accepted configurations or decisions change. |
| `compilerVersion`, `planCodecVersion` | Existing durable compiler/codec constants. |
| `goos` | `runtime.GOOS`, because platform support such as systemd is part of validation. |
| `resources` | Sorted, unique actually supported `ResourceKinds()`, not every type present in the SDK schema. |
| `drivers` | Sorted unique `(kind, driver, available, configModel)` from `jobs.Capabilities()` joined to the inert `runtimeDriverFactories` mapping. Exclude human-readable reason strings. |
| `limits` | Resource bytes, input/graph count, graph work/metadata, live borrowed-byte allowance, plan metadata/serialization and result limits that can produce a deterministic validation-limit disposition. |

For `configModel`, the existing inert factory's pointed-to Go type name matches
the corresponding concrete API schema model for all built-in mappings; qualify
that relationship rather than assume it. Every available driver must have a
reviewed runtime mapping and concrete selected schema definition. An unknown,
duplicate or missing mapping fails profile construction. No job constructor,
provider SDK client, credential file, network probe or arbitrary configuration is
invoked. A future external driver needs explicit registered server support and its
versioned validation contract; an external type merely appearing in a tagged SDK
schema does not enable that feature.

Using the exact complete embedded schema is intentionally conservative: even a
contract documentation-only change invalidates the v1 profile. It avoids inventing
an incomplete JSON Schema canonicalizer in this bounded slice. A narrower semantic
schema projection can be versioned later. The outer profile is canonical fixed-
field JSON with sorted lists and no maps; hash a domain-separated encoding such
as `cpra/collection/validation-profile/v1\x00 || canonicalJSON`. Do not hash release
version, commit, compiler executable, installation path, hostname or credentials.

Implemented limits for the builder are 1 MiB selected schema (current checked
artifacts are about 169/194 KiB), at most 64 drivers, 16 resource kinds and 64 KiB
encoded profile. Numeric values are integer counters. No unbounded user input
enters the builder. Whole-schema hashing detects wire-model changes; the explicit
policy version covers semantic Go checks that schema bytes cannot describe.
This version requires a documented review rule and tests, not a claim that a hash
automatically captures arbitrary changes in program behavior.

Scoped tests cover ordering-independent profile identity, exact repeatability,
changed schema/policy/limit/driver-availability sensitivity, missing/duplicate
runtime mappings rejected, all 33 built-in mappings covered, default/all-driver
and tagged matrices, without provider execution. Qualification is recorded in
`bin/verification/collection-validation-profile`; lifecycle/restart qualification
is recorded separately below; the public async contract is described above and
its scoped connected release/minimum-Go qualification is recorded below.

## Implemented lifecycle ownership

`runCPRa` in `main.go` owns the coordinator separately from Ark and HTTP, after
committed authentication and catalog verification and before HTTP readiness.
`Store.CollectionValidationWork` returns at most 64 detached metadata observations,
sorted by original request time and numeric operation sequence. It copies only
original request/claim identities, authority/profile, exact progress fences,
timestamps and finalization state. It reads no protected resource payload,
ledger row or retained-history segment. Lock acquisition is cancellable; recorded
storage failure, reset/restore, administrative opening and nonleader states fail
explicitly. This observation is not an execution grant.

Registration is once for the lifetime of an opened Store, even if another Catalog
points at it. The registering coordinator retires abandoned eligible claims
synchronously before launching work. A second coordinator cannot perform a
restart sweep, including after the first has joined. Finalized and terminal
outcomes remain unchanged; expired headers remain available to existing bounded
maintenance. Unclaimed requests may receive their first claim after current
authority/profile checks. Claimed requests never receive a new compiler attempt.

One active compiler is supervised by a one-second poll and a coalesced wakeup.
Committed cancellation, expiry and authority changes request cooperative
cancellation; the supervisor joins the attempt before selecting another. It
flushes uncertain submissions before reading the committed prefix for an audited
interruption. It never refreshes original request, claim, plan or result identity
to resume work. Storage/policy-read failure is infrastructure unavailability;
backdated time fails explicitly rather than fabricating an observation. A known
stale authority or profile instead produces an inert interruption.

Main includes coordinator readiness in HTTP readiness and observes fatal worker
termination during initialization and steady operation. Shutdown closes validation
admission, requests cancellation, stops HTTP mutation admission and joins the
coordinator before the durable flush, controller drain and store/keyring close.
Early-error paths use the same ownership barrier. The configured shutdown deadline
bounds the caller's wait; it cannot forcibly stop arbitrary Go/KMS work. If that
deadline expires, storage and encryption remain owned until process exit. The
supervisor must enforce the hard process deadline. A coordinator-only failure
closes catalog admission without inventing a storage fault, permitting the normal
flush once the compiler has joined.

Collection-specific browser handling now distinguishes accepted validation,
read-only waiting, sealed verdicts and interrupted outcomes. It does not depend
on the general resource-operation decoder to recognize every collection phase.
The scoped real-browser release-Go and minimum-Go campaigns pass.

## Coordinator acceptance evidence

Use actual TLS routes and current durable authentication, plus real-process Raft
restarts. Cover duplicate Validate, lost Begin/append/finalize replies, conflict
with a competing coordinator, cancel between pages, token revocation or policy
rotation during blocked decryption, expiry, restore, schema/profile changes,
original results after staging cleanup, unavailable retained segments and cursor
bounds. Crash cases must distinguish a parent retaining original artifact bytes
from server-only recovery. Keep counters for active catalog records and provider
calls at zero throughout Validate, including rejected inputs. The current private
runner tests cover valid/rejected outcomes, original result order and source
coordinates, concurrent request reconciliation, zero active records/provider
calls, cancellation after claim and plan Begin, and joined streaming cancellation.
Scoped source hashes and checks are recorded in
`bin/verification/collection-validation-request-integration`. Storage request,
snapshot, receipt/cleanup and protected-view reports are separately recorded in
`collection-validation-request-core`, `collection-validation-request-snapshot`,
`collection-validation-request-receipts` and `collection-validation-attempt-view`.
Later lifecycle qualification covers the private worker and normal main path:

| Slice | Executed evidence |
| --- | --- |
| Metadata selector and Store registration | Go 1.27.1 ordinary 2.037 s, race 3.563 s; Go 1.25 2.054 s. Detached bounded observations, lock cancellation, one registration, real Raft reopen and no state mutation. |
| Worker supervision | Go 1.27.1 race 1.305 s. Second-Catalog exclusion, single attempt, blocked unwrap and shutdown ownership, authority expiry, profile rejection before decryption and fatal clock isolation. |
| Real-process worker restart | Go 1.27.1 race 6.689 s; Go 1.25 6.078 s. Killed-process recovery preserves original identities, retires claimed/partial work, permits only never-claimed work to compile, and preserves finalized evidence. |
| Normal main lifecycle | Go 1.27.1 race 29.771 s; Go 1.25 5.021 s; `externaljobs` race 6.063 s. Normal startup, readiness, restart retirement, shutdown and zero validation-triggered activation. |

Independent source reviews passed. Reports with commands, source hashes and
observation boundaries are in `bin/verification/collection-validation-work`,
`collection-validation-worker`, `collection-validation-worker-restart` and
`main-collection-lifecycle`. The earlier full durable race pass of 397.519 s
predates the selector and lifecycle additions; it is not their full-package
qualification. These scoped checks do not complete the public HTTP/result/cursor
matrix, connected SDK/browser Apply, provider-account, platform, packaging or
million-monitor endurance gates. No publication is authorized by this evidence.

## Async client qualification checkpoint

`bin/verification/sdk-async-validation/result.json` records source hashes, exact
commands and passing Go 1.27.1 default/tagged race, Go 1.25 default/tagged, vet,
two-root generation, generated-reference consistency and example-module checks.
Root independent SDK source review passed. Actual TLS tests in the SDK exercise
pending cancellation without mutations, explicit verdicts, identity rejection,
page bounds, unknown read observations and immutable lazy pagination. The
example server remains an in-memory immediate-seal fixture with fixture-only
cursors/digests; it is not runtime, retained-history or provider evidence.

The actual normal-main browser/TLS/Raft/restart campaign passed Go 1.27.1 race
in 11.608 s (test 10.53 s), with Chrome 153.0.8010.36, Node 24.21.0 and
Playwright 1.56.1. It made exactly five explicit writes, submitted no Activate,
observed zero target requests/active catalog changes, and retained the same
operation/result/digest through restart. Plaintext canaries were absent from the
checked responses/storage. Source and embedded-asset hashes are in
`bin/verification/collection-validation-browser/fixed-release-race.json`; the
first failed attempt is preserved and exposed the SDK empty-cursor serialization
defect corrected before this pass. The same Go 1.25 campaign passed in 10.073 s
(test 10.05 s). Parser reproduction and byte-for-byte Vite/embedded asset equality
also passed. Consolidated evidence:
`bin/verification/collection-validation-browser/result.json`.

Conditional activation, active item outcomes,
collection listing, refreshed-input reselection, CLI apply/validation commands,
external workers, provider accounts, platform/package gates and the million-
monitor 24-hour campaign remain unfinished. No publication was performed.

The next [collection operation-listing seam](collection-operation-listing.md) is
a design proposal, not implemented listing. Its capture, ownership and bounded
history requirements remain acceptance work. Separately, the complete default
`internal/httpserver` race suite passed in 52.800 s, including actual SDK
first-page/continuation/WaitValidation calls and all route/auth/cursor tests.
Its command, source scope and output are recorded in
`bin/verification/collection-validation-http/full-server-race.json`. This does
not qualify conditional activation or the complete release.
