# Discover Services in a Kubernetes namespace

This program reads **all Services in one namespace** and registers CPRa monitors
for them. It gives each Service a DNS-resolution monitor and adds TCP connection
monitors for the ports that can be addressed directly. It does not discover Pods.

**CPRa must run inside the same Kubernetes cluster.** The generated targets use
names such as `redis.payments.svc.cluster.local`. CPRa also needs DNS access and
network permission to reach the namespace. The discovery program may run in the
cluster or use an explicitly supplied kubeconfig from your workstation.

The local demo works now. Connecting it to CPRa requires the planned management
v2 server, which is not implemented in the current application. A passing demo
tests client behavior against HTTP fixtures; it does not establish Kubernetes or
CPRa production compatibility.

## Try the local demo

Prepare the example module using the [examples setup guide](index.md), then
run these commands from `examples/sdk`:

```sh
go run ./kubernetes-services -demo
go test ./kubernetes-services
```

No kubeconfig, cloud account, or CPRa token is read in demo mode. The program uses
the real client-go and CPRa SDK HTTP clients against two local fixture servers.
The fixture Kubernetes server returns `redis` and `resolver` on separate list
pages, then sends a watch event for `checkout`. You should see five created
monitors:

| Service | Created monitors |
| --- | --- |
| `redis` | DNS resolution and TCP port 6379 |
| `resolver` | DNS resolution; the UDP limitation is printed separately |
| `checkout` | DNS resolution and TCP port 8080 |

The demo repeats the initial list. It reads the same stable monitor IDs and
creates no duplicates. Its final line reports five monitors. The demo does not
run the generated health checks against real targets.

## Find the code behind the result

| File | Functions to read | Responsibility |
| --- | --- | --- |
| [main.go](source/examples/sdk/kubernetes-services/main.go.txt) | `command` | Validates flags, loads Kubernetes credentials, constructs both clients, and handles cancellation |
| [discovery.go](source/examples/sdk/kubernetes-services/discovery.go.txt) | `desired`, `identity` | Converts a Service into typed monitors with stable identities and ownership labels |
| `discovery.go` | `reconcile`, `upsert`, `replaceObjectPatch` | Creates missing monitors or conditionally patches the owned check fields |
| `discovery.go` | `list`, `watch`, `runWithIntervals` | Handles pagination, watch cursors, reconnection, and periodic full reconciliation |
| [demo.go](source/examples/sdk/kubernetes-services/demo.go.txt) | `runDemo` | Runs the same discovery path against local Kubernetes and CPRa HTTP fixtures |
| [discovery_test.go](source/examples/sdk/kubernetes-services/discovery_test.go.txt), [review_test.go](source/examples/sdk/kubernetes-services/review_test.go.txt) | `Test...` functions | Check target generation, version preconditions, control preservation, and recovery from interrupted watches |

This directory is a runnable `package main`. Copy and adapt its discovery logic
in your own application; use `github.com/ziad-hsn/cpra/sdk/go` and
`github.com/ziad-hsn/cpra/sdk/go/api` as the public SDK imports. Kubernetes
client-go remains a dependency of your integration, not of the CPRa SDK. The
example module's `internal` helpers cannot be imported by an unrelated module.
The [setup guide](index.md) explains how to build against the current
unpublished SDK candidate.

## Understand the two clients

The program has one client for discovery and another for CPRa management. The
Kubernetes client is restricted to Services in the selected namespace:

```go
core, err := typedcore.NewForConfig(config)
if err != nil {
    return fmt.Errorf("Kubernetes client: %w", err)
}
```

This excerpt is from `command`; `config` is a `*rest.Config` loaded from a trusted
kubeconfig or the Pod's in-cluster identity. `core.Services(*namespace)` supplies
the `ServiceInterface` stored in `discovery.services`. The CPRa client is created
with `cpra.New` and stored separately in `discovery.client`. A Kubernetes token is
never used as a CPRa token.

All requests receive `ctx`. In this command it is canceled by Ctrl+C or SIGTERM,
which ends list/watch work and any in-progress SDK request. Your own service can
supply its existing application context instead of installing another signal
handler.

## Turn a Service into monitors

In `desired`, `s` is the Kubernetes Service and `d.domain` is the configured
cluster domain. These are excerpts from the function, not standalone programs:

```go
host := s.Name + "." + s.Namespace + ".svc." + d.domain
```

The local `makeMonitor` closure calls `api.Driver` to construct and validate the
requested driver. Every Service first receives this DNS check:

```go
dns, err := makeMonitor("dns", "DNS resolution", "dns", api.PulseDNSConfig{Host: &host})
if err != nil {
    return nil, nil, err
}
monitors := []api.Monitor{dns}
```

The closure's first argument is part of the stable monitor identity, the second
is a display label, and the third selects the driver type. `Host` is a pointer
because the public configuration type preserves optional-field presence. The
closure sets the check interval to `60s` and timeout to `5s`.

