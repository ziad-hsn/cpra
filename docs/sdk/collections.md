---
title: SDK candidate · Configuration collections
description: SDK candidate · Configuration collections for the reviewed CPRa source; see the version and availability notice.
cpra_scope: sdk
---

> **Unpublished SDK candidate:** this package guide reflects the 13 September source snapshot. See [availability and source](../versions.md#go-sdk-and-approved-management-plan) before running candidate commands.

# Configuration collections

`collection` freezes files, directories, readers, URL inputs, or an incremental
typed resource source, and uses the public SDK's operation protocol to apply
them. It contains no CPRa server, provider driver, or controller dependency.

This package ships in the `github.com/ziad-hsn/cpra/sdk/go` module. After a
qualified SDK version is published, add that module to your application with
`go get github.com/ziad-hsn/cpra/sdk/go@'REPLACE_WITH_PUBLISHED_SDK_VERSION'`. Import the package
as `github.com/ziad-hsn/cpra/sdk/go/collection`; it has no separate version tag.
The parent [SDK README](module-overview.md) describes candidate versions and current
server compatibility.

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
    _, err = client.Operations.Wait(ctx, result.OperationID)
}
return err
```

`Freeze` finishes reading every input before `Apply` can send an activation
request. If the final file is malformed, this code returns before changing any
active resource. The deferred `Close` removes local staging when the function
returns. `Apply` returns an operation handle because server activation may
continue after the request; `Wait` observes that original operation.

Try [example_test.go](source/sdk/go/collection/example_test.go.txt) for complete programs that freeze either
legacy YAML or typed resources, validate references, and inspect an item without
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
and preserves content identities and source/document/item locations. Mutating an
input afterward never changes that frozen collection. `Close` removes its private
plaintext staging. The caller owns and closes supplied readers.

Directory traversal selects `.yaml`, `.yml`, and `.json` files in lexical order;
recursion is explicit. Overlapping file paths are deduplicated. Directory symlinks
are never followed. Standalone resource objects, arrays, Kubernetes-style `List`
objects, multi-document YAML, consecutive JSON documents (including JSON Lines),
and existing manifest envelopes are supported.
Shared endpoint/group files need no `monitors` field. YAML block monitor and
`List.items` sequences, and their JSON counterparts, decode one resource at a
time, including when the complete collection exceeds the document-memory limit.
YAML aliases are rejected; render explicit values before importing them.

Legacy conversion retains deterministic name-derived IDs, explicit `false` and
zero values, interval strings, thresholds, inline notifications, ordered groups,
and all 33 built-in driver configurations. Known driver configuration field names
are converted to the v2 camelCase names. User map keys such as HTTP headers are
unchanged. Driver executors and provider credentials are never invoked by parsing.

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
Decompressed source bytes share the staging quota and the source request timeout.

Default limits are 1 GiB of simultaneous plaintext staging, 1 MiB per resource,
16 MiB for a non-streamed document or envelope metadata, two million resources,
and a 30-second URL request timeout. The staging quota includes both the frozen
source currently being decoded and emitted resource records. Limits are explicit;
increase the staging quota for a measured large collection. Unix directories and
files use 0700/0600; Windows directories receive an owner-only inheritable ACL at
creation. Arbitrary caller readers must honor their own cancellation if a read
can block indefinitely.

`Preflight` and `Diff` make an ephemeral validation request with at most 4 MiB of
encoded data. Larger ephemeral dry-runs fail locally without a server call;
the server's future ephemeral streaming protocol is not fabricated here.
`Apply` supports larger collections by creating inactive encrypted server staging,
uploading chunks bounded to 256 items/4 MiB, validating the entire staged union,
and only then requesting activation. The server must enforce authorization,
capabilities, live dependency versions, dependency ordering, and per-resource CAS.

`Resume` first reads the original server-issued operation and compares the frozen
content identity. It cannot apply changed input under an old operation. Replayed
uploads preserve item identities and digests; the server must accept identical
replay and reject changed content. Applying and terminal operations are returned
without reactivation. Partial failures retain the handle and progress; there is
no implicit rollback, pruning, cancellation, or mutation retry. Cancelling a wait
does not cancel its operation. Use `client.Operations.Cancel` explicitly.

The server v2 management/operation contract remains a prerequisite. Package and
HTTP fixture tests establish loader/client behavior, not production server CAS,
encrypted server staging, real provider effects, or million-monitor qualification.

<!-- Imported from sdk/go/collection/README.md; preserve candidate scope and reconcile with original before regenerating. -->
