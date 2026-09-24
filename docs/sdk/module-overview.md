---
title: SDK candidate · CPRa Go SDK
description: SDK candidate · CPRa Go SDK for the reviewed CPRa source; see the version and availability notice.
cpra_scope: sdk
---

> **Unpublished SDK candidate:** this guide follows the source in this checkout. Confirm the connected server’s capabilities and release qualification before using candidate APIs. See [availability and source](../versions.md#go-sdk-and-approved-management-plan).

# CPRa Go SDK

This independently versioned module contains the typed Go client for CPRa.
It supports Go 1.25 and does not depend on the CPRa application module, controller,
Ark, Raft, provider SDKs, or worker execution library.

**Private integration status (2026-09-24):** this SDK and the current CPRa working
branch implement v2 resource management, incident/monitor controls, audited action
review, operation reads and observations. Tests exercise these routes through the
public SDK and real local TLS/Raft servers; selected dashboard flows also run
through normal application startup and restart. Collection preflight, durable
upload, validation, public activation and retained execution results are connected
through the shared Apply helper and native CLI. Original-file reselection has a
connected SDK/server path and a guided browser flow for the versioned file profile.
The shared `collection.Reselect` helper and native `cpractl resume-upload`
support restarted clients for that profile. Additional source profiles remain
separate work.
External-worker server integration remains unfinished; SDK transport fixtures
for that protocol do not establish server support.

Discover the connected server's capabilities
before selecting v2 operations; this working-branch evidence does not establish
compatibility with an earlier published server. Both SDK modules remain
unpublished, and candidates stay private until all agreed release gates pass.
The planned first prerelease is `v0.1.0-rc.1`; that name does not establish that
a downloadable tag exists.

## Install in your application

Use Go 1.25 or newer. The module path is the import path; the package name at
its root is `cpra`. This is a library, so add it with `go get` inside your own
module. It does not install the CPRa server or `cpractl`.

After a version has been published and qualified, replace the placeholder with
that exact version:

```sh
mkdir cpra-integration
cd cpra-integration
go mod init example.com/team/cpra-integration
GOWORK=off go get github.com/ziad-hsn/cpra/sdk/go@'REPLACE_WITH_PUBLISHED_SDK_VERSION'
```

The planned `v0.1.0-rc.1` is a candidate version, not evidence that the download
exists. To inspect and test this checkout before publication, change into
`sdk/go` and run `GOWORK=off go test ./...`. The complete integration examples
use the repository's explicit development workspace; do not copy a local
`replace` directive into a published application's `go.mod`.

| Import | Use it for |
| --- | --- |
| `github.com/ziad-hsn/cpra/sdk/go` | V2 resource services, request policy, errors, and operations |
| `github.com/ziad-hsn/cpra/sdk/go/api` | Resource models, driver settings, local validation |
| `github.com/ziad-hsn/cpra/sdk/go/collection` | Files, URLs, typed collections, preflight, and apply |
| `github.com/ziad-hsn/cpra/sdk/go/worker` | Separate module for tagged external-worker execution |

The module includes its [MIT license](source/sdk/go/LICENSE.txt). Source and package documentation
ship in its own module archive; importing it does not require cloning the server
repository. SDK versions and HTTP API versions are independent. A `v0.x` release
can contain breaking SDK changes; read its release notes and pin a version.

## Clients and resource types

Import `github.com/ziad-hsn/cpra/sdk/go` as `cpra` and its `api` package for resource
types. `cpra.New` accepts an explicit base URL, token or token-source callback,
request timeout, trust roots, and optional caller-owned HTTP client. Methods accept
contexts and return a typed `Response[T]` with `Data`, resource version, request ID,
and operation ID. The API version is `cpra.io/v2`, independent of the SDK module
version.

Operation progress uses optional pointers: `Operation.Uploaded`, `Committed`,
and `Applied` are `*int64`; `Operation.Validated` and each `ApplyResult`'s
`Committed`/`Applied` are `*bool`. Check for `nil` before reading them. Nil means
the server did not report the observation, while a pointer to zero or false
preserves that explicit observation. These optional fields do not accept JSON
null. Re-encoding a typed response retains this distinction without retaining
the original HTTP body.

The following complete program creates an HTTP monitor, then disables it using
the version returned by the create. It requires a compatible v2 server and a
principal authorized to create and update monitors. Set `CPRA_URL` to its HTTPS
origin and provide `CPRA_TOKEN` through your process environment.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "time"

    cpra "github.com/ziad-hsn/cpra/sdk/go"
    "github.com/ziad-hsn/cpra/sdk/go/api"
)

