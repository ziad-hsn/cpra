# Controller deadline waiting observation

This is private Linux/WSL development evidence for `748fe4d`, not fleet capacity
qualification, percentile attainment or a published release result. No endurance
test ran. The earlier [result timer observation](idle-profile-2026-10-12.md)
measures a different change and remains separate.

The [controller](../review/source/internal/controller/controller.go.txt) uses an idle timer and
coalesced hints from committed state, routed results and prepared configuration.
Only its owner accesses the ECS world. Its [wait calculation](../review/source/internal/controller/systems/owner_wait.go.txt)
includes check/action/snooze/SLO deadlines and a one-second health/progress
fallback. Pending work retains the configured update interval. Repeated hints
cannot postpone or bypass the fixed earliest update boundary. Shutdown keeps
the existing bounded drain loop.

The [scheduler bitmap](../review/source/internal/scheduler/scheduler.go.txt) finds the next
occupied wheel bucket or overflow-root deadline without scanning monitor rows.
Its lookup examines at most 129 bitmap words. A separate helper benchmark with
zero, 100,000 and 1,000,000 scheduled entries measured 1.264, 4.719 and 7.630 ns
per lookup respectively, with zero allocations in all three cases. This does
not execute a million-monitor workload.

## Short process comparison

Both executables use Go 1.27.1, disabled cgo, baseline CPU targets, trimpath,
normal optimizations, PGO off, VCS stamping off and readonly module resolution.
Ambient Go flags/experiments were cleared. The before executable uses an exact
Go-source overlay from `fdb6b14`; other build inputs are unchanged. The after
executable uses the committed owner-waiting implementation. Build records retain
source inventories, overlay/input hashes and executable digests. The harness
checks that executable bytes do not change during measurement.

For each workload, three interleaved before/after process pairs create their own
Raft directories, warm for five seconds and measure for ten seconds. A separate
real HTTP receiver counts requests. Process CPU counters measure consumption
without an active CPU profiler. The host exposes 12 logical CPUs; results are
percent of one core, not percent of the whole machine.

| Workload | Before mean (range), % of one core | After mean (range), % of one core |
| --- | --- | --- |
| No monitors | 8.43 (8.00–8.70) | 4.80 (4.50–5.10) |
| One HTTP monitor / 60 seconds | 9.00 (8.90–9.10) | 4.17 (3.80–4.70) |

All six empty measurement windows produced zero target requests. All six
one-monitor windows produced one request each. All twelve owned processes
stopped cleanly. Unrelated host activity remained present; recorded load averages
declined during the comparison. These short observations establish the measured
local result and its limits, not universal causal or long-run resource claims.
RSS was recorded, but this work makes no memory-stability claim.

## Correctness and review

Deadline-oracle tests cover replacement, cancellation, bitmap-word boundaries,
wheel wrap, overflow and clock jumps. Deterministic owner tests cover idle waits,
coalescing, the update-rate boundary and shutdown. Store/router tests check that
hints follow committed state or result delivery and retain bounded capacity.

Complete controller, controller-system, scheduler and queue suites pass on
Go 1.27.1 with default and externaljobs builds. Focused race checks pass.
The Go 1.25 full run passed controller/scheduler/queue and found a maintenance
fixture's wall-minute crossing; the fixture now uses a controlled one-shot
boundary. Three repetitions and the complete controller-system rerun pass.
The failed run remains retained. It is not relabeled as a pass.

Independent read-only source review of `fdb6b14..748fe4d` found no concrete
correctness, security or ownership defect. That reviewer did not rerun the
tests or performance comparison. Final frozen-source matrices, broader process
campaigns, native platform evidence and the agreed fleet benchmark remain open.

The measurement script, progress/final report, per-run process logs, build
metadata, binaries and failure evidence remain in protected task-owned scratch.
They are not public CI assets or a release attestation.
