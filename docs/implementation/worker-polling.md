# Worker polling

The tagged worker HTTP adapter honors `waitSeconds` from zero through 25.
This is part of the private external-worker implementation. Normal application
startup now registers the opted-in [worker HTTP routes](worker-http.md).
Controller dispatch and full worker-runner interoperability remain unfinished.

## Waiting and committed replies

After authentication and request validation, the handler reserves one polling
slot for the worker UID. At most 32 workers may poll concurrently. The reservation
covers body reading, waiting, the durable commit and response construction.
Waiting happens outside the mutation admission callback.

The store reads committed session and execution state at intervals of at most
100 milliseconds. Each read checks current authority, owner and session identity,
available worker capacity, advertised capabilities and execution eligibility.
It examines at most the 4,096 retained execution identities. It does not scan
the monitor fleet, decrypt payloads, run a provider, or retain a database
transaction or state lock between reads. This bounded polling interval is not
a measured capacity or latency guarantee.

A wait ends when eligible work is observed, the requested duration expires,
the caller cancels, credentials expire, the owner/storage becomes unavailable,
or the store stops. A waiting read creates no session, assignment, start grant,
or empty Raft reply. A canceled wait therefore does not consume a poll sequence.

Availability is a hint. The subsequent committed poll rechecks the original
authority, session, dependency and control conditions and the allocation limits.
Another worker may win the work between that read and commit; an empty committed
reply is valid in that case. Every external invocation still requires its own
committed Start grant.

An exact retry of a retained poll returns immediately, including an empty reply.
It cannot wait for and substitute newly arrived work. The original client nonce,
session, sequence, capability set and request digest continue to identify that
reply. A changed retry fails; an expired session requires the existing explicit
recovery procedure.

## Deadlines and shutdown

Authentication and body reading use a five-second request budget. After a valid
body, polling gets the requested wait plus five seconds for commit and response,
always bounded by the caller's context. The maximum valid polling request is no
longer truncated by the adapter's former fixed ten-second timeout. The SDK
already uses `waitSeconds + 5s` for the complete client request.

Readiness is checked again after waiting, before mutation admission. The commit
callback remains responsible for ordering admission against process shutdown.
Request completion releases its global and worker-specific reservation even on
cancellation or error.

The subsequent [worker HTTP integration](worker-http.md) adds a 40-second server
write allowance, cancels waits on admission shutdown and separates new work from
completion delivery. Earlier dedicated-mux polling evidence remains scoped to
the adapter; production route tests and complete dispatch qualification are
distinct boundaries.

## Verification scope

The tests cover new work arriving for all three job categories, exact empty and
nonempty replay, a competing poll, cancellation without hidden session creation,
credential expiry, storage failure and stopped ownership. Deterministic unit
timing is separate from real TLS/SDK/Raft tests, including a full 25-second wait.

This change introduces no durable format or wire schema change. The tagged
format remains 27. `WaitWorkerPoll` and its implementation are absent from
default and all-built-in-driver builds.

Independent review, production integration, complete worker/server process
qualification and the broader release gates remain open.
