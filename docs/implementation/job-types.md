# JobType persistence and validation

This is the private implementation boundary as of 2026-09-24. Tagged storage,
management preparation, five JobType HTTP operations, configuration receipts and
runtime opt-in are implemented. [Stopped worker provisioning and scoped grants](../worker-authentication.md)
are also implemented. Assignment, execution, worker result receipts and external
configuration/collection activation remain unfinished. Internal encrypted
configuration records can now pin authenticated immutable JobType references;
ordinary API, bootstrap and runtime admission still reject external drivers.
The JobType SDK
methods now have real-server evidence; worker interoperability and shipping
Ticket 8 remain incomplete.

## HTTP and admission

An `externaljobs` build must also set `external_jobs.enabled: true` in runtime
configuration. Enablement requires authenticated management and Raft storage,
including the existing protected transport and encryption configuration. The
setting is omitted or false by default. Untagged runtime configuration rejects
the entire `external_jobs` section, even with `enabled: false`.

The registered operations are:

| Method and route | Permission | Successful response |
| --- | --- | --- |
| `GET /api/v2/job-types` | Reader or operator | `JobTypeList` |
| `GET /api/v2/job-types/{id}` | Reader or operator | `JobType` |
| `POST /api/v2/job-types` | Operator | `JobType` |
| `PUT /api/v2/job-types/{id}` | Operator | `JobType` |
| `DELETE /api/v2/job-types/{id}` | Operator | `Operation` |

Create requires `If-None-Match: *`; replace/delete require the exact strong
`If-Match` resource version. All five return 200 on success. The current tagged
SDK contract has no JobType PATCH operation. Runtime-disabled or untagged
servers do not register or advertise these routes. Enabling them registers no
worker polling, start, heartbeat or result route.

Mutation preparation compiles schemas and seals the resource before admission.
Admission allocates an authenticated operation handle bound to the frozen
ciphertext, identities, version guards and operator policy. It then commits the
resource and a terminal audit receipt in one state-machine application. A
`completed` receipt means configuration was stored; it does not mean a worker
ran. The public operation API can observe that original receipt after restart.
The response includes `X-Operation-ID`, `X-Commit-Index`, `X-CPRa-Admission`, ETag
and the resource version. Confirmed allocation handles are preserved on uncertain
target outcomes. Unconfirmed allocation submits no target mutation. Neither path
automatically retries the mutation.

Lists use five-minute immutable encrypted snapshots bound to the principal,
authorization generation and original page size. Page sizes default to 100 and
are capped at 500; encoded responses are bounded to 8 MiB. An earlier cursor
retains earlier values across replacement, deletion and recreation. Nonempty
selector/monitor filters return explicit feature-unavailable responses.

Capture reserves at most 65 MiB before copying descriptors, then shrinks the
charge to their encrypted encoding plus 1 KiB per descriptor for bounded index
and descriptor overhead. Budgets are 130 MiB per principal and 260 MiB globally,
in addition to existing cursor-count limits. Active reads retain their charge
after cursor expiry until they release the view. These are accounting limits,
not a total heap or RSS claim. Decryption and page construction hold neither the
cursor mutex nor the authorization lock; current authority and expiry are checked
again before publishing data.

## Storage contract

Only `externaljobs` builds include the JobType command, state, management methods
and schema evaluator. Default builds retain storage format 14. JobType receipts
use format 16; tagged builds additionally support worker policy in format 17
and configuration references in format 18.
Format 15 remains readable for the earlier private registration foundation.
Opening an external format with an untagged binary fails explicitly,
including when an external field is null or is attached to an older envelope.
The external formats preserve all format-14 collection snapshot sections.
Format 16 adds authority-bound operation reservations and terminal registration
receipts. Earlier format-15 command bytes use a frozen digest projection;
existing authentication and ordinary operation digests remain unchanged.

A worker grant pins a retained JobType incarnation and immutable version.
Deletion returns `409 resourceReferenced` while any non-revoked worker has a
grant for that incarnation, including an expired worker. Remove its grants or
revoke the worker explicitly before deleting the JobType. Metadata replacement
and admission of a new immutable version preserve existing version grants.

