---
title: Performance Tuning and SLOs
parent: How-to Guides
---

# Performance Tuning and SLOs

CPRA's performance is driven by its ability to dynamically size its worker pools to meet a Service Level Objective (SLO). This guide explains how to configure and tune these settings for optimal performance in your environment.

## 1. Understanding the SLO

The SLO in CPRA is defined as the target latency for job processing in each pipeline. By default, the target is **100ms P95**, meaning 95% of all jobs should be processed within 100 milliseconds.

The worker pool for each pipeline (Pulse, Intervention, Code) is independently sized to meet this SLO based on the principles of M/M/c queueing theory.

## 2. Configuring the Global Controller

The primary configuration for performance tuning is done in the `ControllerConfig` struct when initializing CPRA.

| Configuration Field | Description | Tuning Impact |
| :--- | :--- | :--- |
| `SLOTargetMs` | The target latency in milliseconds for P95 job completion. | **Lowering** this value forces the system to provision **more workers** and can increase resource consumption. |
| `WorkerConfig.MinWorkers` | The minimum number of workers to keep active, even under no load. | Prevents cold start latency. Set to a small number (e.g., 10). |
| `WorkerConfig.MaxWorkers` | The absolute maximum number of workers the pool can scale to. | Acts as a safety limit to prevent resource exhaustion during extreme load spikes. |
| `BatchSize` | The number of entities processed by a System in a single iteration. | **Increasing** this value can improve throughput but may increase the latency of individual state updates. |

## 3. Tuning the Pulse Pipeline

The Pulse pipeline is the most critical and highest-volume pipeline. Its performance is heavily influenced by the network latency of the health checks themselves.

*   **Monitor Interval (`interval`):** A shorter interval increases the job arrival rate ($\lambda$), which forces the worker pool to scale up.
*   **Monitor Timeout (`timeout`):** A longer timeout increases the average service time ($\mu$), which also forces the worker pool to scale up.

**Best Practice:** Set the `timeout` to be as short as possible. A long timeout directly increases the service time, which is the most significant factor in the M/M/c calculation.

## 4. Monitoring and Profiling

To validate your tuning efforts, use the built-in profiling tools:

1.  **Enable Profiling:** Start CPRA with the `--pprof` flag.
2.  **Access Metrics:** The profiling server will be available at `http://localhost:6060/debug/pprof/`.
3.  **Analyze Worker Pool Metrics:** Pay close attention to the `queue_size` and `worker_count` metrics exposed by the controller. If the `queue_size` is consistently high, it indicates that the worker pool is undersized for the current load and SLO target.

**Tip:** If you observe high CPU utilization but low throughput, consider increasing the `BatchSize` to reduce system overhead. If you observe high latency, consider lowering the `SLOTargetMs` (if resources allow) or increasing the `MaxWorkers`.

---

### **Next Steps**

*   **[Queueing Theory for Dynamic Scaling](explanation/queueing-theory.md)**: Understand the mathematical model behind the dynamic scaling.
*   **[Monitor Configuration Schema](reference/config-schema.md)**: Review the configuration fields that affect job arrival and service rates.