func main() {
    token := os.Getenv("CPRA_TOKEN")
    if token == "" {
        log.Fatal("set CPRA_TOKEN for an authorized management principal")
    }
    client, err := cpra.New(cpra.Config{
        BaseURL: os.Getenv("CPRA_URL"),
        AuthToken: token,
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.CloseIdleConnections()
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()

    driver, err := api.Driver("check", "http", api.PulseHTTPConfig{
        URL: api.Pointer("https://service.example.net/health"),
    })
    if err != nil {
        log.Fatal(err)
    }
    created, err := client.Monitors.Create(ctx, api.Monitor{
        APIVersion: api.APIVersion,
        Kind: "Monitor",
        Metadata: api.Metadata{ID: "payments-http", Name: api.Pointer("Payments HTTP")},
        Spec: api.MonitorSpec{Check: api.CheckSpec{
            Driver: driver, Interval: "60s", Timeout: "5s",
        }},
    })
    if err != nil {
        log.Fatal(err)
    }
    disabled, err := client.Monitors.Patch(ctx, created.Data.Metadata.ID,
        created.ResourceVersion, api.MergePatch(`{"spec":{"enabled":false}}`))
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Disabled monitor:", disabled.Data.Metadata.ID)
}
```

Read the program in this order:

1. `cpra.New` validates connection settings. It does not connect to providers or
   start a controller. `defer` closes idle connections when the program returns.
2. The context bounds the whole operation. Each HTTP request also has the SDK's
   default ten-second timeout.
3. `api.Driver` validates configuration data locally. It does not call the health
   endpoint. `api.Pointer` supplies a value for an optional wire field.
4. `Create` sends the desired resource and a create-only precondition. The ID is
   stable across retries and renames; choose it from your service identity.
5. `Patch` changes only the enabled setting and sends the observed resource
   version. It fails on a concurrent edit instead of replacing that edit. Pass the raw
   `metadata.resourceVersion` or response `ResourceVersion`; the SDK quotes it
   as a strong HTTP `If-Match` tag. Do not supply quotes, a weak tag, or a tag list.

The first successful run prints `Disabled monitor: payments-http`. A second run
conflicts on creation because that ID already exists. An integration should
reconcile the existing resource and its ownership before deciding to update it;
it should not change the ID merely to avoid a conflict. The simple `log.Fatal`
branches above report errors; the runnable examples show recovery decisions.
See the executable local HTTP fixture in [example_test.go](source/sdk/go/example_test.go.txt) to
exercise returned data and versions without a v2 server.

All 33 built-in driver configurations have concrete public structs. `Driver`
validates a configuration for the `check`, `recovery`, or `notification` category.
The driver envelope preserves raw configuration received from future servers.
Unknown variants can be inspected, but mutation helpers reject them rather than
silently dropping their fields. Default driver executability remains a **server
capability**, independent of the SDK's ability to represent built-in configuration.

Use `Monitors`, `NotificationEndpoints`, `Recipients`, `NotificationGroups`, and `Credentials`
for CRUD and merge patch. Incidents expose `Acknowledge`, `Dismiss`, and `Reopen`;
monitors expose `Snooze`, `Unsnooze`, `Disable`, `Enable`, and guarded `Recover`.
There is no check-now operation. `Actions.Review` submits the explicit audited
review request; it does not prove an external operation completed.

Replacement, patching, deletion, and controls require explicit versions. Creation
sends `If-None-Match: *`. The SDK never reads a newer version to resolve a conflict
on your behalf. For merge patch, arrays replace and `null` removes optional values;
raw patch bytes retain the difference between omission, false, zero, and null.

## Observation and batches

`State`, `History`, `Metrics`, `RuntimeConfig`, `Capabilities`, `Version`, `Explain`,
`Ready`, and `Live` expose the corresponding observation/discovery API. `SLO.Get`,
`Queues.List`, `Pools.List`, and `Systems.List` preserve explicit availability and
millisecond units. `Prometheus(ctx, writer)` streams `/metrics` with a byte bound.

Resource services expose `List` and lazy `Iterate`. Pages default to 100 items and
cannot exceed 500. An iterator holds one page; it never builds a fleet-sized list.

The `collection` package freezes multiple files, directories, readers, and URLs,
normalizes monitor manifests, validates the complete local collection,
and stages bounded uploads. `collection.Apply(ctx, client.Operations, frozen)`
requests activation only after a matching sealed validation verdict.
It returns on activation admission. An uncertain activation reply permits one
read of the original content-bound operation; it never repeats the activation
POST or allocates a replacement operation. If that read cannot establish
admission, the error and original handle remain available to the caller.

Use `collection.Wait(ctx, client.Operations, result)` explicitly after `Apply`
to await the retained execution result. It pins the original operation ID,
identity format, content digest and item count. You may close `Frozen` before
waiting. The returned `Result` preserves the latest verified partial counts on
read failure or cancellation; a ready partial or failed execution is returned
with its immutable summary and no transport error. Check that summary's
`Outcome` before treating the application as successful. The helper reads only
the bounded first result page; use `ExecutionItems` for further pages.
`Operations.Validate` returns the original `Operation` from a 202 admission
response; it does not return a completed preflight. `Operations.WaitValidation`
only polls GET validation and returns one bounded first page plus the immutable
summary. `Operations.Validation` and lazy `ValidationItems` expose retained
results (100 items by default, at most 500 and 4 MiB per response).

A pending result is a `validationPending` problem, distinct from a sealed
`valid: false` verdict. Interrupted, canceled, expired, and unavailable results
remain distinct errors. Waiting honors a server Retry-After up to 24 hours; an
unsupported interval fails explicitly instead of polling early. Cancellation
retains the original operation handle and sends no mutation.

`Operations` also exposes individual prepare/create/upload/activate/get/cancel/wait
steps. General `Wait` observes activation completion; `validated` alone is not
completion. Rejected or invalidated operations are terminal observations.

Execution-result reads use the existing operation detail endpoint, without an
activation body. `Operations.ExecutionResult(ctx, id, ExecutionResultPageOptions{})`
reads one page; `ExecutionItems` lazily reads immutable pages while checking the
original collection identity and complete sealed summary. `WaitExecutionResult`
waits for explicit `executionResult.state == "ready"`, not a terminal parent
state. A canceled parent can still have pending accepted children or result
publication. Canceling this wait stops only the reader and retains the original
operation handle. Unknown or absent availability returns
`ErrExecutionResultUnsupported`; explicit result expiry returns
`ErrExecutionResultExpired`. Typed HTTP problems, including missing operations,
remain distinct. `Activate` uses a path-only POST with no request body and
requires a matching HTTP 200 admission receipt.

`catalogDecision` records the original conditional catalog decision independently
of `childDisposition`. An accepted mutation remains committed if its child later
reports `projection_failed` or `superseded`. An unchanged input creates no child;
its `applied` observation is absent. Result pages retain original input and plan
ordinals, opaque source coordinates, incarnation/version identities, and sealed
aggregate counts. Unknown observational strings remain readable and never grant
mutation authority. Each response is bounded to 500 items and 4 MiB, further
limited by `Config.MaxResponseBytes`.

`Apply` obtains one
private admission ticket and retains it in the original `Frozen` before Create.
Repeating Apply reuses that ticket, including after an uncertain reply or expiry;
it never silently creates a new attempt. Overlapping Apply/Resume calls on the
same Frozen return `collection.ErrApplyInProgress`. Preserve an operation ID and
the original open `Frozen` instance for `Resume`; the digest alone cannot resume.
Every freeze creates a new private key and identity, even for unchanged input.
Ordinary Freeze/Apply cannot resume across processes. Use `FreezeProfile` at
creation to enable the separate original-file recovery flow below. Resource paths
stay client-local; ordinary upload
requests use opaque source tokens. See the [collection guide](collections.md)
for current server support, limits and private staging lifetime.
Cancelling a waiting context never silently cancels the server operation.

`collection.FreezeProfile(ctx, sources, collection.FileNormalizationProfile,
options)` records `cpra.file.base.v1` when the original operation is created.
`collection.Reselect` later freezes selected raw files and completes only the
missing upload under the server-held original identity. It does not validate or
activate configuration. Keep the returned operation and attempt handles even on
failure; a later explicit call reads known progress before proceeding. A known
partial attempt retains its already-staged raw bytes, and selected files supply
the missing suffix. See the [collection guide](collections.md#file-profiles-and-recovery-after-restart)
for limits, source ordering, uncertain responses and the retained-prefix boundary.

The SDK also provides the **low-level original-file reselection contract** tested
against the working-branch TLS server. Normal managed web startup enables this
path; custom server construction must explicitly configure its manager. Discover
support before use, including when connecting to an older published server. The original
operation must have recorded `cpra.file.base.v1`, and the authenticated caller must
remain its original owner. An attempt is disposable and disappears after a server
restart. It never replaces the original operation or activates configuration.

| SDK method | Explicit step |
| --- | --- |
| `Operations.CreateReselection` | Create an owned attempt using source count and the original normalization profile. |
| `Operations.UploadReselectionSource` | Send one `ReselectionSourcePart` with one-based source, raw byte offset, end flag, and at most 1 MiB of bytes. |
| `Operations.VerifyReselection` | Admit asynchronous proof of every original source and normalized resource; HTTP 202 is not a successful proof. |
| `Operations.GetReselection` | Read bounded progress without extending either expiry. |
| `Operations.ResumeReselection` | Admit asynchronous upload of the verified missing suffix to the original operation. |
| `Operations.DiscardReselection` | Remove only the disposable attempt; already committed input remains in the original operation. |

This path sends complete selected file bytes, including comments and unused text,
to the configured authenticated origin. It sends no filename, local path, or URL.
`ReselectionSourcePart.Data` is a binary body, so no JSON or base64 wrapping is
needed. An empty end part closes an empty source; an empty non-end part is invalid.
After all sources are complete, `nextSource` and `nextOffset` are both zero.
The client bounds attempt responses to 16 KiB, preserves the original operation
handle on ambiguous outcomes, and never automatically repeats a mutation.
Reconcile progress after an uncertain reply before deciding whether an identical
last-part retry is allowed. No method exposes the original inventory key or
private source fingerprint, renews expiry, or silently starts the next step.

## Errors, transport, and ownership

Use `errors.Is` with `ErrConflict`, `ErrExpired`, `ErrUnavailable`, `ErrUnauthorized`,
`ErrInvalid`, and `ErrResponseTooLarge`. Use `errors.As` for `*cpra.Error` to inspect
RFC 9457 details and field errors. Its printable message deliberately omits raw
server detail. `ErrAmbiguous` indicates an outcome that cannot be determined;
inspect the operation handle rather than issuing a fresh mutation.

Authenticated v2 connections require HTTPS unless the caller explicitly permits
HTTP for the configured origin. Redirects are disabled, including when supplying
an HTTP client. Ordinary calls default to ten seconds; context deadlines can be
shorter. Responses are bounded to 64 MiB, error decoding to 64 KiB, and resources
to 1 MiB. Limits detect overflow rather than returning truncated success.

Automatic retries are disabled by default. `ReadAttempts` can opt into at most
three GET attempts for transient HTTP statuses. The SDK never retries mutations.
A supplied transport can itself have retry behavior; its owner must ensure that
behavior does not repeat side effects. Clients install no signal handlers, start
no background pollers, send no telemetry, and never exit the process.

## Optional external jobs

Custom-job source is compiled only with `-tags externaljobs`. The tagged client
adds `JobTypes()`, worker observations, and a separate `NewWorkerClient` with a
required authentication source. The server must independently be built with the
tag, have its runtime feature enabled, and authorize the worker's scope. The SDK
is not an authorization boundary.

On the private integration branch, `JobTypes()` list/create/get/replace/delete
methods have real TLS/Raft server tests, including retained operation receipts
and restart. The server runtime setting is `external_jobs.enabled: true`, which
requires authenticated management and Raft. Creation uses an absence precondition;
replacement/deletion use the resource version returned by a read. Registration
stores a declarative contract; it does not start a handler. Scoped worker grants
are provisioned through [stopped local administration](../worker-authentication.md).
Poll/start/result integration remains unfinished, so the worker methods are not
yet qualified against a completed server.

The worker runner lives in its own module:
`github.com/ziad-hsn/cpra/sdk/go/worker`. It is absent from this module's dependency
graph. Its source files all require the same build tag; its separate documentation
covers handler registration, isolated provider credentials, encrypted storage,
crash behavior, and shutdown. Build tags exclude compiled functionality, not source
files from public module archives. No untagged custom-job method returns a disabled
stub.

## Generation and verification

From the repository root, run `GO=/path/to/go python3 tools/sdkgen/generate.py
--check`. The pinned local generator is `oapi-codegen` 2.8.0 with runtime 1.6.0.
OpenAPI inputs live under `api/openapi`; generated public types, internal transport,
and embedded schema have independent default and tagged variants. `generation.json`
records hashes. Generation neither needs a platform account nor imports the server.

Run `go test ./...` and `go test -tags externaljobs ./...` within this module. The
nested worker module requires its own test invocation; root `go test ./...` does
not cover either nested module. Contract tests cover every inventoried v2 operation
against an HTTP fixture and test safety boundaries. Repository-level tests exercise the SDK against the current server.
External-worker server qualification, downloaded published modules, and release signatures remain distinct
gates.

## Read package documentation

Each public package has an overview and testable Go examples. Use these commands
from a module that requires the SDK, or from this module's checkout:

```sh
go doc github.com/ziad-hsn/cpra/sdk/go
go doc github.com/ziad-hsn/cpra/sdk/go/api.Driver
go doc github.com/ziad-hsn/cpra/sdk/go/collection.Freeze
go test -run Example ./...
```

Go examples with an `Output:` comment run during tests; examples without one
compile but do not execute their illustrated network calls. The comments and
examples follow the [Go documentation format](https://go.dev/doc/comment) and
[testable example convention](https://go.dev/blog/examples). After publication,
pkg.go.dev reads these files from the downloaded module version. Its presence is
not a claim that CPRa's pending server contracts have passed qualification.

Maintainers publishing from the complete repository checkout must follow the
[publishing guide](publishing.md), including the separate nested
module tags, downloaded-consumer checks, and server compatibility gates. That
guide lives outside this module archive; the installation, client behavior, and
package documentation above are included in the downloaded module itself.

<!-- Adapted from sdk/go/README.md. -->
