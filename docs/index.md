---
title: Welcome to CPRA Documentation
---

<section class="cpra-hero" aria-labelledby="cpra-hero-title">
  <div class="cpra-hero__content">
    <p class="cpra-hero__eyebrow">CPRA Documentation</p>
    <h1 class="cpra-hero__title" id="cpra-hero-title">Operate at planetary scale with confident remediation.</h1>
    <p class="cpra-hero__lede">Concurrent Pulse-Remediation-Alerting (CPRA) delivers ultra-low latency health checks, automated remediation, and resilient alerting pipelines tuned by queueing theory. Explore proven playbooks, deep architecture notes, and reference-grade APIs to master the platform.</p>
    <div class="cpra-hero__actions">
      <a class="cpra-hero__button cpra-hero__button--primary" href="tutorials/quickstart/" aria-label="Start the CPRA quickstart tutorial">Start the Quickstart</a>
      <a class="cpra-hero__button cpra-hero__button--secondary" href="reference/config-schema/" aria-label="View the CPRA configuration schema">Explore the Reference</a>
    </div>
  </div>
  <div class="cpra-hero__graphic" aria-hidden="true">
    <ul class="cpra-hero__list">
      <li class="cpra-hero__metric"><span>1M+</span> concurrent health checks per cluster</li>
      <li class="cpra-hero__metric"><span>&lt;120ms</span> remediation dispatch median</li>
      <li class="cpra-hero__metric"><span>99.95%</span> SLO-backed event delivery</li>
    </ul>
  </div>
</section>

## Why CPRA

CPRA is a high-performance infrastructure monitoring system engineered for teams that demand both scale and precision.

- **Data-Oriented ECS Core:** Optimized memory layout drives cache-friendly execution across massive fleets.
- **Independent Pulse / Intervention / Code Pipelines:** Tune and isolate workloads without cross-impact.
- **Scientifically Tuned SLOs:** Worker pools scale dynamically using M/M/c queueing theory for predictable latency.
- **Built for Extensibility:** Compose remediation recipes, pluggable transports, and custom monitors with safety rails.

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

Ready to dive deeper? Start with the [Quickstart tutorial](tutorials/quickstart.md), or jump into the [configuration reference](reference/config-schema.md) when you are wiring CPRA into production.
