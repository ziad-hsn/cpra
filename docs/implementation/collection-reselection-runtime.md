# Original collection upload recovery

Status: implemented locally; TLS/SDK, normal-startup/restart, native CLI and actual
Chrome upload recovery checks pass. The original-operation continuation controls are
implemented; full integrated dashboard qualification remains open. This is
part of the private dashboard candidate, not a public release claim.

## Operator workflow

For an incomplete operation using `cpra.file.base.v1`, open its operation page,
sign in as its original operator, and select the original YAML/JSON files. The
dashboard orders files by their local names, as in the original import. It sends
the complete raw contents, including comments and empty sources, to the same
authenticated CPRa origin. It does not send filenames, directory paths or URLs.

Choose **Verify original files**. Verification checks both the original raw-source
commitment and every normalized resource under the original encrypted identity.
Changed contents fail verification. After verification, **Resume original upload**
appends only missing resources to that same operation. Existing ciphertext is
preserved. Neither action validates the configuration graph, activates resources,
or invokes providers. Once upload completes, **Validate original collection**
requests validation of that original operation. **Review activation** reads the
retained verdict and current receipt; a separate **Confirm activation** submits
the change. Its confirmation identifies the original operation and explains
conditional per-resource application and possible partial commits.

The bearer token and selected files remain in tab memory. Refresh requires sign-in
and file selection again. Leaving the page does not cancel the original operation.
A lost response stops automatic progress; **Read attempt progress** observes the
existing attempt before further operator action. It never retries a mutation.
When the server reports that a disposable attempt is gone, the page refreshes the
original receipt and permits explicit reselection if it remains eligible.
If the create response is lost before the attempt ID is received, the current
protocol cannot discover that attempt by operation ID. The original operation
remains readable, but a new attempt may be refused until the first attempt expires
or a server restart discards it. There is no automatic duplicate creation.

**Cancel inactive operation** requires confirmation and refreshes progress before
submission. Eligibility is an observation, not an atomic inactive-only guarantee:
the server may stop remaining work if activation begins concurrently. Committed
changes and external actions are not undone. Supported unprofiled collections
retain reads and cancellation; their validation and activation use the SDK or CLI.

## Runtime ownership

Normal managed web startup owns a `CollectionReselectionManager`. Its lifecycle
participates in application readiness, supervision and the common shutdown
deadline. Shutdown stops admission and joins all collection workers before closing
Raft or encryption dependencies. If a worker cannot join by the deadline, those
dependencies and the spool lock remain owned until process exit.

Raft mode uses the private `collection-reselection` child of the opened data
directory. Memory mode uses a separate private OS temporary directory, not the
configured lexical state boundary. Successful memory shutdown removes only the
captured owned files and directories. Unexpected entries or replaced filesystem
identities produce an error instead of recursive deletion.

Temporary source and normalized-suffix frames are encrypted with a key held only
in process memory. Restart discards attempts, performs bounded cleanup of owned
ciphertext, and requires fresh input verification. Stopped whole-directory backups
may include this disposable ciphertext; it is never used to resume execution or
recover a temporary key. The original operation, encrypted identity and accepted
resources remain in persistent storage.

## API and limits

The following operations are registered and advertised only when the server owns
the manager. Normal managed web startup enables it. Every request requires the
original named operator and the current collection-write permissions, including
attempt reads. Reader and legacy credentials cannot access this workflow.

| Method | Path suffix under `/api/v2/operations/{id}` | Behavior |
| --- | --- | --- |
| POST | `/reselection` | Create a disposable attempt with source count/profile. |
| GET | `/reselection/{attempt}` | Read bounded progress. |
| PUT | `/reselection/{attempt}/sources/{source}?offset=0&end=true` | Upload one sequential binary part. |
| POST | `/reselection/{attempt}/verify` | Start verification without durable mutation. |
| POST | `/reselection/{attempt}/resume` | Start conditional suffix transfer. |
| DELETE | `/reselection/{attempt}` | Discard the attempt, preserving the operation. |

Create accepts JSON; source upload requires `application/octet-stream`. Bodies
are read only after authentication and original-owner checks. Responses expose
counts, coordinates, a finite phase/error vocabulary, expiry and operation identity.
They never return keys, source fingerprints, parser diagnostics or source contents.
The Go SDK provides matching methods on `Operations`.

