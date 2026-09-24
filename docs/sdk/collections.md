---
title: SDK candidate · Configuration collections
description: SDK candidate · Configuration collections for the reviewed CPRa source; see the version and availability notice.
cpra_scope: sdk
---

> **Unpublished SDK candidate:** this guide follows the source in this checkout. Confirm the connected server’s capabilities and release qualification before using candidate APIs. See [availability and source](../versions.md#go-sdk-and-approved-management-plan).

# Configuration collections

`collection` freezes files, directories, readers, URL inputs, or an incremental
typed resource source, and uses the public SDK's operation protocol to apply
them. It contains no CPRa server, provider driver, or controller dependency.

**Private integration status (2026-09-24):** the working server supports bounded
ephemeral `Preflight`/`Diff`, durable operation creation, encrypted upload,
asynchronous validation, path-only activation and retained execution results.
The normal TLS/Raft/controller entrypoint has been exercised through native CLI
Apply, Wait, bounded result pages and restart. `FreezeProfile` opts file inputs
into restart recovery through `Reselect`; ordinary `Resume` still requires the
original open `Frozen`. These are local candidate checks and do not identify a
published SDK or server release.

This package ships in the `github.com/ziad-hsn/cpra/sdk/go` module. After a
qualified SDK version is published, add that module to your application with
`go get github.com/ziad-hsn/cpra/sdk/go@'REPLACE_WITH_PUBLISHED_SDK_VERSION'`. Import the package
as `github.com/ziad-hsn/cpra/sdk/go/collection`; it has no separate version tag.
The parent [SDK README](module-overview.md) describes candidate versions and current
server API support.

The following excerpt belongs inside a function returning `error`. `ctx` is the
caller's context and `client` is an initialized `*cpra.Client`:

```go
frozen, err := collection.Freeze(ctx, []collection.Source{
    collection.File("shared/endpoints.yaml"),
    collection.File("services/"),
}, collection.Options{Recursive: true})
if err != nil {
    return err
}
defer frozen.Close()

result, err := collection.Apply(ctx, client.Operations, frozen)
// Keep result.OperationID even when err is non-nil. A lost response can leave
// committed progress. Do not create another operation blindly.
if err != nil {
    return err
}
if !result.Noop {
    result, err = collection.Wait(ctx, client.Operations, result)
    // A ready result can be partial or failed. Inspect
    // result.Operation.ExecutionResult.Summary.Outcome and its counts.
}
return err
```

`Freeze` finishes reading every input before `Apply` can send an activation
request. If the final file is malformed, this code returns before changing any
active resource. The deferred `Close` removes local staging when the function
returns. `Apply` returns an operation handle because server activation may
continue after the request; `collection.Wait` pins that original content identity
and waits for retained execution readiness. Canceling the wait preserves its
latest verified observation and sends no mutation. The helper returns one bounded
first page; it does not collect the complete result inventory.

Try [example_test.go](source/sdk/go/collection/example_test.go.txt) for complete programs that freeze either
manifest YAML or typed resources, validate references, and inspect an item without
contacting CPRa. From this package directory, run:

```sh
go test -run Example -v
```

The examples print `2 resources validated locally` and
`Monitor payments-http`. They establish local loading and validation only; the
server must still preflight permissions, live versions, capabilities, and
dependencies before activation.

`Freeze` reads each input once, validates all resource envelopes and built-in
driver shapes, rejects duplicate identities (including identical definitions),
and preserves source/document/item locations locally. Each freeze generates a new
random 32-byte key and commits to the raw input sources, exact serialized resource
objects, their order and declared count. The protocol is
`cpra.collection.hmac-sha256-json-bytes.v1`; its digest is an opaque keyed
commitment, not a reusable plain hash of a credential. Upload attribution contains
opaque `source.00000000000000000001` tokens, never local paths or source URLs.
The private key and source fingerprint are sent only in preparation, creation and preflight requests
to the configured authenticated CPRa server. They are not written to local staging.

Mutating an input afterward never changes that frozen collection. Readback checks
each staged resource against its commitment before returning it. `Close` removes
the private plaintext staging and clears the key, fingerprint and admission-ticket buffers owned by
`Frozen`; Go runtime and HTTP encoding copies cannot be guaranteed erased. Use the
returned pointer without copying `Frozen`, and do not log resource payloads or
serialize private request fields. The caller owns and closes supplied readers.

Directory traversal selects `.yaml`, `.yml`, and `.json` files in lexical order;
recursion is explicit. Overlapping file paths are deduplicated. Directory symlinks
are never followed. Standalone resource objects, arrays, Kubernetes-style `List`
objects, multi-document YAML, consecutive JSON documents (including JSON Lines),
and manifest envelopes are supported.
Shared endpoint/group files need no `monitors` field. YAML block monitor and
`List.items` sequences, and their JSON counterparts, decode one resource at a
time, including when the complete collection exceeds the document-memory limit.
YAML aliases are rejected; render explicit values before importing them.

Manifest normalization retains deterministic name-derived IDs, explicit `false`,
interval strings, thresholds, inline notifications, ordered groups,
and all 33 built-in driver configurations. Known driver configuration field names
are converted to the v2 camelCase names. User map keys such as HTTP headers are
unchanged. Driver executors and provider credentials are never invoked by parsing.
Manifest zero retries inherit positive parent retries, and a zero unhealthy
threshold inherits positive `max_failures`, matching the existing manifest
loader. Explicit zero values in v2 resources remain zero.

For an application that supplies its own encrypted staging, `Decode` shares the
same YAML/JSON parser without opening a file or URL or creating a plaintext
temporary file:

```go
err := collection.Decode(ctx, inputReader, collection.DecodeOptions{
    SourceName: "service configuration",
    MaxBytes:   64 << 20,
}, func(item collection.Item) error {
    return encryptedStage.Add(ctx, item.Resource)
})
```

Here `encryptedStage` is application-owned staging. The callback must not apply
active changes: a malformed final item can fail after earlier callbacks ran.
Activate only after `Decode` succeeds and the complete staged union passes
reference, permission, capability and version validation. The identity index is
bounded by `MaxResources`; decoded resource bodies remain individually bounded.
`Decode` preserves callback errors and respects cancellation during input reads.
Its item digest is a local SHA-256 parsing aid, with no remote inventory position
or resume key. It cannot be substituted for a frozen collection in this protocol.
Ordinary clients can continue using `Freeze` and `Apply` above.

`FreezeResources` accepts a `ResourceSource.Next(context.Context)` iterator;
`Slice` adapts a small `[]api.Resource`. `Range` and `Item` read frozen resources
without loading their complete payloads into memory. The offset/size index is
retained; duplicate detection temporarily retains the identity index during
freezing. `ValidateReferences` can validate against only the staged union, or
resolve omitted dependencies through a supplied authorized live resolver. Its
observed versions are diagnostic and do not replace server-side conditional
validation.

The URL source client has no connection to the authenticated CPRa client. HTTPS
is required unless `AllowHTTP` is explicitly set. URL user information and
HTTPS-to-HTTP redirects are rejected; redirects are bounded to three and clear
request headers. Query strings are omitted from source attribution and errors.
Custom source transports are trusted caller code and must not inject credentials.
Decompressed source bytes share the staging quota and source timeout, and count
toward the separate cumulative source-byte limit.

Default limits are 1 GiB of simultaneous plaintext staging and 1 GiB of cumulative
raw input, 1 MiB per resource,
16 MiB for a non-streamed document or envelope metadata, two million resources,
and a 30-second URL request timeout. The staging quota includes both the frozen
source currently being decoded and emitted resource records. Limits are explicit;
increase the staging quota for a measured large collection. Unix directories and
files use 0700/0600; Windows directories receive an owner-only inheritable ACL at
creation. Arbitrary caller readers must honor their own cancellation if a read
can block indefinitely.

`MaxSourceBytes` defaults to `MaxStagingBytes` and includes all sources even after
their temporary copies are removed. Typed resource streams count serialized
objects plus one LF per object. The protocol caps sources at one million and
resources at ten million; the smaller configured `MaxResources` still applies.
Canceled input and quota failures remove the complete staging directory.

`Preflight` and `Diff` make an ephemeral validation request with at most 10,000
items and 4 MiB of encoded data. Larger ephemeral dry-runs fail locally without a server call;
the server's future ephemeral streaming protocol is not fabricated here.
On a server implementing durable collection operations, `Apply` supports larger
collections by creating inactive encrypted server staging,
uploading chunks bounded to 256 items/4 MiB, validating the entire staged union,
and only then requesting activation. `Validate` admits the original request and
returns an operation receipt; `WaitValidation` polls only the read endpoint until
a sealed result or a distinct terminal error is available. Pending validation
never becomes a fabricated negative verdict. `Result.Validation` contains the
immutable summary; `Result.Preflight` is reserved for the separate ephemeral
contract. The helper reads only the first bounded result page and checks its
returned rows against the original frozen input. It relies on the sealed summary
for the complete original collection, and does not fetch or claim to inspect all
remaining result rows. Inspect them lazily with `Operations.ValidationItems`.

The server must enforce authorization,
capabilities, live dependency versions, dependency ordering, and per-resource CAS.

Before its first Create, `Apply` calls `Operations.Prepare` with the frozen
identity. Preparation returns a private, expiring admission ticket without
uploading or activating resources. `Apply` retains that ticket in memory before
attempting creation. Every subsequent Apply with the same Frozen reuses the
original ticket, even after an uncertain Create response, explicit rejection or
expiry. The server reconciles a consumed live ticket to the original operation;
an expired ticket or one from a restored server epoch cannot allocate a new
operation. Starting a new Freeze is an explicit new attempt with a new identity.
Ticket values never enter local staging and ordinary Go formatting omits them.
Go runtime and HTTP encoding copies cannot be guaranteed erased.

Only one Apply or Resume workflow can use a Frozen at a time. A competing call
returns `ErrApplyInProgress` before sending a request. Read-only inspection can
overlap. Close does not wait for an in-flight network request and cannot undo a
request already sent; it prevents further access to the local staged data. Keep
the Frozen open while reconciling an uncertain result. The lower-level
`Operations.Prepare` and `Operations.Create` methods leave ticket ownership to
the caller and never renew or retry it automatically.

`Resume` requires the original, still-open `Frozen` instance, including its private
key and staged resource bytes. Reading the same files again creates a different
identity even if no bytes changed. Ordinary unprofiled operations have no
cross-process resume file. Keep the instance alive until you finish resuming or
consciously stop retaining its private input. The explicit file profile below
supports a separate upload-recovery path without exporting the original key.

`Resume` first reads the original server-issued operation and compares the frozen
content identity, protocol format and declared item count. It cannot apply changed
input under an old operation. Creation, upload and validation receipts must also
match these fields before the next mutation; missing counts are not guessed.
A validating or validated operation resumes by reading its original verdict,
without submitting another validation request. A rejected verdict remains a
rejection. An intentional new validation requires a new operation. Replayed
uploads preserve item identities and digests; the server must accept identical
replay and reject changed content. Applying and other terminal operations are returned
without reactivation. Partial failures retain the handle and progress; there is
no implicit rollback, pruning, cancellation, or mutation retry. Cancelling a wait
does not cancel its operation. Use `client.Operations.Cancel` explicitly.

An uncertain activation response permits one GET of the original operation.
The helper accepts that observation only when its content identity matches and
the server reports applying work or supported execution-result availability.
It retains an ambiguous error if admission cannot be established, and never
repeats a POST. A successful HTTP status without admission evidence is also
unconfirmed. `Apply` returns on admission; use `collection.Wait` explicitly to
await execution and preserve partial counts on interruption.

## File profiles and recovery after restart

Select a normalization profile when creating a file collection that must support
recovery after the client exits:

```go
frozen, err := collection.FreezeProfile(ctx, []collection.Source{
    collection.File("shared/endpoints.yaml"),
    collection.File("services/payments.yaml"),
    collection.File("services/empty.yaml"),
}, collection.FileNormalizationProfile, collection.Options{})
if err != nil {
    return err
}
defer frozen.Close()
result, err := collection.Apply(ctx, client.Operations, frozen)
// Retain result.OperationID on failure as well as success.
```

`FileNormalizationProfile` is `cpra.file.base.v1`. It permits at most 1,000
expanded sources, 64 MiB total raw input, 10,000 resources, 1 MiB per resource,
16 MiB of document metadata and 512 MiB of local plaintext staging. Options can
tighten these limits. Unsupported profiles and enlarged limits fail before
reading sources. The profile is part of the immutable frozen identity;
`Frozen.NormalizationProfile` reports it. Existing `Freeze` and `FreezeResources`
operations cannot be relabeled afterward.

The explicit source order and lexical directory expansion determine the source
inventory. Preserve every source boundary, including empty files. Raw comments
and whitespace participate in its commitment. Do not replace the original
files with a reformatted export or a concatenated document. Browser imports
order files by their relative names; when recovering one through Go or the CLI,
supply that same original order.

After a restart, use the original operation ID with the original inputs:

```go
recovery, err := collection.Reselect(ctx, client.Operations, operationID,
    []collection.Source{
        collection.File("shared/endpoints.yaml"),
        collection.File("services/payments.yaml"),
        collection.File("services/empty.yaml"),
    }, collection.ReselectionOptions{AttemptID: knownAttemptID})
// Preserve recovery.OperationID and, when present, recovery.Attempt.ID.
// recovery.Complete means the original upload is full, not that it was applied.
```

Use an empty `knownAttemptID` for a new disposable attempt. `Reselect` first reads
the original operation and, if supplied, the known attempt. It freezes all
required raw sources privately before creating an attempt or sending a source
part. That spool is limited to 64 MiB and 1,000 expanded sources; lower
`Sources.MaxSourceBytes` or `Sources.MaxStagingBytes` values tighten it. Parts
are at most 1 MiB. URLs use the separate unauthenticated source client and never
inherit the CPRa bearer token. All raw content, including comments, is sent to
the authenticated CPRa origin. Errors and progress omit paths and payloads.

A known partial attempt retains the raw bytes already acknowledged by the
server. Newly selected files supply only its missing bytes. Same-length edits
to an already-staged prefix are therefore not compared against that prefix in
the new selection. The server verifies its complete assembled input against the
original collection identity before appending any resource suffix. It never
uses these edits to replace the original desired configuration.

The helper stops on an uncertain mutation response. An explicit later call with
the known attempt ID reads its current progress before proceeding. A known
attempt initially reporting `failed` with `transfer_failed` may receive one
explicit resume request; the server determines whether retained transfer state
can continue. A failure first observed during the current invocation is not
retried. If an attempt-creation response is lost before its ID is received,
this API cannot rediscover that ID; the disposable attempt must expire or be
removed by a server restart before starting another.

Verification and transfer can continue without source files after upload has
finished. A fully uploaded original operation returns `Complete` without
looking up an expired or disappeared attempt. Polling uses GET requests at least
five seconds apart and respects longer server retry intervals, context
cancellation and the attempt's fixed expiry. Cancellation does not cancel the
server operation or remove acknowledged input.

`Reselect` only finishes the original upload. Validation, review and activation
remain explicit subsequent operations; the dashboard offers those controls on
the original operation page. The helper does not create a replacement
collection, activate resources or invoke providers. See
[example_reselection_test.go](source/sdk/go/collection/example_reselection_test.go.txt) for a runnable local
profile example and a compiled remote-recovery example. Ordinary return paths
remove private staging; forced process termination cannot run Go cleanup.

For ordinary `Resume`, an older server may omit `Operation.Uploaded`. Reads
preserve that absence, but resuming staging, uploading, pending, validating,
validated or rejected work returns
`collection.ErrProgressUnavailable` before any upload, validation or activation.
An absent counter is not treated as zero or proof that the upload is complete.
Applying and terminal operations can still be observed without the count.
`Reselect` requires an explicit uploaded count in every original-operation
observation and returns `ErrReselectionObservation` when that count is absent.

The server v2 management/operation contract remains a prerequisite. Package and
HTTP fixture tests establish loader/client behavior, not production server CAS,
encrypted server staging, real provider effects, or million-monitor qualification.

<!-- Adapted from sdk/go/collection/README.md. -->
