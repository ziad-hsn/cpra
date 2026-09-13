---
title: SDK candidate · CPRa Go SDK
description: SDK candidate · CPRa Go SDK for the reviewed CPRa source; see the version and availability notice.
cpra_scope: sdk
---

> **Unpublished SDK candidate:** this package guide reflects the 13 September source snapshot. See [availability and source](../versions.md#go-sdk-and-approved-management-plan) before running candidate commands.

# CPRa Go SDK

This independently versioned module contains the typed Go client for CPRa.
It supports Go 1.25 and does not depend on the CPRa application module, controller,
Ark, Raft, provider SDKs, or worker execution library.

**Release status:** the management v2 contract is implemented in this SDK as a
reviewable draft. The corresponding server API is not implemented in the current
CPRa source. The transport contract tests do not establish server compatibility.
Use `legacy` for the existing, read-only v1 server. Both prerelease and stable SDK
publication require the corresponding server gates to pass. Do not claim
management operations work against that server before qualification.
The planned first prerelease is `v0.1.0-rc.1`; this document does not claim that tag
is already published.

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
| `github.com/ziad-hsn/cpra/sdk/go/legacy` | Existing read-only v1 servers |
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
   version. It fails on a concurrent edit instead of replacing that edit.

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

Use `Monitors`, `NotificationEndpoints`, `NotificationGroups`, and `Credentials`
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
normalizes supported legacy manifests, validates the complete local collection,
and stages bounded uploads. `collection.Apply(ctx, client.Operations, frozen)`
activates only after complete preflight. `Operations` also exposes individual
create/upload/validate/activate/get/cancel/wait steps. Preserve an operation ID and
input content digest for `Resume`; changing a file creates a different operation.
Cancelling a waiting context never silently cancels the server operation.

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

The worker runner lives in its own module:
`github.com/ziad-hsn/cpra/sdk/go/worker`. It is absent from this module's dependency
graph. Its source files all require the same build tag; its separate documentation
covers handler registration, isolated provider credentials, encrypted storage,
crash behavior, and shutdown. Build tags exclude compiled functionality, not source
files from public module archives. No untagged custom-job method returns a disabled
stub.

## Existing v1 server

This excerpt belongs inside your application after importing
`github.com/ziad-hsn/cpra/sdk/go/legacy`; `ctx` is your request context and `token`
is the configured API token. It reads a bounded page from the current server.
The executable [legacy example](source/sdk/go/legacy/example_test.go.txt) includes all setup and a
local HTTP fixture.

```go
client, err := legacy.New(legacy.Config{BaseURL: "http://127.0.0.1:8060", AuthToken: token})
if err != nil { return err }
page, err := client.ListMonitors(ctx, legacy.MonitorListOptions{Page: 1, Size: 100})
```

This is an explicit client choice. V2 mutations never fall back to v1. Legacy
numeric monitor IDs and existing JSON units are preserved, including duration
fields expressed as nanoseconds.

## Generation and verification

From the repository root, run `GO=/path/to/go python3 tools/sdkgen/generate.py
--check`. The pinned local generator is `oapi-codegen` 2.8.0 with runtime 1.6.0.
OpenAPI inputs live under `api/openapi`; generated public types, internal transport,
and embedded schema have independent default and tagged variants. `generation.json`
records hashes. Generation neither needs a platform account nor imports the server.

Run `go test ./...` and `go test -tags externaljobs ./...` within this module. The
nested worker module requires its own test invocation; root `go test ./...` does
not cover either nested module. Contract tests cover every inventoried v2 operation
against an HTTP fixture and test safety boundaries. Repository-level tests compare
legacy calls to the real existing server. Server v2, external-worker server
qualification, downloaded published modules, and release signatures remain distinct
gates.

## Read package documentation

Each public package has an overview and testable Go examples. Use these commands
from a module that requires the SDK, or from this module's checkout:

```sh
go doc github.com/ziad-hsn/cpra/sdk/go
go doc github.com/ziad-hsn/cpra/sdk/go/api.Driver
go doc github.com/ziad-hsn/cpra/sdk/go/collection.Freeze
go doc github.com/ziad-hsn/cpra/sdk/go/legacy.Client.ListMonitors
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

<!-- Imported from sdk/go/README.md; preserve candidate scope and reconcile with original before regenerating. -->