The rest of `desired` adds eligible TCP ports and returns coverage notices for
ports it cannot safely translate. Monitor IDs combine your stable cluster ID,
the immutable Service UID, and the check key. The name can be reused after a
Service is deleted; its UID cannot. This keeps a new Service from inheriting the
old Service's monitor identity and incident history.

## Patch only the configuration discovery owns

`upsert` first reads the monitor, verifies its ID and ownership labels, and checks
that the current driver is understood by this SDK. If the check is unchanged,
the function returns without writing. Otherwise it creates the following patch:

```go
checkPatch, err := replaceObjectPatch(oldCheck, newCheck)
if err != nil {
    return err
}
patch, err := json.Marshal(map[string]any{"spec": map[string]any{"check": checkPatch}})
if err != nil {
    return err
}
version := current.ResourceVersion
if version == "" {
    version = got.Metadata.ResourceVersion
}
_, err = d.client.Monitors.Patch(ctx, desired.Metadata.ID, version, api.MergePatch(patch))
```

`oldCheck` and `newCheck` are the encoded current and desired check objects.
`replaceObjectPatch` inserts explicit `null` values for fields that must be
removed: merely omitting an old nested field from JSON Merge Patch leaves it in
place. The outer `spec.check` wrapper keeps the write away from `spec.enabled`,
notification settings, and operator controls.

`version` is the CPRa resource version observed by the preceding read. The SDK
uses it as a write precondition so a concurrent change is surfaced as a conflict.
It is separate from Kubernetes' collection/watch `resourceVersion`, which only
tracks discovery progress. Neither version is a timestamp or a number for this
program to increment.

## Read a namespace before writing to CPRa

Choose a cluster identifier that you will keep across restarts. It distinguishes
clusters that contain Services with the same names. Changing it creates a new set
of monitor IDs. Use only trusted kubeconfig files: their credential configuration
can invoke executable authentication helpers.

```sh
go run ./kubernetes-services \
  -kubeconfig "$HOME/.kube/config" \
  -cluster-id staging-eu \
  -namespace payments \
  -once -print > /tmp/payments-monitors.jsonl
```

This command lists a real namespace and writes one monitor resource per line. It
makes no CPRa requests. Read the output and the diagnostics before adopting the
targets. Set `-cluster-domain` if your cluster does not use `cluster.local`.