JobTypes occupy a dedicated namespace separate from the ordinary resource catalog.
Generic catalog, bootstrap and controller-recovery paths cannot insert them.
Registration is not an execution grant. The FSM performs no schema compilation,
key wrapping, network access or handler invocation.

Each ID retains its current descriptor and immutable encrypted versions. Resource
incarnation, configuration revision, generation and implementation version are
distinct. Create and recreate assign a new incarnation and generation 1;
replacement and deletion advance generation. Current-revision CAS and a captured
committed operator-policy fence protect changes. The current category cannot
change, including across deletion and recreation. A previously retained version
name cannot be reused for another implementation.

Metadata-only replacement of the current version authenticates the original
encrypted spec and compares it exactly before preparing a conditional command.
Its original version ciphertext remains unchanged. Numeric comparisons preserve
values beyond float64 precision. A changed handler, schema, timeout, protocol or
rejection policy requires a new version. Historical-version reactivation is not
implemented. Deletion leaves a tombstone and retained versions; no version is
evicted to make room. Future executions must select their pinned immutable version,
not the current descriptor.

Schemas and editable metadata are encrypted before submission. Plaintext
selectors contain only identity, category, handler, implementation/protocol/schema
versions. Startup authenticates every retained version and the current descriptor,
including versions retained under tombstones. It rejects inconsistent current
specs. Prepared values redact pointer and value formatting and refuse JSON
serialization. Encryption and decryption preparation recheck captured authority
before and after key operations; the FSM checks it again at admission time.

| Storage bound | Limit |
| --- | --- |
| Retained IDs, including tombstones | 1,024 |
| Immutable versions per ID | 64 |
| Encoded state per ID | 8 MiB |
| Aggregate encoded JobType state | 64 MiB |
| Internal current-descriptor page | 100 rows or 4 MiB |

Reads return detached state. Snapshot data is copied before serialization can
overlap later mutations. Quota failures preserve all earlier admitted state.
Stopped backup/restore continues to include the entire storage directory.

## Configuration references

Tagged encrypted catalog records retain a separate canonical set of JobType
references: ID, incarnation UID, immutable version, original version revision
and category. A record can retain at most 64 distinct references. Monitor
records support check, recovery and inline notification references; notification
endpoints support notification references. Parameters and worker-local credential
profile selectors remain inside the encrypted resource payload.

Preparation resolves an active JobType incarnation and validates parameters
against its retained schema. The FSM checks the same exact selectors atomically
with the catalog mutation. A JobType deletion fails while an active source
record retains its incarnation. Removing or replacing that source releases its
previous live references. Metadata-only edits and a newer current JobType
version preserve existing immutable pins.

Reading an older encrypted catalog snapshot authenticates its original retained
version even after source deletion and JobType recreation. It never rebinds the
old configuration to the new incarnation. Missing or substituted reference
metadata fails integrity validation. Cancellation and storage-read failures do
not by themselves establish catalog corruption.

External drivers cannot resolve CPRa-held `credentialRefs`. Their optional
`credentialProfile` names worker-local configuration. The credential resolver
rejects an external credential-reference map before querying or decrypting any
Credential resource.

This is a storage and validation prerequisite. The execution adapter, external
configuration admission, collection integration and worker protocol are still
required. The existing compiled-driver guards remain closed; these changes do
not advertise runnable external monitors.

## Schema profile

`cpra.schema.v1` is a bounded CPRa vocabulary based on Draft 2020-12 semantics,
not a complete JSON Schema implementation. Its interpretation is persisted with
each immutable JobType version. A future profile requires explicit compatibility
handling; it must not silently reinterpret an existing execution contract.

