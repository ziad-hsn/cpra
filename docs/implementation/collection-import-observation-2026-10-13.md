# Ordinary collection import observation

The real CLI collection regression now includes 35 monitors from one manifest
and 50 monitors from five team-separated manifests. It executes freshly built
native CLI subprocesses against the normal application entrypoint, real TLS,
single-node Raft and controller owner in the test process. This is Linux/WSL
development evidence, not process-crash, provider-account or release qualification.

Both cases require the original admitted operation/content identities, complete
terminal execution results and every child applied. API reads require generation
one, owner-observed generation agreement and explicit disabled configuration.
The readiness assertion checks usable controller/storage admission after import.
A graceful owner restart must retain each UID/resource version and rebuild its
observed projection. A real HTTP receiver counts zero calls: checks are disabled,
and validation, reads and readiness inspection cannot cause provider operations.

The initial 35-resource case passed. The first 50-resource case reached the
fixture's 12-second wait deadline and is retained as a failed run; all 50 rows
were projected by shutdown, but terminal completion was not established in that
run. Sized imports now use an explicit 30-second operation wait inside a
45-second native-process budget. Ordinary HTTP requests retain the existing
five-second test timeout. Production health-check targets are unchanged.

| Compiler/build | 35 monitors, one file: apply-to-observation time | 50 monitors, five files: apply-to-observation time |
| --- | --- | --- |
| Go 1.27.1 default | 15.47 seconds | 25.52 seconds |
| Go 1.25.0 default | 15.45 seconds | 20.54 seconds |
| Go 1.27.1 externaljobs, matching tagged CLI | 15.42 seconds | 25.53 seconds |
| Go 1.27.1 race-instrumented server/test, normal CLI | 20.92 seconds | 26.03 seconds |

Every row passed the parent collection regression, both sized imports and their
graceful restarts. These are single development runs, include CLI polling, and
are not a performance benchmark or an import-throughput promise. The final
readiness assertion was added before the race run; the preceding three runs
already required complete collection results and successful monitor reads.

The existing opt-in CLI integration job selects this parent test for default
and externaljobs builds. Its absent-invocation skip remains separate from these
explicitly enabled passing checks. Complete frozen-source matrices and the
remaining interruption/contention campaigns remain open. Independent source
review of this regression extension remains required.