Service names follow Kubernetes' cluster DNS convention. A regular Service name
resolves to its virtual IP. A headless Service name can resolve to several endpoint
addresses. [Kubernetes DNS for Services](https://kubernetes.io/docs/concepts/services-networking/dns-pod-service/)

## Connect discovery to management v2

Once the corresponding CPRa server operations are available and qualified,
provide an operator token with permission to read, create, and patch monitors:

```sh
go run ./kubernetes-services \
  -kubeconfig "$HOME/.kube/config" \
  -cluster-id staging-eu \
  -namespace payments \
  -server https://cpra.example.net \
  -token-file /run/secrets/cpra-operator-token \
  -once
```

Omit `-once` to continue watching for added and modified Services. A full list
resync runs every five minutes, including while the watch is quiet. Stop the
process with Ctrl+C. Without `-kubeconfig`, it uses the Pod's in-cluster identity. The token
file belongs to this discovery service; it is not copied into monitor resources.
HTTPS is required for authenticated CPRa connections unless you explicitly pass
`-allow-http` for the configured origin.

The example needs only `list` and `watch` permissions on Services. The
[RBAC manifest](source/examples/sdk/kubernetes-services/rbac.yaml) grants those permissions in `payments`. Change every
namespace occurrence if you use another namespace, review the manifest, then
apply it. Assign `serviceAccountName: cpra-service-discovery` to the Pod that runs
this program. It does not need permission to read Secrets or change workloads.
Kubernetes Roles restrict namespaced permissions, while RoleBindings assign them
to the service identity. [Kubernetes RBAC](https://kubernetes.io/docs/reference/access-authn-authz/rbac/)

## Follow one Service through the code

1. `list` requests 100 Services at a time. It follows the continuation token and
   checks that the pages belong to the same collection `resourceVersion`.
2. `desired` builds targets from the Service's name, namespace, UID, and ports.
   The monitor ID hashes the cluster identity, Service UID, and check identity.
   A deleted and recreated Service gets new monitors even if its name is reused.
3. `upsert` reads that monitor ID. If it is absent, `Create` sends
   `If-None-Match: *`. If it exists, ownership labels must match before any update.
4. When the generated check changes, `Patch` sends the version returned by the
   read. The patch contains only `spec.check`; it preserves operator disable
   state, snoozes, notification policy, and other monitor settings. Discovery owns
   the check target, 60-second interval, and five-second timeout. JSON Merge Patch
   merges nested objects, so the patch uses explicit `null` entries to remove
   obsolete fields inside the owned check. A driver unknown to this SDK is
   reported for review without a write.
5. `watch` starts after the listed collection version and advances its cursor
   after processed events or bookmarks. A `410 Gone` clears the cursor and starts
   a fresh list. Failed reconciliation is reported and retried after five seconds
   in watch mode. `-once` exits unsuccessfully if any Service could not reconcile.
6. The five-minute resync deadline closes an idle watch and starts a fresh list.
   This also detects changes made directly in CPRa. A monitor deleted while its
   Service still exists is recreated; disable the monitor to pause its checks.
   Reconciliation preserves that disabled setting.

Kubernetes keeps watch history for a limited period. Clients must relist when an
old resource version is no longer available; list continuation tokens can also
expire. The code retains one list page, bounds the traversal to 10,000 pages, and
never treats an incomplete list as evidence that a Service was deleted.
[Kubernetes API concepts](https://kubernetes.io/docs/reference/using-api/api-concepts/)

Conflicts are returned from the individual write without an immediate read-and-
overwrite retry. The next discovery pass reads the current version and reconciles
only its owned check fields. A lost write response is reported as uncertain; a
later pass first reads the same stable ID rather than creating a new identity.

## Understand what each check measures

| Service or port | Coverage |
| --- | --- |
| Every Service, including `ExternalName` and portless Services | DNS resolution of the Service name |
| ClusterIP, NodePort, or LoadBalancer with TCP ports | TCP connection to each Service port through cluster DNS |
| Headless TCP with numeric `targetPort` | TCP connection to that endpoint port through Service DNS |
| Headless TCP with named `targetPort` | DNS only, with a diagnostic; resolving individual EndpointSlices is outside this example |
| UDP or SCTP port | DNS only, with a diagnostic; no generic protocol probe is invented |
| `ExternalName` with TCP ports | TCP connection through the Service alias to the specified port |

Headless Services have no Service proxy to translate a Service port into a target
port. `ExternalName` provides a DNS alias. These differences explain the explicit
port handling in `desired`. [Kubernetes Service types](https://kubernetes.io/docs/concepts/services-networking/service/)

A TCP connection proves that a connection was accepted. It does not prove that
HTTP, Redis, TLS, or another application protocol is healthy. DNS success for a
ClusterIP can continue while all backends are unavailable. A headless name can
select one endpoint; this example does not claim to check every Pod. Add an
application-specific check when you know the service's health contract.

Service deletion and port removal do not delete or disable existing monitors.
Deletion events print the Service UID for operator review. A deleted CPRa monitor
for a Service that still exists is recreated during the next Service event or
five-minute resync. This example does not
maintain a durable inventory for safe pruning, and cannot infer whether an absent
target should remain monitored. Review monitors bearing the
`examples.cpra.io/owner=kubernetes-services` label when retiring Services or ports.

## Extend the integration with a test first

The smallest place to add a discovery rule is `desired`; Kubernetes I/O belongs
in `list` and `watch`, and CPRa writes belong in `upsert`. Keep that separation
when adding a rule so target generation can be tested without either server.

As an exercise, add a Service with two TCP ports to the table in
`TestEveryServiceGetsDNSAndUsableTCPPorts`. Expect three monitors: one DNS check
and one TCP check for each port. Run:

```sh
go test ./kubernetes-services -run TestEveryServiceGetsDNSAndUsableTCPPorts -v
go test ./kubernetes-services -run 'Test(UpsertUses|ReconcileRemoves|ConflictIsReturned|FullResync)' -v
```

The second command checks that your change still preserves operator controls,
removes obsolete owned fields, reports concurrent modifications, and repairs a
deleted monitor during an idle-watch resync. All listed tests should pass.

For an HTTP-specific extension, define an explicit opt-in Service annotation for
the health path and select a known TCP port. Validate the annotation, construct
an `api.PulseHTTPConfig`, and use a distinct check key such as `http:<port>` so it
does not collide with the existing DNS/TCP monitors. Add cases for missing or
invalid annotations and for Services without the selected port. This is a
suggested extension, not a feature of the current example: discovery cannot infer
an application's health endpoint from its Service name.

Before adding deletion or pruning, define durable ownership and a complete
inventory contract. A partial list, an expired cursor, or a watch disconnect
cannot establish that a Service has been deliberately retired.

## What the tests establish

The tests exercise all Service types, multiple TCP ports, UDP/SCTP notices,
headless target-port handling, identities, paginated Kubernetes HTTP requests,
watch bookmarks, expired watch history, partial failures, and deletion reporting.
The real list/watch loop tests verify bounded reconnection, recovery from an
expired watch, and recreation of a deleted monitor during a quiet-watch resync.
CPRa HTTP fixtures verify conditional creation, narrow patches, preserved disable
state, removal of obsolete check fields, unknown-driver refusal, ownership refusal,
and surfaced conflicts. They do not contact a real
cluster or demonstrate that the current CPRa server implements v2.

<!-- Generated from examples/sdk/kubernetes-services/README.md by scripts/sdk/sync_guides.py. -->
