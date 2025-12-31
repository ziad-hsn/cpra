# CPRA Architecture Documentation

This directory contains comprehensive architecture documentation for the CPRA (Concurrent Pulse-Remediation-Alerting) system.

## Documents

| Document | Description |
|----------|-------------|
| [ARCHITECTURE.md](ARCHITECTURE.md) | Complete system architecture, including diagrams, component details, and design decisions |

## Quick Links

### Core Concepts
- [Entity-Component-System (ECS)](ARCHITECTURE.md#entity-component-system-ecs)
- [Three Pipeline Architecture](ARCHITECTURE.md#three-pipeline-architecture)
- [State Machine](ARCHITECTURE.md#state-machine)

### Components
- [MonitorState](ARCHITECTURE.md#monitorstate)
- [PulseConfig](ARCHITECTURE.md#pulseconfig)
- [ColorCode Alert System](ARCHITECTURE.md#colorcode-alert-system)

### Infrastructure
- [Queue System](ARCHITECTURE.md#queue-system)
- [Dynamic Worker Pool](ARCHITECTURE.md#dynamic-worker-pool)
- [Job Execution](ARCHITECTURE.md#job-execution)

### Operations
- [Loading Pipeline](ARCHITECTURE.md#loading-pipeline)
- [Graceful Shutdown](ARCHITECTURE.md#graceful-shutdown)
- [Monitoring & Observability](ARCHITECTURE.md#monitoring--observability)

## Related Documentation

- [Package Documentation](../packages/) - Detailed package-level documentation
- [Developer Guide](../developer-guide.md) - Development workflow and common tasks
- [API Reference](../reference/api-reference.md) - Complete API documentation
- [Tutorials](../tutorials/) - Getting started guides

