---
title: Candidate · Architecture
description: Candidate · How CPRa connects its ECS controller, check and recovery workers, notifications and read-only snapshots.
cpra_scope: release_candidate
---

> **Release candidate:** this page describes [`410fbfb`](https://github.com/ziad-hsn/cpra/commit/410fbfb0092d01277b3884cd04151c27443a4226), which is separate from `main`. See [version and availability](../../versions.md).


# Architecture

One CPRa process owns an Entity-Component-System (ECS) world and three job pipelines. The controller schedules work and consumes results; job workers perform network or local operations.

~~~text
Manifest → validation and entities → controller schedules work
                                      ↓
                           pulse / intervention / code
                                      ↓
                              queues and workers
                                      ↓
                          results update monitor state
                                      ↓
                           snapshots → API / dashboard
~~~

## The three pipelines

| Pipeline | Responsibility |
| --- | --- |
| Pulse | Run the configured health check and return its result. |
| Intervention | Perform an admitted recovery action. |
| Code | Deliver configured incident notifications. |

Each pipeline has a queue and a worker pool. Separating work prevents slow target I/O from becoming controller state mutation inside each network call. Results are committed through the deterministic Raft state machine; the controller owner loop applies the committed projection to its ECS world.

## Scheduling and state

The loader validates YAML or JSON and creates monitor entities and their components. Schedulers identify work that is due; result systems update health, incident, and notification state.

Configuration is loaded before startup. Live reload and runtime queue migration are unsupported. A startup entity-count threshold can select the queue type before workers begin.

## Read-only inspection

An incremental index publishes immutable rows as committed monitor state changes. API filtering and serialization run outside the controller owner loop. Queue and pool telemetry expose worker demand, capacity, observations, and model state.

Normal monitor pages construct only the requested rows; filtered searches scan the index on the request goroutine. Page responses are capped at 500 monitors. This architecture does not itself establish a measured fleet-capacity claim.

## Concurrency and lifecycle

Go contexts and deadlines bound supported operations. Shutdown rejects new admission, drains the controller while diagnostics remain available, then stops HTTP and closes storage within the shutdown deadline. Queues and workers have explicit ownership of completion and cancellation.

CPRa coordinates within one process. Its local Raft state and retained incident history survive restart. An exclusive store lock protects the selected directory; independent directories do not coordinate ownership or provide multi-node failover.

[Worker sizing](queueing-theory.md) · [Incident lifecycle](incident-lifecycle.md) · [Source layout](../developer-guide.md)
