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

1.  **Measurement:** The system continuously measures the current job arrival rate ($\lambda$) and the average service time ($\mu$) for each pipeline.
2.  **Calculation:** The **State Logger System** feeds this data into the M/M/c model (with the Allen-Cunneen approximation) to calculate the required number of workers ($c_{required}$) to meet the SLO.
3.  **Adjustment:** The **Dynamic Worker Pool** adjusts its size (scaling up or down) to match $c_{required}$, ensuring resources are neither wasted nor insufficient.

This intelligent, mathematically-grounded approach is what allows CPRA to guarantee performance targets even as the monitored environment and load fluctuate.

---

### **Next Steps**

*   **[Performance Tuning & SLOs](how-to/performance-tuning.md)**: Learn how to configure and tune the SLO targets for your environment.
*   **[Monitor Configuration Schema](reference/config-schema.md)**: See where to define the `interval` and `timeout` that feed into the $\lambda$ and $\mu$ calculations.
