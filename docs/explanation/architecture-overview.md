---
title: Architecture Overview
parent: Explanation
---

# Architecture Overview: The ECS Core

CPRA's architecture is built on a high-performance, data-oriented design centered around the **Entity-Component-System (ECS)** pattern. This design choice is fundamental to CPRA's ability to scale to over a million concurrent monitors with minimal memory footprint and high throughput.

## 1. Entity-Component-System (ECS)

CPRA uses the ECS pattern to separate data from behavior, a technique borrowed from high-performance game engines and adapted for infrastructure monitoring. This separation is key to achieving performance at scale.

| Element | Role in CPRA | Technical Implementation |
| :--- | :--- | :--- |
| **Entity** | Represents a single, unique monitor (e.g., a service health check). | A simple integer ID. |
| **Component** | Raw data and configuration associated with a monitor. | Structs like `MonitorState`, `PulseConfig`, and `JobStorage`. |
| **System** | The logic that processes monitors (Entities) that have a specific set of data (Components). | Functions like `BatchPulseSystem` and `BatchInterventionSystem`. |

By storing component data in contiguous memory blocks, ECS ensures that the CPU's cache is used efficiently, allowing the system to process thousands of monitors in a single, fast loop.

## 2. The Three Independent Pipelines

Monitoring tasks are divided into three distinct, concurrent pipelines. This separation is a core design decision that provides **fault isolation** and allows each pipeline to be optimized for its specific workload.

![Pipeline Flow Diagram](../images/pipeline-flow.png)

| Pipeline | Primary Function | Triggered By | Key Characteristic |
| :--- | :--- | :--- | :--- |
| **Pulse** | **Detection** (Health Checks) | Scheduling system (based on `interval`). | High volume, I/O-bound. |
| **Intervention** | **Remediation** (Automated Recovery) | Pulse Pipeline failure (after `unhealthy_threshold`). | Medium volume, requires reliability. |
| **Code** | **Alerting** (Notifications) | Intervention failure or persistent Pulse failure. | Low volume, requires guaranteed delivery. |

Each pipeline operates with its own dedicated queue and worker pool. This means a backlog in the Code (alerting) pipeline will not slow down the critical Pulse (health check) pipeline.

## 3. Data-Oriented Optimizations for Scale

To support 1,000,000+ monitors, CPRA employs advanced memory and data handling techniques:

*   **Component Consolidation:** Instead of many small components, core state is consolidated into components like `MonitorState`. This minimizes the number of unique **Archetypes** in the ECS world, which is a critical factor for iteration speed.
*   **String Interning:** Common strings (like monitor names and check types) are stored once in a global pool. Monitors only store a pointer to the string, drastically reducing the memory overhead per monitor.
*   **Batch Processing:** All Systems operate on large batches of Entities, which maximizes CPU cache utilization and minimizes function call overhead.

## 4. Dynamic Worker Scaling

The worker pools for each pipeline are not statically configured. They are dynamically sized based on **M/M/c queueing theory** to meet a user-defined **Service Level Objective (SLO)**. This ensures that CPRA always provisions the optimal number of workers to handle the current load while guaranteeing that a high percentage of jobs (e.g., P95) are processed within a target latency.

For a detailed explanation of the scaling mechanism, see the [Queueing Theory for Scaling](queueing-theory.md) document.