Supported keywords are `type`, `properties`, `required`, `additionalProperties`,
`items`, `minItems`, `maxItems`, `minProperties`, `maxProperties`, `minLength`,
`maxLength`, `pattern`, `minimum`, `maximum`, `exclusiveMinimum`,
`exclusiveMaximum`, `multipleOf`, `const`, `enum`, root `$defs`, and `$ref` to
one root definition. Boolean schemas are supported. `title` and `description`
are bounded annotations. An optional root `$schema` must be exactly
`https://json-schema.org/draft/2020-12/schema`.

All other keywords are rejected, including unsupported formats, defaults,
combinators, dynamic references and content processing. No schema is fetched.
References use `#/$defs/NAME`, with `~0` and `~1` JSON Pointer escaping. URI,
percent-encoded, anchor, nested-pointer, unresolved and cyclic references are
rejected. Unused definitions are validated too. The implementation shares compiled
reference nodes rather than expanding them.

Reference siblings also apply. `properties` does not imply object type, absent
`additionalProperties` allows other fields, and `required` tests presence.
Numeric equality is exact within the limits below: `1`, `1.0` and `1e0` are equal
integer values. String lengths count Unicode code points. Patterns use Go's RE2
syntax and are unanchored unless the pattern provides anchors. These semantics
follow the [JSON Schema core](https://json-schema.org/draft/2020-12/json-schema-core)
and [validation](https://json-schema.org/draft/2020-12/json-schema-validation)
specifications within the declared subset; regex restrictions follow
[Go regexp](https://pkg.go.dev/regexp).

| Evaluation bound | Limit |
| --- | --- |
| Each parameter or result schema | 64 KiB |
| One data value | 128 KiB |
| Parsed JSON values | 8,192 |
| Schema nodes | 1,024 |
| Nesting/reference depth | 32 |
| Evaluation steps | 100,000 |
| Charged scan work, including regex instruction weight | 8 MiB |
| Numeric token / exponent | 128 characters / −308 through 308 |
| Enum entries | 64 |
| Pattern source / compiled instructions | 256 bytes / 1,024 |
| Aggregate compiled pattern instructions per schema | 16,384 |

Duplicate keys, trailing input and invalid UTF-8 are rejected. Parsing,
compilation and evaluation check cancellation and share bounded work accounting.
Errors expose fixed categories rather than values or arbitrary field names.
Budget exhaustion is an explicit failure, not schema success or a silently
truncated result. There is no provider I/O or durable mutation during validation.

Protocol `1` is the only accepted worker protocol at this foundation. JobType,
version and handler identifiers are bounded ASCII tokens. Built-in driver names
are reserved even when their optional driver is not compiled. An explicit timeout
must be positive and at most 24 hours; omitted timeout resolution belongs to the
future execution integration. At most 32 distinct rejection classifications are
accepted. A classification is not yet retry authority: that requires the pending
server execution state machine.

## Qualification boundary

Focused tests cover schema semantics and limits, exact-number preservation,
concurrent version conflicts, immutable ciphertext, authority expiry during key
wrapping, stopped revocation and native Raft reopen, startup contract comparison,
tag exclusion, historical digests, quotas and snapshot isolation. The
[foundation evidence directory](../../bin/verification/job-type-foundation-2026-09-24)
keeps the initial scoped checkpoint. The
[HTTP evidence directory](../../bin/verification/job-type-http-2026-09-24)
records the subsequent API, cursor, audit and compatibility checks, including
the real SDK over TLS/Raft, lost-response reconciliation and restart. The
[runtime qualification](../../bin/verification/externaljobs-runtime-2026-09-24/qualification.json)
covers strict configuration, default/all-built-in exclusion and normal startup.
The [configuration-reference checkpoint](../../bin/verification/job-type-references-2026-09-24/result.json)
records the subsequent storage-format, encrypted-reference, deletion, retained
read and credential-separation checks. Its private fixtures do not qualify
public external configuration activation or worker execution.

No real worker has been granted execution by these additions. Separate-process
lost-start/lost-receipt, handler invocation counts, unknown outcomes, late evidence,
outbox capacity and shutdown tests remain required by the
[external-worker plan](external-worker-server-next-steps.md). Provider-account,
native-platform packaging and million-monitor/endurance gates remain separate.
