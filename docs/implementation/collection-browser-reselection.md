# Collection reselection after a browser refresh

Status: shared file normalization, encrypted source proof, conditional suffix
transfer, authenticated HTTP/SDK operations and the refreshed-browser upload
flow are implemented locally. See the
[runtime workflow and qualification boundaries](collection-reselection-runtime.md)
and [source/proof component record](collection-reselection-proof.md).
Existing SDK `Resume` requires the
original open `Frozen` instance. Creating a new instance generates a new private
identity even when its input files have identical contents.

## Required identity

The approved dashboard plan requires a refreshed tab to resume the original
operation after the operator signs in again and reselects identical input. It
does not permit changed input to replace an operation's frozen collection.

The current identity has two independently significant inputs:

1. `SourceAccumulator` commits the ordered raw source bytes, lengths and source
   boundaries under the original private key. Opaque source tokens identify
   position; filenames and URLs are absent. Comments, whitespace and empty files
   contribute to this commitment.
2. The inventory commits each resource's exact transmitted JSON bytes, identity,
   ordinal, and source/document/item coordinates under that same key.

The original key and source fingerprint already reside in the encrypted durable
operation header. They must remain write-only. A fresh client cannot transform
its new-key commitments into the old-key commitments. Matching decoded resource
values alone is insufficient. Resumption must verify both original commitments,
without returning either the key or the private source fingerprint.

## Implemented file-source workflow

The implementation provides authenticated, server-assisted **file-source reselection** as an
optional capability. An attempt is disposable verification state subordinate to
an existing operation; it is not a new apply operation or an alternate operation
identity. Ordinary original-`Frozen` SDK resumption remains available separately.

The browser sends selected raw bytes to the same authenticated CPRa origin, with
opaque sequential source tokens. The server uses the original key internally,
parses with a supported normalization profile, and reconstructs the complete
original inventory. It may append the missing suffix only after every identity
check succeeds. Validation and activation remain separate explicit operations.

This changes the source-data boundary: the server receives complete selected
files, including comments or unused source text that did not appear in normalized
resources. The import description must say so. Do not fetch a client-supplied URL
on the server, accept archive extraction, submit filenames as multipart metadata,
or send local paths or signed source URLs. Browser filenames remain local display
labels. SDK URL readers fetch through their separate unauthenticated source
client and explicitly transmit the resulting bytes if this capability is used.

## Normalization compatibility

The original create contract recorded `identityFormat` without a normalization
profile. The HMAC format specifies exact bytes; it does not standardize a parser
or serializer. Re-running a changed parser cannot guarantee cross-version
reconstruction.

New browser operations now declare `normalizationProfile: cpra.file.base.v1`
in Prepare/Create and retain it in protected operation metadata. The shared
`collection.NormalizeFile` implementation defines UTF-8 validation, resource
serialization, source/document/item coordinates and the base resource/driver
projection. The profile identifier does not select a server executable or load
old code. Typed-resource `FreezeResources` and ordinary SDK `Freeze` retain
their existing contracts and do not automatically claim this profile.

The parser build gate checks fixed raw-input-to-exact-JSON-and-coordinate
fixtures against compiled browser Wasm and both native base and `externaljobs`
adapters. The same base profile excludes JobTypes and external check, recovery,
notification and notification-selector variants in both builds. Its limits are
64 MiB of raw source, 1 MiB per encoded resource and 10,000 resources. The
16 MiB document setting bounds parser metadata; it is not a whole-document
ceiling for streamed lists. Fixed fixtures also pin that accounting. Changing
v1 serialization, coordinates or accepted input requires a new profile.

Storage format 14 binds a nonempty profile into both the admission commitment
and encrypted identity envelope. Omitted legacy profiles preserve the original
admission HMAC and encryption binding. Explicit JSON `null`, empty or unsupported
profiles are rejected on admission. Receipts preserve the recorded profile
through snapshots, replay, result publication and source cleanup. Future unknown
profile observations remain readable but cannot grant browser write support.

Format 14 also adds an optional internal upload fence. Current actor authority
and the exact uploaded count, byte count and prefix digest are checked in the
same FSM transaction as append. An advanced prefix, including an already
accepted row, conflicts; a caller must reconcile its original ciphertext through
a protected read. Ordinary legacy upload replay retains its previous semantics.

