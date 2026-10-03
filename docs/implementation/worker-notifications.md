# External notification sources

This private implementation extends external notification admission to monitor
inline configuration. Public external configuration and controller dispatch are
still incomplete; this contract does not make an external monitor executable.
See the [remaining server integration](external-worker-server-next-steps.md).

## Configuration identity and permission

An external notification has one of two source kinds:

| Configuration | Source kind | Worker grant |
| --- | --- | --- |
| A referenced notification endpoint | `NotificationEndpoint` | Notification JobType/version and endpoint ID |
| A driver defined on a monitor's notification color | `Monitor` | Notification JobType/version and monitor ID |

The source ID, incarnation and configuration revision are retained at the
original notification color and ordinal in `Policy.WorkerNotificationSources`.
Built-in notification positions remain empty. The permission's resource kind is
exact: an endpoint grant never authorizes a monitor source, or vice versa. This
also applies to a wildcard resource ID. A stable-ID grant does not bypass the
execution's separate incarnation, dependency, control and deadline checks.

For inline preparation, `Catalog.PrepareWorkerExecution` reads the existing
committed action. It verifies that action's monitor incarnation, execution
revision, queued state and scheduled time, and uses its original color to select
the authenticated `notifications.<color>` configuration. Callers do not supply
a new color or notification target. The encrypted assignment binds the action
ID and the existing complete execution identity.

Preparation grants no permission. Admission and Start recheck the committed
source position, current configuration, controls, action limits and worker
authority. A configuration change between preparation and admission cannot
redirect the old action. Opening retained ciphertext continues to return its
original parameters, rather than reading a new target into an old assignment.

Results and finalization keep the existing receipt contract. An uncertain
notification stays unknown, and a terminal Start request returns a terminal
disposition without an executable grant. Replaying a result recovers its
original receipt. Worker restart and matching terminal reconciliation retain an
unknown outcome and permit append-only evidence against its original receipt;
a different receipt cannot replace that identity. A source-kind distinction is
not an exactly-once delivery guarantee.

## Storage contract

Tagged storage format **28** identifies explicit notification source kinds,
monitor-scoped notification grants and inline notification executions. Writers
select it when those records first appear. Envelope and snapshot validation
reject the new meanings under older format numbers.

An omitted source kind preserves the endpoint representation introduced in
format 20. Its JSON encoding and existing execution digests remain unchanged.
An explicit kind must be `Monitor` or `NotificationEndpoint`; null, empty and
unknown kinds cannot silently become endpoint identities. Default builds remain
at format 14, exclude the tagged implementation and reject format-28 state.

This change adds no worker HTTP fields, provider credential delivery or runtime
code loading. It retains the existing encrypted parameters and worker-local
credential-profile alias.

## Local verification — 29 September 2026

The final notification checkpoint records 240 selected server/persistence tests
on Go 1.27.1 with race detection and Go 1.25.0, including 60 native process-kill
cases per compiler and one expected subprocess-helper skip. The complete tagged
SDK and worker-module runs passed 236 tests on each compiler. Another 20 selected
default-boundary tests passed on each compiler, along with tagged vet and
two-root generation. These checks cover the same 1,091 Go/module inputs.

Build exclusion checks 110 symbols in tagged builds and each symbol's absence
in both ordinary and all-built-in-driver builds. The grouped compiler path must
produce every expected unavailable-symbol diagnostic; real-compiler adversarial
cases prove that one leaked symbol or missing enabled symbol fails verification.
The release-tool suite passed 32 tests. Earlier failing and interrupted attempts
remain in the [private evidence](../review/local-evidence.md#record-f55c7111789b8c2953c93c85).

The rebuilt dashboard passed all 498 tests, type checks, lint and asset freshness.
Its full asset inventory matches the earlier passing eight-case browser campaign;
the browser campaign was not rerun for unchanged assets. This is scoped local
evidence, not qualification of subsequent server changes, complete worker
execution, independent review or a release.
