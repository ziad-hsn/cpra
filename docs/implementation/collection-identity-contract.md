# Collection identity and preflight

This document records the bounded cryptographic protocol implemented under
[`sdk/go/collection/commitment`](../../sdk/go/collection/commitment/README.md).
It supports the collection work in the
[management API plan](api-management-plan.md). The private working branch now
connects keyed SDK freezing to `POST /api/v2/collections/preflight`. The normal
TLS/Raft application has been exercised through that public SDK path, including
multi-file references and restart without activation. Persistent collection
upload, activation, cancellation and resume routes remain unfinished.

The earlier SDK collection digest included raw source material and labels that
a receiving server could not reconstruct from its upload objects. Plain hashes of
credential payloads would also allow a reader with operation receipts to test
candidate secret values. The prepared format uses a fresh private 32-byte key for
each frozen collection and domain-separated HMAC-SHA-256 commitments. Only opaque
digests may become reader-visible; the key and source fingerprint remain private.

The client freezes each resource object's exact serialized bytes. Receivers must
extract the uploaded object's original byte span before semantic decoding, verify
its MAC, then validate schema, identity, authorization, and references. Receivers
must not reconstruct that span by re-encoding a map or a typed resource. This
avoids assumptions about JSON key order, Unicode escaping, or numeric spellings
being identical across languages.

The [package framing specification](../../sdk/go/collection/commitment/README.md)
defines domain separation, eight-byte length prefixes, unsigned big-endian
coordinates, source boundaries, and ordered inventory construction. The package
accepts only bounded `Kind/ID` identities and canonical ordered source tokens such
as `source.00000000000000000001`. Filenames and URLs never enter token fields.
Source bytes are processed incrementally under an explicit total quota. Empty
collections have a defined local identity, while an operation API may reject a
zero-item submission.

The helper tracks ordinal/count progression in constant memory. Durable staging
still has to enforce unique resource IDs, exact source-coordinate MAC binding, exact
resumed-prefix identity, principal/epoch binding, and per-operation quotas. An
item MAC is not permission to execute a provider, and the inventory MAC does not
replace whole-union validation or per-resource version preconditions.

The canonical schema conveys format, key, source fingerprint, item count and
inventory commitment. Private key/fingerprint fields are required on requests
and marked write-only; receivers enforce their presence despite the generator's
optional-pointer representation. Collection responses acknowledge the format,
digest and count, with count absent for ordinary non-collection operations.
SDK collection helpers reject missing or mismatched acknowledgments before
continuing a mutable operation. Item uploads carry exact identities and source
coordinates while preserving the resource-object wire shape. The future
persistent stage must retain private values encrypted; preflight retains them
only for the request and creates no encryption keys, files or durable operation.
They never belong in ordinary operation observations, history, logs or diagnostics.
A source fingerprint binds the
client's claimed frozen source input; it does not certify where the source came
from because the server never fetches or receives those original source streams.

Resumption must preserve the original key and exact item bytes. Refreezing the
same files with a fresh key creates a different identity. No browser-refresh or
cross-process client resume mechanism is claimed by this helper; a later client
persistence design must safely retain the original frozen inputs before such a
workflow can be promised.

Preflight admits an authenticated operator on the approved HTTPS origin before
reading the body. Its request limit is 4 MiB, each resource is limited to 1 MiB,
and it accepts at most 10,000 submitted items. The graph validator independently
bounds retained desired/live resource versions, accounted bytes and routing work.
Actual body reading has a ten-second deadline and shares the bounded management
validation capacity. Graph rejection returns `valid: false` with safe item/field
classifications; malformed wire data and invalid commitments are request errors.
Every proposed item reports `committed: false` and `applied: false`.

The public package includes independent Python-generated fixed vectors and Go
tests for bounds, ordering, source chunking, altered payloads, different keys,
finalization errors, and private-state cleanup. A separate real-TLS fixture uses
the public SDK upload method and checks raw spans with difficult Unicode, HTML,
escaped-key, and numeric inputs. Those payloads deliberately exercise serializer
behavior without claiming real driver configuration. Separate actual server
tests exercise preflight admission and validation; neither fixture establishes
persistent collection application.

Both accumulator types offer idempotent `Close` for deferred cleanup. Completion
and validation failure drop MAC references, and the source accumulator clears its
explicit key buffer. Formatting is redacted and JSON serialization is rejected.
These measures limit accidental disclosure; they cannot guarantee erasure of
compiler/runtime/crypto-library copies or defend against hostile in-process code.

Remaining integration work includes encrypted durable stage commands and
resumable receipts, incremental validation for collections exceeding the bounded
preflight limits, conditional activation and dashboard/CLI collection flows.
Stage idempotency must preserve original ciphertext and item identities, and
catalog admission must be atomic with each item's durable receipt. The private
candidate and publication gates remain unchanged.
