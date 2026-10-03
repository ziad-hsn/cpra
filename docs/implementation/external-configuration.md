# External-job configuration admission

This change connects external check, intervention and notification configuration
to normal resource preparation, collection validation and initial bootstrap.
It is private, uncommitted implementation work. The verification record is
`bin/verification/external-config-2026-09-30/`; independent review and the complete
release gates remain open.

## Enablement and ownership

The server and the SDK or CLI consumer require the `externaljobs` build tag.
The server must also explicitly enable the extension:

```yaml
external_jobs:
  enabled: true
```

Normal application configuration requires authenticated management, protected
transport, encryption and Raft storage. A tagged build with this setting omitted
or false rejects new external configurations. An untagged build excludes the
external contract, worker protocol routes and native JobType read methods.

Catalog options are captured when the catalog is constructed. They are not a
mutable process-global switch. The controller's existing runtime opt-in remains
an independent dispatch gate. Resource write permission cannot authenticate a
worker or grant it permission to start an execution.

The built-in driver registry still describes local drivers. External bindings
resolve registered JobTypes and dispatch to separate workers; validation does
not create a local executable adapter or contact a provider.

## Register a contract, then apply configuration

Start an empty managed instance, register the required JobTypes through the
tagged SDK, provision worker grants while the owner is stopped, then apply
monitor and endpoint resources through the normal API or collection operation.
JobTypes retain their separate management namespace and are not generic batch
resources.

A monitor can select an immutable registered version:

```yaml
apiVersion: cpra.io/v2
kind: Monitor
metadata:
  id: worker-service
spec:
  check:
    interval: 60s
    timeout: 5s
    driver:
      type: external
      config:
        jobTypeID: service-check
        version: v1
        parameters:
          target: https://service.example/health
        credentialProfile: service-account
```

`service-check` must already exist with category `check` and a parameter schema
that accepts this object. Its versioned handler descriptor and worker protocol
must match the worker's registered handler. `credentialProfile` identifies a
worker-local credential profile; it is not a file path. External drivers reject
CPRa `credentialRefs`, so CPRa-held provider secrets cannot be forwarded to the
worker through credential resolution.

The same binding shape is accepted for a monitor's recovery driver, an inline
notification driver, or a `NotificationEndpoint` whose JobType category is
`notification`. Recipient/group routing still selects configured endpoints.

## Conditional changes and retained contracts

Create, replace and merge-patch use the existing resource authorization and
version preconditions. Validation checks the current JobType incarnation, the
requested retained version, its category and its parameter schema. The encrypted
catalog record pins the JobType ID, incarnation, version and immutable revision.
Parameter values and credential-profile names do not enter the cleartext index.

The state machine rechecks those references when the resource commits. Deleting
and recreating a JobType between preparation and commit cannot redirect the
prepared resource. Referenced JobTypes cannot be deleted. Publishing a new
version within the same incarnation does not invalidate a resource pinned to an
older retained version.

Runtime-disabled tagged instances can authenticate and read retained external
resources. They reject new external replacements and preserve the controller's
disabled execution gate. Removing a resource retains the existing audited
deletion and unknown-action policy.

## Complete collection validation

Ephemeral preflight and encrypted staged validation use the same catalog settings
and contract checks. A final invalid or missing contract rejects complete
validation before any resource activates. Activation and per-resource preparation
retain the existing conditional-write and partial-operation semantics.

Validation policy version 5 adds enabled external categories to the catalog's
validation identity. Restarting with different runtime enablement cannot activate
or execute a collection using a verdict from the previous setting. The original
operation remains inspectable; it is not silently revalidated under new settings.

## Startup and offline validation

An initial bootstrap can use contracts already retained in the same store. A
fresh store with no registered contract fails explicitly and stays inactive;
starting empty and registering contracts before Apply is the normal setup path.
An existing authoritative catalog still takes precedence over startup file flags.

