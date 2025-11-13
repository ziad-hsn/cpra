---
title: Welcome to CPRA Documentation
---

# Welcome to CPRA Documentation

**CPRA (Concurrent Pulse-Remediation-Alerting)** is a high-performance infrastructure monitoring system designed for massive scale. Built on a data-oriented Entity-Component-System (ECS) architecture and leveraging advanced queueing theory, CPRA can handle over a million concurrent health checks with dynamic worker scaling to meet strict Service Level Objectives (SLOs).

This documentation is structured to help you learn, use, and understand CPRA, whether you are a first-time user or an advanced developer.

---

### **Documentation Structure**

This site is organized into four key sections based on the [Diátaxis framework](https://diataxis.fr/), ensuring you can find the right information for your needs.

| Section | Goal & Purpose | Key Content |
| :--- | :--- | :--- |
| **Tutorials** | **Learning-oriented lessons** to guide you from novice to proficient. | [Quickstart](tutorials/quickstart.md), [Your First Custom Monitor](tutorials/your-first-monitor.md) |
| **How-To Guides** | **Problem-oriented recipes** to help you accomplish specific, real-world tasks. | [Deploying to Production](how-to/deploy-to-production.md), [Performance Tuning](how-to/performance-tuning.md) |
| **Explanation** | **Understanding-oriented discussions** to clarify the *why* behind the architecture. | [Architecture Overview](explanation/architecture-overview.md), [Queueing Theory for Scaling](explanation/queueing-theory.md) |
| **Reference** | **Information-oriented descriptions** of the technical machinery. | [Monitor Config Schema](reference/config-schema.md), [CLI Reference](reference/cli-reference.md) |

---

### **Core Concepts of v0.5**

The `v0.5` release introduces a powerful new architecture. Understanding these concepts is key to leveraging CPRA effectively.

*   **Entity-Component-System (ECS):** A data-oriented design pattern that enables massive scalability and performance by organizing data for optimal CPU cache usage.
*   **Three Independent Pipelines:** Separate, concurrent pipelines for **Pulse** (health checks), **Intervention** (remediation), and **Code** (alerting) provide fault isolation and independent tuning.
*   **Dynamic Worker Scaling:** Worker pools automatically scale based on **M/M/c queueing theory** to meet user-defined latency targets (SLOs), ensuring performance under varying loads.

To get started, we recommend following the [**Quickstart Tutorial**](tutorials/quickstart.md).