Old operations without a profile can be attempted with an explicitly supported
decoder, but success requires exact final commitment equality. Failure cannot
be converted into success through semantic comparison or a new digest. Expose
unsupported normalization distinctly from ordinary resumability; do not promise
all unprofiled operations are recoverable after client state is lost.

Build/source hashes remain provenance, not semantic profile identifiers. These
prerequisites alone do not establish cross-process reselection. The implemented
attempt protocol verifies raw sources and the complete
inventory before appending any missing row.

`FreezeResources` is another distinct profile: it hashes a synthetic stream of
serialized objects plus LF, but gives every resource `Document=1`. Ordinary file
decoding is not a substitute for that typed-stream contract. The first browser
file-source capability must not claim universal SDK coverage. A later protocol
may accept original normalized item bytes as a second bounded stream, checking
them under the server-held original key. That would support additional
serializers, but still cannot reconstruct lost bytes after an incompatible
client normalization change.

## Additive API shape

Exact route names require the normal API review. A concrete minimal shape is:

| Operation | Request | Safe response |
| --- | --- | --- |
| Create reselection attempt under an operation | Source count and supported profile | Opaque attempt ID, phase, quotas and expiry |
| Upload source part | Source ordinal, byte offset, explicit end-of-source flag and bounded binary body | Accepted source ordinal/offset and phase |
| Finish verification | Attempt identity and expected progress | Verification state or safe error; no commitments |
| Read attempt | Attempt identity | Counts, offsets, phase, expiry and original operation handle |
| Append verified remainder | Attempt identity and captured operation precondition | Original operation progress |
| Discard attempt | Attempt identity | Completion acknowledgment |

An attempt ID is not sufficient authorization. Require the normal mutation
authorization on every request, a matching operation owner, and current resource
permissions. If a different operator may take over, that must be a separately
specified, authorized and audited policy; ordinary operation-read permission
must not grant it. The attempt is fenced to store/restore epoch, original upload
identity, original actor, original count, and immutable inventory identity.

Use TLS and the existing bounded request reader. Authenticate before consuming
bodies. Explicit binary framing avoids base64 expanding a 4 MiB source chunk
past the request ceiling. The actual total request, including framing, remains
at most 4 MiB; declared content lengths do not substitute for a streaming limit.
Reads retain the existing actual-read timeout. Bound simultaneous attempts and
CPU verification separately from controller admission.

New attempt responses return neither item MACs, source fingerprints, unkeyed
source hashes nor the old key. Existing ordinary operation responses already
contain an opaque keyed inventory digest; this design adds no private digest
response. Errors must not interpolate raw parser errors, supplied values, or
source labels. Source/document/item positions are sufficient for local
attribution. Rate-limit repeated failed identity comparisons for an operation.

## Bounded verification and storage

1. Capture the original uploading header and immutable uploaded prefix. Check
   authorization, current time, restore epoch and phase before opening the
   protected identity. No read or failed attempt extends the original expiry.
2. Accept sequential source parts into an exclusively owned encrypted temporary
   spool. Bind each part to attempt identity, source ordinal, offset and purpose.
   Use a fresh attempt key retained only in server memory; a restart discards the
   attempt and leaves only encrypted, disposable files for bounded cleanup.
   Never place raw input or the original HMAC key in a plaintext temporary file.
3. Recompute the source fingerprint with `SourceAccumulator`. Decrypt bounded
   parts through a reader; do not join the files into a full plaintext buffer.
   Check the original fingerprint without returning a partial comparison result.
4. Decode one source/resource at a time with the selected profile, using opaque
   labels. Preserve exact serializer output and source coordinates. Enforce
   duplicate identities and current authorization. Compute item MACs with the
   original key and feed the complete ordered inventory accumulator.
5. For ordinals already uploaded, compare the original row identity, coordinates
   and item commitment; discard the candidate plaintext. Retain only the missing
   suffix as encrypted temporary rows. This avoids a second copy of the entire
   normalized collection. No original row is changed during verification.