Encrypted bootstrap stage format 2 binds its validation profile and whether
stored contracts were verified. Resume rejects a different runtime profile or a
switch between offline and authoritative validation. Existing stage-format-1
files are rejected rather than repaired or replaced. Restore an interrupted
bootstrap with its matching source artifact and stopped backup.

During seeding, ordinary catalog/JobType reads remain unavailable. A separate
native read permits the owning bootstrap stage to read retained immutable
contracts, checks recorded storage failures and cancellation, and returns detached
ciphertext. A different stage cannot borrow that read authority. It exposes no
partially seeded resources or permission to execute work.

`cpra -validate` performs offline shape and build/runtime checks without opening
Raft. It reports the number of stored-contract references it could not verify.
Successful offline validation therefore does not certify a JobType's existence,
schema, worker grants, provider access or execution. Startup or Apply must verify
the authoritative contracts before admission.

## Verification boundaries

The regression cases cover all three driver categories, API create/replace/patch/
delete, reader denial, pinned contract deletion, stale incarnations, restart,
collection activation from multiple files and an explicitly requested URL, a bad
final contract with zero active changes, offline coverage reporting, initial
bootstrap's private read boundary, and restart of interrupted bootstrap seeding.
The source URL receives no CPRa authorization header or cookie.

The focused [Go 1.27.1 race report](../review/local-evidence.md#record-87c49725c4cd774c8a39f1f7)
and [Go 1.25.0 report](../review/local-evidence.md#record-93268c59dfb54bd03a7a72f2)
each record 24 passing tests, no skips and unchanged Go inputs. The broader
management/bootstrap selections each pass 223 tests, with the subprocess helper
explicitly skipped. Earlier failed attempts retain the reproduced partial-bootstrap
lookup error and its subsequent correction rather than being overwritten.

The complete all-built-in-driver management package with `externaljobs` passes
1,108 tests on each compiler, with race detection on Go 1.27.1 and unchanged Go
inputs. The all-built-in-driver package without the extension passes 856 tests on
each compiler. Four skips are explicit in each variant: three subprocess entry points are invoked by
their parent process tests, and the unavailable-driver case does not apply when
all built-in drivers are compiled. The first complete attempts exposed two pure
routing fixtures that omitted catalog settings after validation began using
those settings. The fixtures now provide disabled extension options; their
original routing-work and cancellation assertions remain unchanged. Both focused
correction runs pass, and the failed complete attempts remain retained.

Build exclusion is verified on both compilers: 135 tagged symbols compile with
the tag and fail with their exact missing-symbol diagnostics in default and
all-built-in-driver builds. The checks include `LookupBootstrapJobTypeVersion`
and `CatalogOptions.ExternalJobsEnabled`, source selection, dependencies and
runtime schema/route boundaries. The unavailable-symbol verifier also requires
the diagnostic's receiver type to match, so an unrelated compiler failure cannot
establish exclusion.

All four affected vet selections pass on the two compilers, with and without the
extension. The [complete local checkpoint](../review/local-evidence.md#record-3482c687a1d4f99fa7e0a4d2)
binds the reports, raw outputs and source inventories and preserves six failed
attempts. The earlier API and process reports precede only the two routing-test
fixture corrections; their executable production paths are unchanged. This
explicit input distinction is retained instead of describing these runs as one
frozen release candidate.

The [Go 1.27.1 race process report](../review/local-evidence.md#record-494cad1a22ff0f1eaf661327)
and [Go 1.25.0 process report](../review/local-evidence.md#record-a25e45930e09433f16933428)
each pass all 37 separate-process scenarios, plus the parent test, with no skips
or changed Go inputs. The 27 ordinary crash scenarios create monitors through the
public SDK. The ten configured-capacity cases and earlier load fixtures continue
to install their controlled initial state in the stopped store; their evidence is
kept separate. The scenario inventory is checked against each retained raw Go
stream, rather than treating a parent pass alone as complete execution evidence.

Passing these local contracts does not qualify a frozen downloaded SDK,
independent review, native distributions, account-backed providers or the
million-monitor campaign. Those remain in the [release status](STATUS.md).
