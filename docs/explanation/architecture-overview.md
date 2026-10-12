---
title: Architecture
description: How CPRa commits configuration and lifecycle state, schedules work and serves management and observation clients.
cpra_scope: development
---

See [versions and availability](../versions.md) for the current implementation,
qualification and publication boundaries.


# Architecture

One CPRa process owns an Entity-Component-System (ECS) world and three job pipelines. The controller schedules work and consumes results; job workers perform network or local operations.

~~~text
Manifest or management apply → validation → committed configuration
                                                ↓
                                     controller projection
                                      ↓
                           pulse / intervention / code
                                      ↓
                              queues and workers
                                      ↓
                          results commit monitor state
                                      ↓
                       incremental view → API / CLI / dashboard
~~~

## The three pipelines

| Pipeline | Responsibility |
| --- | --- |
| Pulse | Run the configured health check and return its result. |
| Intervention | Perform an admitted recovery action. |
| Code | Deliver configured incident notifications. |

Each pipeline has a queue and a worker pool. Workers perform external I/O on
private job copies. Incident transitions and action intent commit before
dispatch; an external action's started marker commits before invocation.
Results return to the controller owner for committed projection updates.

## Scheduling and state

The controller owner alone reads and changes the Ark ECS world. Raft applies
serializable commands without executable jobs, contexts, channels or provider
calls. The operational projection rebuilds jobs from configuration after restart.
Interrupted started actions become unknown and are held; health checks resume.

Management mode imports its configured bootstrap manifest once. Subsequent
changes use explicit API, CLI, SDK or dashboard operations; editing a source
file does not reload it. Configuration revisions fence queued work from changed
targets. See [configuration management](../cpractl-management.md).

Scheduling uses timing-wheel buckets and overflow deadlines. The owner waits
until the next scheduling or operational deadline when idle, with a one-second
progress fallback. Committed-state changes, worker results and prepared
configuration wake it. Ready work retains the configured update interval;
wakeups carry hints, not permission to execute or mutable ECS state.

## Management and inspection { #read-only-inspection }

Named operators manage monitors, notification contacts/groups, protected
credentials and incident controls through authenticated v2 operations. Reader
identities observe their granted resources. HTTP handlers never mutate the ECS
world directly. Probe requests inspect process/storage/controller health and
perform no provider operations.

An incremental index publishes immutable monitor rows as committed state
changes. API filtering and serialization run outside the owner loop, and normal
pages construct only the requested rows. History, queue saturation, worker
capacity and measured SLO windows have separate bounded observations. This
architecture alone does not establish a measured fleet-capacity claim.

## Concurrency and lifecycle

Go contexts and deadlines bound supported operations. Shutdown rejects new
admission, drains or cancels accepted work while diagnostics remain available,
commits known outcomes and closes dependencies. Ambiguous external effects
remain held. Queues, background preparation and workers have explicit completion
and cancellation owners.

Single-node Raft restores committed state, with an exclusive lock protecting
the selected data directory. Thirty-day incident/action history lives alongside
it and belongs in a complete stopped backup. Separate data directories do not
coordinate one fleet, and single-node Raft provides no distributed failover.
Configuration operators are trusted to choose provider destinations; CPRa does
not provide multi-tenant credential/destination isolation.

[Worker sizing](queueing-theory.md) · [Incident lifecycle](incident-lifecycle.md) · [Source layout](../developer-guide.md)
