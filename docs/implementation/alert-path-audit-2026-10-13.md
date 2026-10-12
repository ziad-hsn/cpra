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