6. Require complete parsing, all sources, the original item count, and exact
   final inventory equality. A malformed last file, permission failure, quota
   exhaustion or mismatch discards the attempt without changing the original
   upload. No provider operations are permitted in any of these steps.

The initial browser limits remain 1,000 sources, 64 MiB raw input, 10,000
resources, 1 MiB per resource and 512 MiB total normalized bytes. Independently
bound parser document size, decoded scratch, identity indexes, actual encoded
temporary disk usage, elapsed verification time and active attempts. The SDK's
larger configured limits are not silently adopted as browser server quotas.

Account for original prefix plus encrypted raw source spool plus encrypted
missing suffix and transfer headroom. Ciphertext framing and base64 in durable
rows consume real space. The current 1 GiB collection-ledger limit is not a
promise that every maximum-size attempt fits alongside other operations or that
total disk usage is 1 GiB: Raft logs, snapshots and history are additional.
Reserve enough actual temporary and durable capacity before appending; reject
insufficient space explicitly. Logical byte reservations do not establish an
RSS bound. Measure peak memory and encoded disk usage in the maximum-input test.

## Appending to the original upload

A verified flag alone must not authorize arbitrary later input. Verification
binds the exact frozen encrypted suffix owned by the attempt. Transfer those
rows through the original bounded durable upload path, preserving the original
operation, upload identity, key, ordinal and source coordinates. Never return
the key to the refreshed tab and never create a replacement apply operation.

Before the first append, compare the captured uploaded count and progress
identity, and recheck authorization, phase, current expiry and restore epoch. If
another uploader changed the prefix, reject the stale attempt; the initial
implementation need not merge concurrent uploaders. Do not revive expired,
invalidated, canceled, applying or terminal operations. A complete existing
upload needs no resource append after successful identity verification.

Preserve existing ciphertext for uploaded rows. For new rows, retain the exact
encrypted command until its durable outcome is reconciled: regenerating a
randomized envelope is not an identical ciphertext retry. Once a row is
authoritatively committed, discard its temporary copy. Check fences between
bounded append batches; a failed transfer reports original durable progress.

After a server restart, the temporary attempt is unavailable. Any suffix already
committed belongs to the same original operation; it is not rolled back. The
operator can reselect again, verify the whole original identity, and continue
from the new committed prefix. Similarly, a lost response is reconciled by
reading original operation or attempt progress, without automatic replay of an
unknown mutation. Use accepted offsets and server-private last-part identity to
distinguish an identical source-part retry from altered input at an old offset.

Verification does not renew or waive resource-version guards. Normal whole-graph
validation, unsafe-prefix checks and per-resource conditional activation still
run against their own captured catalog state. A matching source is evidence of
input identity, not permission to overwrite changes made since the first tab.

## Acceptance evidence before enabling the capability

- Refresh, reauthentication and identical files continue a partially uploaded
  original operation; its handle and prior ciphertext rows are unchanged.
- Changed comments, whitespace, ordering, source boundaries, coordinates,
  resource bytes or final file reject without appending to that operation.
- Browser/server profile fixtures match exact bytes in default and external-job
  builds where supported. Typed SDK sources are explicitly supported or rejected.
- Lost chunk and append replies reconcile without changed-byte retries; process
  crashes before proof and during transfer preserve truthful original progress.
- Operation-owner mismatch, revoked permissions, epoch changes and expiry fail
  before disclosure or append. Reads and failed attempts do not keep input alive.
- Maximum allowed inputs stay inside measured memory and disk budgets. Full
  disk, corrupted attempt files, parser timeout and restart leave no plaintext
  files and cannot damage original committed rows.
- Responses, request diagnostics, audit events, URLs and browser storage contain
  no keys, private commitments, supplied credentials or source paths. Deliberate
  attempt records report actor, original operation and safe outcome only.
- Verification, progress reads, retry reconciliation and discard cause zero
  provider operations and zero active configuration changes.

Relevant implementation anchors are `sdk/go/collection/frozen.go`,
`sdk/go/collection/commitment/sources.go`, `sdk/go/collection/commitment/inventory.go`,
`api/openapi/models.base.json`, `internal/persistence/collections.go`, and
`dashboard/src/import/collection.ts`. These files establish current behavior;
this proposal does not change their contracts by itself.