The declared base profile is checked on resource upload and again before staged
input is used for validation or execution, including retained candidates and
unchanged decisions. Enabling `externaljobs` does not broaden this file profile.
Existing unprofiled collections retain their earlier contract. This is enforcement
of the existing profile definition; it does not change normalized bytes, storage
formats or the validation policy identity of unrelated collections.
An incompatible historical profiled row that bypassed earlier admission checks
is rejected before execution. The current execution owner treats that rejection
as an admission failure and stops; it does not automatically repair or cancel the
operation. This fail-closed case is separate from an ordinary provider failure.

| Bound | Value |
| --- | --- |
| Source files / raw bytes | 1,000 / 64 MiB |
| Binary request part | 1 MiB, including an explicit empty final part when needed |
| Normalized resources / resource bytes | 10,000 / 1 MiB each |
| Attempt lifetime | 15 minutes maximum, capped by original upload expiry |
| Verification/transfer work | Two concurrent workers, two-minute maximum per run, also capped by attempt expiry |
| Encoded reservation | 600 MiB per attempt within a 2 GiB manager budget |
| Effective maximum concurrent reservations | Three; a separate four-attempt count ceiling also applies |

These are logical encoded-byte and record bounds, not guarantees about physical
free space or filesystem allocation. Reads and retries do not renew attempt
expiry. One operation has at most one live attempt; creation is throttled.

Each appended resource uses a persisted conditional prefix/authority fence. If a
commit response is uncertain, the transfer retains that exact encrypted command
and reconciles it against storage. It does not generate new ciphertext and resend
an uncertain mutation. A concurrent uploader, changed authority or changed original
identity cannot silently redirect or merge the transfer.

## Remaining qualification and scope

The component, HTTP/SDK and runtime tests have separate evidence records. The
[combined runtime/browser record](../../bin/verification/collection-reselection-runtime-2026-09-24/result.json)
includes commands, logs and selected source hashes. Chrome checks run
against the embedded dashboard and the normal TLS/Raft application. They require
token re-entry after refresh, reselect real files including an empty source, lose
the real server's successful resume response, and verify one explicit progress
read completes the same upload without a second resume or configuration activation.
A separate continuation then validates, dismisses an activation confirmation with
zero writes, and explicitly activates that original operation. Its two credential
resources reach committed and applied outcomes. The existing fresh-import
activation campaign also passes against the same rebuilt assets. These three
browser scenarios pass with Go 1.27 race detection (90.705 s) and Go 1.25 (76.911 s).
The final dashboard build passes 489 tests, type checks and lint. An inherited Node TLS override in
the first test invocation produced a warning that invalidated its JSON report;
the final harness removes that override and trusts the fixture certificate
explicitly. A later minimum-Go invocation exposed an ambiguous heading selector;
the fixture now selects the main landmark. Failed invocation logs are retained
alongside final passing evidence.

This workflow supports the shared versioned browser/CLI file profile. Ordinary SDK
`Freeze`/`FreezeResources` and unprofiled legacy operations have distinct byte
contracts; creating a new frozen instance is not recovery of the original identity.
Maximum-input measurements, the complete dashboard audit and private shipping
gates remain separately tracked. Native Windows, provider accounts, the million-monitor
campaign and the 24-hour endurance claim are not established by this work.

## CLI and SDK continuation

`cpractl apply --file-profile cpra.file.base.v1` uses `collection.FreezeProfile`
to bind the file-normalization profile at admission. Plain `apply` continues to
use unprofiled `collection.Freeze`; typed-resource and existing unprofiled
contracts are unchanged. The profile accepts at most 1,000 expanded sources,
64 MiB of cumulative raw input, 10,000 resources, 1 MiB per resource, a 16 MiB
document bound and 512 MiB of plaintext client staging. Callers can tighten
these limits. Unsupported profiles fail before input acquisition or API requests.

```sh
cpractl apply --file-profile cpra.file.base.v1 -f shared.yaml -f service.yaml -f empty.yaml
cpractl resume-upload operation/OPERATION_ID -f shared.yaml -f service.yaml -f empty.yaml -o json
cpractl get upload-attempt OPERATION_ID ATTEMPT_ID -o json
cpractl resume-upload operation/OPERATION_ID --attempt ATTEMPT_ID -o json
```

