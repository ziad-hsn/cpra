# Alert path reachability audit

This is a source audit of `37ee8e6`, with the application/runtime implementation
unchanged from `748fe4d`. It does not remove packages, promise compatibility for
internal APIs or count old tests as production-path coverage.

The non-test application contains one `controller.NewController` call in
`runCPRa`. Startup opens the selected Store, returns on failure, assigns that
non-nil Store to the controller configuration and then constructs the controller.
Explicit memory mode still supplies a Store. Built-in and externaljobs build
variants do not replace that selection with a nil-Store caller.

`NewController` installs `DurableSystem` when Store is present. The previous
batch scheduling/result/alert systems are installed only in its nil-Store branch.
Their objects are currently constructed before branch selection even though the
normal executable does not install them. That allocation and compiled branch
are distinct from runtime production reachability.

The old path still has direct tests for recovery thresholds, single-flight
interventions, fanout, cooldown/retry behavior, TLS warning/clearance, snapshot
aggregation and throttling. Nil-Store controller tests also exercise startup
sizing and lifecycle. Removing their implementation while discarding those
assertions would not satisfy the approved regression-migration requirement.

The next code-removal step must move the useful assertions to the committed
owner, retained per-endpoint action outcomes, incremental view and pool/lifecycle
paths. Some old retry/cooldown expectations describe obsolete behavior and need
an explicit replacement by the current unknown-outcome/episode policy. Existing
active regressions cover several of these cases, but this audit does not claim
a complete one-to-one migration or authorize an unreviewed deletion.

No dead-path cleanup is claimed complete. The evidence resolves the executable
selection question; test migration, removal, default/tagged builds and independent
review remain required. Current package names and storage formats are preserved.

## Removal follow-up, 14 October

The subsequent working change removes the nil-Store branch and its batch/alert
systems. The controller requires an initialized Store before allocating queues
or workers; explicit memory mode still uses the same owner. Pulse staggering,
ready queues, result guards and monitor-summary helpers remain on the active
path. Package names and persisted formats are unchanged.

Useful regressions now exercise committed transitions and projection:

| Previous assertion | Active coverage |
| --- | --- |
| Consecutive healthy checks before recovery | `TestRecoveryRequiresConsecutiveSuccesses` |
| One intervention per incident | `TestInterventionIsSingleFlightPerIncident` |
| Partial queue admission and per-endpoint completion | `TestAlertFanoutRetainsIndependentDelivery` |
| Bounded retry following confirmed rejection | `TestNotificationConfirmedRejectionStopsAfterThreeAttempts` |
| Recovery and maintenance prevent unsafe admission | `TestRecoveryCancelsUnadmittedIntervention`, `TestMaintenanceSuppressesDelayedIntervention` |
| Failure counts and TLS warning/clearance | Migrated intervention-admission and TLS tests |
| Fleet aggregates and bounded navigation | `TestCommittedProjectionAggregates` and `fleetview` index tests |
| Startup sizing and concurrent stop | Controller sizing/lifecycle tests using a memory Store |

Old tests requiring deferred cooldown alerts, clearing cooldown for every error,
or a full-fleet snapshot timer describe superseded behavior. Their replacement
is the existing episode, obsolete-notification cancellation, held-unknown,
retained endpoint outcome and incremental projection coverage. New tests preserve
logical delivery/deadline identity across three attempts and duplicate results.
This does not reintroduce unconditional HTTP 5xx retries.

Independent source review closed this finite removal after identifying and
correcting two lost assertions: fanout must rely on retained scheduling instead
of test-injected readiness, and the projection must count pending red delivery
and decrement it after cancellation. The reviewer ran no tests or builds.

| Private development check | Result |
| --- | --- |
| Go 1.27.1 controller subtree and fleetview | Pass; controller 38.469 s, systems 22.329 s |
| Go 1.27.1 `externaljobs` subtree and fleetview | Pass; controller 39.335 s, systems 45.051 s |
| Go 1.25.0 subtree and fleetview | Pass; controller 40.693 s, systems 26.552 s |
| Go 1.27.1 race subtree and fleetview | Pass; controller 45.429 s, systems 29.586 s |
| Minimum/tagged component and entity tests after field cleanup | Pass |
| Corrected fanout and pending-code projection assertions | Pass, 0.132 s systems package |

These runs use Linux/WSL and the private local-module workspace. The three
ordinary subtree runs precede the final unused-field/test-assertion changes;
the affected packages/assertions were rerun as listed. The race run includes
field cleanup and precedes the two assertion-only corrections. This is scoped
regression evidence, not a frozen whole-repository release matrix. The initial
race invocation with cgo disabled failed before tests and remains separate;
the passing race run explicitly enabled cgo. No provider-account operation ran.

The original audit above remains a dated account of the earlier tree. Active
package names and persisted formats are preserved; the unreferenced alerts
implementation is removed rather than given a replacement package name.
