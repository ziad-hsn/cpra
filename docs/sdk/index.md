---
title: SDK candidate · Build integrations with the CPRa Go SDK
description: SDK candidate · Build integrations with the CPRa Go SDK for the reviewed CPRa source; see the version and availability notice.
cpra_scope: sdk
---

> **Unpublished SDK candidate:** reviewed source snapshot of 13 September 2026; SDK modules, v2 writes and the external-worker dispatcher are unavailable in `main`. See [availability and source](../versions.md#go-sdk-and-approved-management-plan).

# Build integrations with the CPRa Go SDK

Start with a queue event that creates a monitor. Then connect the same SDK to
load-balancer changes, Kubernetes Service discovery, and an external worker.
Each lesson includes a finite local demo, expected output, code to inspect, and
steps for supplying your own configuration.

**These are candidate SDK integrations.** The existing CPRa server supports
read-only v1. Its v2 management API and external-worker dispatcher still need
implementation and qualification. Local demos exercise SDK HTTP requests and
worker behavior against fixtures; they do not establish production-server,
cloud-account, cluster, or SMS delivery certification.

## Choose a lesson

| Lesson | What you build | What to observe |
| --- | --- | --- |
| [Queue registration](queue-registration.md) | A mock Redis-style queue consumer | A repeated registration resolves to the same monitor before acknowledgment |
| [AWS deregistration](aws-deregister.md) | An EventBridge/SQS consumer for load-balancer changes | Target absence, monitor incarnation, and configuration version are checked before disabling |
| [Kubernetes Services](kubernetes-services.md) | Namespace discovery for all Services | DNS checks for every Service and TCP checks for usable TCP ports; disabled monitors stay disabled |
| [DAO and SMS worker](dao-sms.md) | A read-only governance RPC check and internal SMS handler | A stale chain head triggers a fixture notification; replaying a result receipt does not resend SMS |

Begin in a checkout containing `examples/sdk`. Select Go 1.25 or later, then run:

```bash
export GOTOOLCHAIN=local
python3 scripts/sdk/workspace.py --examples --output /tmp/cpra-sdk.work
export GOWORK=/tmp/cpra-sdk.work
cd examples/sdk
go run ./queue-registration -demo
```

The final line should be `messages_acknowledged=2 monitors=1`. The first delivery
creates `service-payments`; the second observes that same owned check. The queue
removes each pending delivery only after that outcome is known. No health check
or Redis process runs in this lesson.

## Read a response before changing a resource

The SDK keeps resource identity and change preconditions separate. An ID finds a
monitor. Its UID identifies that incarnation. `ResourceVersion` identifies the
configuration you inspected. Incident and control revisions serve different
operations; use the revision required by that method.

```go
current, err := client.Monitors.Get(ctx, "service-payments")
if err != nil {
    return err
}
_, err = client.Monitors.Patch(ctx, current.Data.Metadata.ID,
    current.ResourceVersion,
    api.MergePatch(`{"spec":{"check":{"interval":"30s"}}}`))
return err
```

If another operator edits the monitor before the patch, the server can reject
the stale version. Inspect that conflict. Automatically fetching a newer version
and retrying would overwrite a change you have not reviewed. This snippet shows
the candidate contract; run the lessons against their local fixtures today.

`Disable` and `Enable` change only admission state. Acknowledge records the
person investigating the exact incident. Dismiss pauses that incident's
notifications. Snooze pauses checks, notifications and new recovery until its deadline.
None of these operations introduces a check-now command.

## Apply several resources

Use `collection.Freeze` for files, directories, multi-document YAML/JSON, readers,
and explicitly supplied URLs. Directory recursion requires `Options.Recursive`.
Use `FreezeResources` with an incremental `ResourceSource`, or adapt a small
resource slice with `collection.Slice`.
Freezing checks and stores the input before any activation request. Close the
frozen collection to remove its restrictive temporary spool.

`collection.Preflight` and `Diff` perform ephemeral validation without creating
an apply operation. They accept at most one 4 MiB request. Larger inputs use
staged validation through `collection.Apply`.
`collection.Apply` stages the frozen collection, asks the server to validate all
items, then requests dependency-ordered activation with per-resource conditions.
It returns an operation handle even when work is partial. The DAO lesson shows
five linked resources in one apply operation.

Call `Operations.Wait` to observe progress. Cancelling its context stops waiting;
`Operations.Cancel` is a separate request. Resume only the original operation
with the original frozen content. A changed file requires a new operation.
There is no collection-wide rollback or implicit pruning.

## Read state and measurements

`State`, `History`, `SLO.Get`, `Queues.List`, `Pools.List`, and `Systems.List`
return structured candidate observations. `Metrics` returns structured metrics;
`Prometheus` streams the exposition response to a writer. Measurement values
carry availability and units. A missing latency measurement is not zero.

Page calls default to 100 items and accept at most 500. Use `Iterate` to process
one page at a time. An iterator never gathers a million-monitor fleet for you.
The SDK's `legacy` package exposes the current v1 read endpoints, including
numeric monitor routes, queue and pool history, retained events, readiness,
state, SLOs, and Prometheus text. V2 writes never silently downgrade to v1.

## Configure transport and errors

Create the client with `cpra.New(cpra.Config{...})`. Supply a token callback when
credentials rotate, or a static token when appropriate. The examples read token
files on each request and never print their contents. Caller-owned HTTP clients,
custom transports, and trust roots are supported without changing the shared
SDK's dependencies.

Authenticated v2 use requires HTTPS by default. Local demos explicitly allow
HTTP to their configured loopback origin. Redirects are disabled; ordinary
requests default to ten seconds and reject oversized decoded responses. Read
retries are opt-in and capped at three attempts. Mutations are not retried
automatically.

Use `errors.Is(err, cpra.ErrConflict)` for a version conflict and
`errors.Is(err, cpra.ErrAmbiguous)` when a mutation may have committed without a
usable reply. Use `errors.As` to inspect typed problem details and operation
handles. Avoid logging raw server detail or credential-bearing resources.
The queue and AWS examples demonstrate explicit reconciliation of uncertain
responses.

## Find an exact method or field

- [HTTP operations](api-reference.md): all 66 candidate operations, parameters,
  request/response types, version conditions, build tags, and current v1 routes.
- [Wire types](wire-types.md): resource fields, every built-in driver schema,
  optional values, tagged types, and complete nested JSON schemas.
- [Go declarations](go-reference.md): exported services, helpers, configuration,
  errors, iterators, collections, legacy types, and worker interfaces.
- [Verification](verification.md): executed checks and their observation boundary.
- [Package documentation and publication](publishing.md): module contents,
  executable examples, pkg.go.dev behavior, and the ordered release gates.
- [Guide maintenance](writing-guide.md): writing references and review procedure.

Generate references from the repository root with
`python3 scripts/sdk/reference.py`. Run it with `--check` to reject stale output.
The schemas and Go declarations are the inputs; tutorial prose is maintained
separately so a generator does not replace explanations of behavior.

## Package guides

[Core SDK](module-overview.md) · [Configuration collections](collections.md) · [External worker](worker-library.md) · [OpenAPI inputs](openapi.md) · [Example workspace](examples.md)
