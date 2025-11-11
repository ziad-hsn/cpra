---
layout: default
title: Architecture Overview
parent: Explanation
nav_order: 1
---

# Architecture Overview

CPRA is designed as a multi-stage, concurrent pipeline based on an Entity-Component-System (ECS) architecture. This design allows for a high degree of parallelism and scalability.

## System Design

The application is composed of several independent systems that operate on entities and their components. The main stages of the pipeline are:

1.  **Loading:** The `StreamingLoader` reads monitor configurations from a file and creates an entity for each monitor in the ECS world.
2.  **Scheduling:** The `BatchPulseScheduleSystem` identifies monitors that are due for a health check and flags them for processing.
3.  **Dispatching:** A set of dispatching systems (`BatchPulseSystem`, `BatchInterventionSystem`, `BatchCodeSystem`) enqueue jobs for the flagged entities.
4.  **Execution:** A `DynamicWorkerPool` of goroutines executes the jobs from the queues.
5.  **Result Processing:** A set of result processing systems (`BatchPulseResultSystem`, `BatchInterventionResultSystem`, `BatchCodeResultSystem`) process the results of the jobs and update the state of the entities.

## Data Flow

1.  A YAML or JSON configuration file is parsed by the `StreamingLoader`.
2.  An entity is created for each monitor, populated with components that define its behavior.
3.  The `BatchPulseScheduleSystem` flags the entity for a health check.
4.  The `BatchPulseSystem` enqueues a `PulseJob`.
5.  A worker goroutine executes the job.
6.  The result is sent back to the `BatchPulseResultSystem`.
7.  The `BatchPulseResultSystem` updates the entity's state.
8.  If the monitor is unhealthy, an `InterventionJob` may be enqueued.
9.  If a notification is required, a `CodeJob` is enqueued.

## Concurrency Model

The application is highly concurrent and uses a variety of patterns to achieve high performance:

*   **Goroutine Pools:** To limit the number of concurrent goroutines and reduce overhead.
*   **Queues:** To decouple the different stages of the pipeline.
*   **Lock-Free Data Structures:** For high-performance queueing.
*   **Channels:** For communicating job results.
