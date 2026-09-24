# Encrypted source staging and original-input verification

These private components support the pending
[file-reselection protocol](collection-browser-reselection.md). They do not
register HTTP routes, advertise a resume capability, append missing durable
rows, validate the resource graph, or activate configuration.

## Temporary storage

`collection_reselection_spool.go` uses a private directory and an exclusively
locked bbolt ownership database. Every attempt receives a random AES-256-GCM
key held only in memory. Source and suffix frames have separate authenticated
purposes. Their associated data includes the attempt, source/ordinal, position,
file offset, length and format. No recovery key is written beside the files.

The primitive limits each plaintext frame to 1 MiB and each key to at most
65,536 accepted encryptions. Cancellation after encryption retires the spool,
so an uncommitted encryption does not release its nonce budget. Failed writes
or synchronization poison the attempt. Quotas account for plaintext and exact
encoded file lengths; they do not represent allocated filesystem blocks, bbolt
allocation, physical free space, or RSS.

Cleanup first acquires exclusive ownership. It bounds the directory scan and
compares captured file identities before removal. Changed or unknown entries
are preserved, and a cleanup failure stops new root admission. The directory
must already be private and trusted; this does not isolate the process from a
hostile program running as the same OS account.

Restart discards attempts and their memory-only keys. A killed process leaves
disposable ciphertext for bounded cleanup. A crash during the first ownership
database initialization can leave an unmarked database; reopening refuses that
state and requires stopped operator inspection. It is not silently repaired.

## Ordered source input

`collection_reselection_sources.go` admits 1–1,000 sequential sources with at
most 64 MiB of total raw input. A source part carries an ordinal, byte offset
and explicit end flag. Empty files receive an authenticated empty end frame.
An exact retry of the last accepted part compares against the original
ciphertext without changing progress. Earlier or altered parts are rejected.

Readers become available only when every source is complete. Each reader owns
at most one frame of scratch and releases spool locks before returning bytes.
Scratch is cleared on exhaustion, close, cancellation and errors. Reader
callers must close readers, including on panic. Source completion and frame
indexes are process-local; the future attempt owner must enforce expiry,
authorization and aggregate admission limits.

## Original-input proof

`collection_reselection_proof.go` captures the named operator's current committed
authority and reads the original operation through its owner-scoped lookup.
It then captures the exact uploading prefix. The explicit `cpra.file.base.v1`
profile is required. Unprofiled and differently normalized collections cannot
use this component.

Before opening the encrypted identity and after key operations, the verifier
checks the same authority revision, owner, operation epoch, phase, prefix and
expiry. It then:

1. Streams the ordered raw sources through the original keyed source
   commitment, including empty files and source boundaries.
2. Normalizes files through the versioned parser and rebuilds the complete
   inventory with the original private key, coordinates and exact JSON bytes.
3. Checks duplicate identities, declared count, per-resource permissions,
   10,000-resource and 512 MiB normalized-byte limits.
4. Authenticates and compares already-uploaded plaintext against the original
   stored row; stages only missing rows in the encrypted temporary spool.
5. Requires the complete inventory commitment and verifies the original
   ciphertext prefix chain and its encoded-byte total before returning success.

`CollectionUploadView.VerifyPrefix` uses bounded pages and hashes outside owner
locks. A valid re-encryption of equivalent plaintext cannot replace the original
committed ciphertext. Successful verification does not reserve the prefix
against another uploader; durable suffix admission must still use the atomic
upload fence.

Proof objects cannot be serialized or formatted to expose private fields. A
suffix callback borrows one plaintext resource outside spool locks; the copy is
cleared after normal return, error or panic. The callback may prepare a detached
conditional upload only. It cannot safely perform a durable or external action
and rely on the post-callback check to undo it.

The proof does not renew an operation, return its original key or private source
fingerprint, submit a Raft command, or invoke a provider. Parser failures return
a fixed error rather than supplied input or filenames. Clearing explicitly
owned buffers is not a guarantee that the Go runtime erased all copies.

## Verification boundaries

The [proof checkpoint record](../../bin/verification/collection-reselection-proof-2026-09-24/result.json)
records source hashes and the final scoped runs: Go 1.27 independent combined
race tests (11.488 s), Go 1.25 combined tests (11.776 s), and the applicable
built-in-driver proof tests (9.421 s). Native Raft tests use actual log and
snapshot close/reopen, preserve original ciphertext, and exercise stopped
authentication replacement and revocation. They do not substitute a clock
advance for that stopped policy change.

Private component records are stored under:

- `bin/verification/collection-reselection-spool-2026-09-24/`
- `bin/verification/collection-reselection-sources-2026-09-24/`
- `bin/verification/collection-reselection-prefix-2026-09-24/`
- `bin/verification/collection-reselection-proof-2026-09-24/`

These records distinguish focused tests, native Linux amd64 execution, process
kill/reopen behavior, and independent review. They do not qualify native Windows
execution or a complete browser/CLI reselection flow. Attempt orchestration,
permission-bound routing, missing-row transfer, connected restart scenarios,
maximum-input measurements and consumer integration remain required before
enabling the public capability.