The [CLI guide](../cpractl-management.md#recover-an-incomplete-original-upload)
explains when the source-free final command is eligible. `resume-upload` calls
the SDK's `collection.Reselect`; its operation interface excludes collection
creation, validation, activation, cancellation and discard. The command first
reads the original operation. If its authoritative uploaded count is already
complete, it returns completion without consulting a possibly disappeared
attempt. Otherwise it reads a supplied attempt ID before acquiring source input.
Required sources are frozen privately before creating an attempt or uploading
a part; source fetching never inherits the CPRa client's credentials.

CLI input follows the explicit `-f` order and lexical directory expansion;
browser imports sort relative names by UTF-8 bytes. Recovery must reproduce the
original source order and boundaries, including empty files. On a new attempt,
all raw bytes, comments and unused text contribute to the original-source proof.
On a known uploading attempt, already staged raw bytes remain authoritative.
Selected files supply only missing bytes; same-length edits to a selected prefix
that is already staged are not compared with that retained prefix. The server
proves its complete assembled source inventory and normalized resources against
the original identity before appending any resource. A changed newly supplied
suffix cannot redefine the operation or bypass the proof.

Known `verifying`, `verified`, `transferring` and `completed` attempts can continue
without source files, and unused file arguments are not reopened. Existing work
is polled at least five seconds apart, without renewing expiry. A verified attempt
receives one request to start transfer; completion is confirmed from the original
operation. If the initial explicit attempt read reports `failed` with
`transfer_failed`, the helper permits one Resume request so the server can decide
whether the retained transfer can reconcile or continue. A failure observed
later in that invocation stops the helper. Other failed attempts do not receive
Resume; uncertain mutations are never automatically repeated.

Errors preserve the last verified original operation and known attempt metadata.
CLI reports expose only safe handles, counts, phase, expiry and failure categories,
including on partial failure. `complete` means upload completion, not validation,
activation or controller application. `get upload-attempt` is a read-only request
under the original owner and current collection-write authority. A lost attempt
creation reply without an ID cannot be rediscovered; a replacement may be refused
until expiry or restart discards that unknown attempt. This limitation is not
hidden by creating a replacement collection.

Recovery client staging is plaintext with restrictive permissions, bounded to
64 MiB of raw bytes and removed on completion, failure or cooperative cancellation.
Smaller source/staging quotas are honored. Forced process termination can leave
private temporary files; cleanup does not guarantee physical erasure. Server
attempt staging remains separately encrypted with a process-memory-only key.

The separate `collection.Resume` helper still requires the original live `Frozen`
and can continue through activation; it is not used by `resume-upload`. Validation
and activation after recovered upload require separate explicit operator actions.
CLI correctness/race and Go 1.25 checks have a separate private evidence directory,
`bin/verification/collection-reselection-cli-2026-09-24`. The subsequent
[native CLI checkpoint](../../bin/verification/collection-cli-reselection-2026-09-24/result.json)
records an actual CLI process kill after 256 of 300 resources committed, followed
by a graceful server restart, rejection of changed original input, and recovery
of only the 44 missing resources. Previously committed ciphertext remains exact.
A separate fixture loses real successful source-upload and transfer replies;
explicit continuation observes the original operation without repeating a
mutation or activating configuration.

That campaign reproduced a ten-second timeout in ordinary 256-row upload:
sequential submissions repeatedly paid the durable admission cost. Upload now
prepares bounded existing command envelopes using the configured command and
encoded-byte limits. New rows carry the existing format-14 prefix and authority
fences; a conflicting predecessor prevents dependent suffix admission. Retries
reuse original ciphertext. Each row still commits independently, synchronous
writes remain enabled, and an accepted prefix is retained after later failure.
The FSM owns its mutex for each bounded envelope; this qualification does not
establish monitoring latency SLOs during concurrent imports.

The final native CLI scenarios, three connected browser scenarios, and affected
runtime tests pass on Go 1.27 with race detection and Go 1.25. SDK and focused
upload checks pass both toolchains with default and `externaljobs` builds, plus
default/tagged release-toolchain race testing. The refreshed dashboard passes
489 tests, type checks, lint and embedded-asset verification. These records remain
distinct from the open scale, endurance, provider and complete shipping gates.
The full management package race invocation subsequently timed out at ten minutes
while the large notification-closure validation test was running. Its trace and
unchanged upload-source hashes are retained in the checkpoint; a complete
management race pass is not established.
