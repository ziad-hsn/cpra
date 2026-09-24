# Private collection plan artifact

Status: implemented with scoped checks, 19 September 2026. This is the serialization
boundary between the private plan compiler and future durable plan staging. It
does not register Validate or Activate, change a store format, persist a plan,
authorize an actor, or execute a resource mutation.

The compiler's output preserves the original input identity and the exact
resource guards used to validate each ordered prefix. One logical row can carry
more dependency metadata than a single command can accept. Grouping whole rows
into small batches does not solve that case.

The typed artifact therefore separates the header, row beginning, guards,
required earlier outcomes, touched reference targets, row ending and final
footer. Array fragments have at most 256 entries. Each encoded fragment is at
most 1 MiB, and the initial complete serialized artifact limit is 32 MiB. This is
a separate serialization budget from the compiler's metadata/work budgets and
the encrypted input ledger's quota. Exceeding any budget must fail before
activation. It is not a million-resource validation claim.

The header binds codec/compiler versions, plan identity, operation/upload/actor
identity, original inventory and encrypted-input progress digests, input count
and observed catalog position. Rows contain resource identities, original
version guards, prior item references and opaque source coordinates. Resource
specifications, credential values, keys, provider parameters and source paths
are absent. Encryption of original input remains the input ledger's job.

Preparation writes the frozen plan to a hashing sink to obtain the complete
intended artifact digest, byte count and fragment count. The future durable
staging Begin command must bind those values before accepting its first
fragment. A second pass streams the same owned plan in bounded chunks and
compares its complete result with the original descriptor. A failed write or
changed descriptor leaves provisional bytes, never a valid plan.

Both passes use the same compiled result. They neither reread the current
catalog nor compile a replacement suffix. An interrupted upload can resume
only the original artifact identity; a new validation intent requires a new
operation. The caller owns the plan exclusively until writing finishes and must
not mutate it concurrently.

The decoder exposes provisional typed fragments as it reads. Its final footer,
complete artifact descriptor and EOF check must all succeed before a caller
accepts the artifact. Callbacks cannot use an early row as permission to apply
it. Context cancellation is cooperative; an arbitrary caller-owned reader or
writer must provide its own way to interrupt a blocked I/O operation.

The next [staging slice](collection-plan-staging.md) now provides bounded inactive
plan storage, cleanup and a self-contained format-5 snapshot. Remaining public
integration must add immutable validation success or failure, a separate
authorization/capability seal and atomic item mutation/outcome/progress.
The current successful-plan codec does not define durable validation failures
or item execution receipts. See the [activation boundary](collection-activation.md).

Qualification includes exact round trips, original identity commitments, rows
larger than 4 MiB, total quotas, malformed-fragment rejection points, dependency
and target guards, callback ownership, cancellation, short writes and retry
identity. The adapter tests also verify unchanged durable state and absence of
fixture secrets/provider parameters in the artifact. Combined checks passed:

| Check | Durable package selection | Management package selection |
| --- | --- | --- |
| Go 1.27.1 race | 4.334 s | 3.910 s |
| Go 1.27.1 `externaljobs` race | 4.321 s | 3.912 s |
| Go 1.25 | 0.373 s | 0.891 s |

Both affected packages passed vet. Exact commands, source hashes, review status
and limitations are recorded in
`bin/verification/collection-plan-artifact/result.json`. The selection is
`^TestCollectionPlan`; it is not the entire repository suite or a throughput test.
