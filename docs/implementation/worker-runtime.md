# External-worker runtime preparation

Tagged `Catalog.PrepareRuntime` now converts authenticated stored external
configuration into inert check, recovery and notification markers plus detached
`ExternalBindings`. The existing `PrepareMonitorExternal` consumes those bindings
when preparing an Ark entity. This joins configuration decoding to the existing
entity preparation contract. The later [controller dispatch](worker-dispatch.md)
connects bounded assignment admission, with full execution qualification pending.

Public API, bootstrap and collection admission still reject external execution
configuration. Tagged controller preparation now selects the external entity
preparer when external jobs, management and Raft are explicitly enabled. Bounded
dispatch, [unadmitted-action expiry](worker-expiry.md) and
[schedule accounting](worker-schedule-accounting.md) are connected. Public
configuration stays closed pending sustained saturation, process-integration and
independent-review acceptance. Production worker routes
are implemented, while full worker-runner interoperability remains unqualified.
See the [remaining integration](external-worker-server-next-steps.md).

## Ownership and limits

The catalog authenticates encrypted resources, their reference indexes and their
pinned immutable JobType contracts before runtime conversion. Each binding keeps
the exact source kind, ID, incarnation, configuration version and slot. A marker
contains that identity; its separate descriptor owns parameters and a worker-local
credential-profile name. Neither is a local executable or an invocation grant.
CPRa-held provider credentials cannot enter an external descriptor.

Repeated use of one external endpoint shares a single prepared binding. Local and
external destination positions remain aligned with the notification target list.
An endpoint filtered out by `notifyType` receives no runtime binding. Inline
notifications retain a monitor-owned slot; completing their execution admission
requires the dispatcher/protocol to preserve that source identity as well.

Runtime preparation retains the existing 10,000-resource bound and charges detached
bindings against its 32 MiB preparation budget, in addition to encrypted source
inputs. Cancellation stops preparation. No provider runs during conversion or
entity installation. The entity preparer copies input before ownership transfers
to the controller; callers cannot change installed parameters by mutating the
preparation result. Default and all-built-in-driver builds expose no
`ExternalBindings` field.

## Controller projection

Startup and configuration reconciliation use the same prepared bindings. The
owner commits notification source kind, stable ID, incarnation and configuration
revision at each original color and endpoint ordinal. Mixed local/external
destinations retain their positions; local slots contain no worker source. Inline
notifications name the Monitor as their source. Non-dispatching colors contribute
no notification intent. Parameters and worker credential profiles never enter the
monitor policy or its Raft command.

External checks contribute zero demand to the local pulse worker pool. Replacing
a local check with an external check removes its previous contribution;
replacing it with a local check restores demand. Snoozing and disablement retain
their existing exclusions. These counters concern local worker capacity; they do
not report external execution capacity or complete remote schedule accounting.

The controller still owns every ECS mutation. Background preparation transfers
private inputs through the existing bounded handoff. Configuration guards are
checked after commit and before installation, including after restart. This
change creates no additional goroutine, provider call or remote execution grant.

## Execution identity and configuration versions

Exact resource versions remain in admission conditions and binding keys. For a
preparation containing external bindings, execution identity uses a separate
versioned hash of configuration content, resource incarnations and immutable
JobType references. Inputs are ordered by kind and ID; JSON object keys are
canonicalized and numbers retain their original precision.

A display-name edit, ciphertext rewrap or current JobType metadata edit therefore
does not change the execution-content identity. The conditional source version
still changes when its resource changes. Parameter changes and replacement
incarnations change execution identity. The hash covers the loaded configuration
dependency closure, including referenced destinations that routing may filter
out; it is deliberately conservative about dependency changes.

Preparations containing only built-in drivers retain their existing execution
fingerprint. No new Raft command, storage format or worker protocol version is
introduced by this change. Retry settings remain present in the monitor; a worker
marker has no local retry loop.

## Verification boundary

Tests exercise encrypted committed catalog inputs, runtime conversion, and actual
Ark entity installation with mixed notification slots. They cover selected
destinations, binding ownership, no provider calls, bounded retention, cancellation,
metadata versus parameter changes, immutable contracts, and integers beyond
floating-point exactness. These fixtures install external resources through the
internal committed catalog path; they do not establish public external admission.

All 24 selected runtime/reference/assignment tests passed with Go 1.27.1 race
instrumentation and Go 1.25.0. Eleven default runtime/projection tests passed on
both compilers; tagged vet, all 340 build-exclusion checks and 31 release-tool
tests also passed. The [scoped report](../review/local-evidence.md#record-474ab2ba713337f08756a55d)
separates the 1,083-input polling checkpoint from the subsequent 1,085-input
runtime checkpoint and preserves unsuccessful initial attempts. Independent
review and complete production integration remain open.

The subsequent
[controller checkpoint](../review/local-evidence.md#record-ab6681e19b6cea4c39fc8984)
records the complete controller package suites: 69 tagged tests and 63 default
tests passed on Go 1.27.1 with race detection and on Go 1.25.0. Each parent run
skipped one subprocess-only lock probe; the owning tests execute that helper in
separate processes. Default/tagged vet and 232 negative compilation checks for
116 excluded symbols passed. The recorded default and built-in-driver source
lists include the base controller adapter and exclude the external adapter.

The new Raft fixture authenticates encrypted monitor, endpoint and JobType
records, installs mixed notification positions, closes/reopens the store and
rebuilds the same owner projection. It rejects a stale endpoint preparation and
verifies zero provider calls. The local-demand test covers replacing a local
check with an external check, replacing it back, idempotent projection and
snoozing. A separate test rejects missing opt-in and mismatched bindings.
The 1,099 recorded Go/module inputs remained unchanged during the passing checks.
This still leaves remote dispatch, public external configuration and independent
review open.
