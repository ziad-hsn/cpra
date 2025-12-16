---
title: Queueing Theory for Dynamic Scaling
parent: Explanation
---

# Queueing Theory for Dynamic Scaling

One of CPRA's most advanced features is its ability to dynamically size its worker pools to meet a guaranteed Service Level Objective (SLO). This is achieved by applying principles from **Queueing Theory**, specifically the **M/M/c model**.

## The M/M/c Queueing Model

The M/M/c model is a mathematical framework used to analyze a system with:

*   **M (Markovian Arrival):** Job arrivals follow a Poisson process (random, independent arrivals).
*   **M (Markovian Service):** Job service times follow an exponential distribution.
*   **c (Servers):** A fixed number of parallel servers (workers).

In CPRA's context:

*   **Jobs ($\lambda$):** The rate at which monitors are ready for processing (e.g., Pulse checks).
*   **Workers ($c$):** The number of goroutines in the worker pool.
*   **Service Time ($\mu$):** The time it takes a single worker to complete a job (e.g., an HTTP check).

The goal is to find the minimum number of workers ($c$) required to ensure that the probability of a job waiting longer than the defined SLO is below a certain tolerance (e.g., 5%).

## SLO-Driven Worker Sizing

CPRA uses the M/M/c model to calculate the optimal worker count based on the following inputs:

1.  **Arrival Rate ($\lambda$):** Calculated from the total number of monitors and their check intervals.
2.  **Service Rate ($\mu$):** Estimated from historical job execution times.
3.  **Service Level Objective (SLO):** The maximum acceptable latency (e.g., 100ms P95).

### The Allen-Cunneen Approximation

Real-world monitoring tasks often do not perfectly fit the "Markovian" (exponential) service time assumption. To account for the variability in real-world workloads (e.g., network jitter, slow APIs), CPRA uses the **Allen-Cunneen approximation** (also known as the $M/G/c$ model approximation).

This approximation introduces the **Coefficient of Variation ($C_s$)** for service time, allowing the model to handle more general service time distributions. This makes the dynamic scaling far more robust and accurate in a production environment.

## Dynamic Scaling in Practice

### Multi-Window Metrics Collection

CPRA collects metrics across three time windows to balance responsiveness with stability:

| Window | Duration | Purpose |
| :--- | :--- | :--- |
| **Short** | 15 seconds | Spike detection, triggers scale-up |
| **Medium** | 5 minutes | Trend detection |
| **Long** | 30 minutes | Baseline, used for scale-down decisions |

### Scaling Algorithm

1. **Measurement:** The `ScalingMetrics` collector continuously tracks:
   - Enqueue rate ($\lambda$)
   - Queue depth
   - Worker utilization
   - Inter-arrival time variance ($C_a$)
   - Service time variance ($C_s$)

2. **Calculation:** The M/M/c model with Allen-Cunneen approximation computes the optimal worker count:
   ```
   c_optimal = FindCForSLO(λ, τ, W_target, Ca, Cs, c_max)
   c_safe = c_optimal × 1.15  // 15% headroom
   ```

3. **Hysteresis:** To prevent oscillation, scaling only occurs when:
   - **Scale-up:** Desired workers > current × 1.10 (10% threshold)
   - **Scale-down:** Desired workers < current × 0.80 (20% threshold)

4. **Cooldowns:** Asymmetric cooldowns prevent rapid changes:
   - Scale-up cooldown: 30 seconds (react quickly to load)
   - Scale-down cooldown: 120 seconds (conservative reduction)

5. **Scale-down Safety:** Scale-down only triggers after sustained low utilization (< 25%) over the 30-minute long window.

### Warmup Period

During the first 60 seconds after startup, no scaling occurs. This allows:
- Initial metrics to stabilize
- Worker pools to warm up
- Variability coefficients to be measured

This mathematically-grounded approach allows CPRA to guarantee performance targets even as the monitored environment and load fluctuate.

---

### **Next Steps**

*   **[Performance Tuning & SLOs](../how-to/performance-tuning.md)**: Learn how to configure and tune the SLO targets for your environment.
*   **[Monitor Configuration Schema](../reference/config-schema.md)**: See where to define the `interval` and `timeout` that feed into the $\lambda$ and $\mu$ calculations.
