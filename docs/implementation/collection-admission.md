# Encrypted collection admission

Status: private implementation; not release qualification.

The public inactive-input path now registers `PrepareCollection`,
`CreateOperation`, `UploadOperation`, and inactive-input `CancelOperation`.
Ordinary operation GET also projects collection upload progress and retained
terminal receipts. This does not yet
register whole staged validation, activation, or collection-list
pagination. The dashboard gates its durable Apply flow on the complete set of
actually registered operations.

## Creation identity

Before its first create, the client requests an encrypted admission ticket bound
to the authenticated principal, persistent store identity and operation epoch,
original inventory key, source fingerprint, exact-byte inventory commitment,
format and declared resource count. The ticket expires after 24 hours and is
bounded to 128 KiB. Preparing it initializes only the epoch when needed and does
not allocate an operation or modify active resources.

The SDK and dashboard retain the original ticket in memory before dispatching
Create. A retry uses that ticket and the original frozen input. Expiry or explicit
backup restore never causes automatic replacement of the ticket. Normal restart
uses the retained wrapping keys; explicit restore changes the epoch. Lost Prepare
replies can be retried because no operation was allocated; a lost Create reply
must reconcile the original ticket.

The deterministic state machine consumes a ticket atomically. At most 4,096 live
consumption records retain the original handle independently of temporary upload
headers. A monotonic committed expiry watermark prevents resurrection after
pruning even when subsequent commands carry older observation times. Existing
identity reconciliation precedes active-upload quota checks. Snapshots preserve
consumption records; history is never consulted to decide allocation.

## Upload and authorization

Uploads preserve exact resource-object spans and verify the original keyed item
commitment before decoding or encryption. A chunk has at most 256 resources and
4 MiB of encoded request bytes. Each resource has the existing 1 MiB bound.
Rows are committed as a contiguous inactive prefix. An interruption can leave a
shorter accepted prefix; reading the original operation determines where to
resume. Exact retries reuse the committed ciphertext, including after wrapping
key rotation. Whole-inventory validation remains required before activation.

Authentication precedes body reads. Request reads have an actual connection
read deadline as well as a context deadline. Encryption and KMS calls run outside
the authorization policy lock. Every durable Submit separately rechecks the same
principal, current resource-kind permissions and shutdown admission. A blocked
crypto call does not keep an expired or revoked grant alive. Private decoded
byte buffers are cleared on completion and error paths; Go does not guarantee
complete process-memory erasure.

The durable command and derived ledger receive encrypted input, never the
inventory key or source fingerprint in plaintext. Responses expose only the
original operation handle, commitment, counts, phase and availability. Errors do
not echo provider configuration or ticket material. Reader identities cannot
prepare, create or upload. Uploaded collections stay inactive.

## Inactive cancellation

Cancellation is owner-scoped and accepts only the original operation handle,
without a body or query parameters. It records one cancellation identity and
prevents later upload. Concurrent retries on the application's catalog share a
context-aware admission queue, and repeated requests return the original
receipt without another durable command or event. A canceled receipt remains
readable after temporary header/row cleanup, within the history retention
boundary. This does not implement cancellation of an active application.

Cancellation holds no encryption key or provider connection. It rechecks
authorization and application admission before a new commit, rejects mutation
requests during draining, and leaves receipt reads available. Expired uploads
and handles from an earlier restore epoch cannot be revived by cancellation.

## Remaining integration

Complete and persist the whole-collection validation/activation contract,
dependency-ordered conditional application, per-item outcomes and cancellation
during activation.
Then qualify SDK/CLI/browser parity and native browser operation against the real
server. Refreshed-tab file reselection is still a separate unimplemented protocol.
Current terminal receipt retention is described in
[collection-terminal-receipts.md](collection-terminal-receipts.md).

Independent review found and resolved a timing defect in the first HTTP wiring:
permission admission originally wrapped slow encryption. Authorization now occurs
at each durable Submit, with regression tests for expiry/revocation while wrapping
is blocked. Integrated admission/cancellation and normal-startup restart tests
pass with the race detector on Go 1.27.1 and with Go 1.25.0. The full web/server
suite also passes. The restart test uses the real SDK, TLS listener and Raft
state; it stages two inactive rows and reconciles the original handle after a
graceful owner restart, then verifies cancellation and the original receipt
across a second restart. It retains the same in-memory SDK Frozen input; this
does not establish browser reselection, forced-termination or activation
behavior. Commands, results and source hashes are recorded in
`bin/verification/collection-public-admission/result.json`.
