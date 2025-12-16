---
title: Welcome to CPRA Documentation
---

<section class="cpra-hero" aria-labelledby="cpra-hero-title">
  <div class="cpra-hero__content">
    <h1 class="cpra-hero__title" id="cpra-hero-title">CPRA</h1>
    <p class="cpra-hero__tagline">High-throughput infrastructure monitoring with automated remediation</p>
    <div class="cpra-hero__stats">
      <div class="stat">
        <span class="stat__value">1M+</span>
        <span class="stat__label">concurrent monitors</span>
      </div>
      <div class="stat">
        <span class="stat__value">&lt;100ms</span>
        <span class="stat__label">P95 latency</span>
      </div>
      <div class="stat">
        <span class="stat__value">~100B</span>
        <span class="stat__label">per monitor</span>
      </div>
    </div>
    <div class="cpra-hero__actions">
      <a class="cpra-button cpra-button--primary" href="tutorials/quickstart/" aria-label="Start the CPRA quickstart tutorial">Quickstart</a>
      <a class="cpra-button cpra-button--secondary" href="reference/config-schema/" aria-label="View the CPRA configuration schema">Reference</a>
    </div>
  </div>
</section>

## Overview

CPRA is a high-performance infrastructure monitoring system designed for large-scale deployments.

- **Data-Oriented ECS Core:** Optimized memory layout for cache-friendly execution at scale.
- **Independent Pipelines:** Separate Pulse (health checks), Intervention (remediation), and Code (alerting) pipelines for fault isolation.
- **Dynamic Worker Scaling:** Worker pools scale automatically using M/M/c queueing theory to meet SLO targets.
- **Extensible Architecture:** Pluggable transports, custom monitors, and remediation recipes.

---

## Documentation at a Glance

| Section | Goal & Purpose | Ready-to-use starting points |
| :--- | :--- | :--- |
| **Tutorials** | Learning-oriented guides to ramp quickly. | [Quickstart](tutorials/quickstart.md), [First Custom Monitor](tutorials/your-first-monitor.md) |
| **How-To Guides** | Problem-driven recipes for day-two operations. | [Deploy to Production](how-to/deploy-to-production.md), [Performance Tuning &amp; SLOs](how-to/performance-tuning.md) |
| **Explanation** | Deep dives that unpack the why behind design choices. | [Architecture Overview](explanation/architecture-overview.md), [Queueing Theory for Scaling](explanation/queueing-theory.md) |
| **Reference** | Precise API and configuration surface area. | [Monitor Configuration Schema](reference/config-schema.md), [API Reference](reference/api-reference.md) |

---

## Core Concepts of v0.5

1. **Entity-Component-System (ECS):** A cache-optimized data model that keeps contention low even at extreme concurrency.
2. **Three Independent Pipelines:** Dedicated Pulse (health checks), Intervention (remediation), and Code (alerting) flows deliver fault isolation.
3. **Dynamic Worker Scaling:** Queue-theory-derived autoscaling ensures SLO commitments during load spikes.

Start with the [Quickstart tutorial](tutorials/quickstart.md) or see the [configuration reference](reference/config-schema.md) for production setup.

