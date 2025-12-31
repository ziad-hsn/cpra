# CPRA Documentation Index

## Overview

This document provides a comprehensive index of all documentation available for the CPRA (Concurrent Pulse-Remediation-Alerting) system.

## Documentation Structure

```
docs/
├── DOCUMENTATION_INDEX.md     # This file - master index
├── developer-guide.md         # Developer workflow and common tasks
├── index.md                   # Documentation home
│
├── architecture/              # System architecture
│   ├── README.md              # Architecture overview
│   └── ARCHITECTURE.md        # Complete architecture documentation
│
├── packages/                  # Package-level documentation
│   ├── README.md              # Package index
│   ├── controller.md          # Controller package docs
│   ├── jobs.md                # Jobs package docs
│   ├── queue.md               # Queue package docs
│   ├── loader.md              # Loader package docs
│   └── logger.md              # Logger package docs
│
├── explanation/               # Conceptual documentation
│   └── architecture-overview.md
│
├── tutorials/                 # Getting started guides
│   ├── quickstart.md
│   └── getting-started.md
│
├── how-to/                    # Task-oriented guides
│   └── common-tasks.md
│
├── reference/                 # API reference
│   ├── api-reference.md
│   └── types-reference.md
│
└── images/                    # Diagrams and images
```

## Quick Start

| Goal | Document |
|------|----------|
| Understand the system | [Architecture Overview](architecture/ARCHITECTURE.md) |
| Start developing | [Developer Guide](developer-guide.md) |
| Learn package APIs | [Package Documentation](packages/README.md) |
| Get running quickly | [Quickstart Tutorial](tutorials/quickstart.md) |

## By Topic

### Architecture & Design

| Topic | Document | Description |
|-------|----------|-------------|
| System Overview | [ARCHITECTURE.md](architecture/ARCHITECTURE.md) | Complete system architecture |
| ECS Design | [ARCHITECTURE.md#entity-component-system-ecs](architecture/ARCHITECTURE.md#entity-component-system-ecs) | Entity-Component-System patterns |
| Pipeline Architecture | [ARCHITECTURE.md#three-pipeline-architecture](architecture/ARCHITECTURE.md#three-pipeline-architecture) | Pulse, Intervention, Code pipelines |
| State Machine | [ARCHITECTURE.md#state-machine](architecture/ARCHITECTURE.md#state-machine) | Monitor state transitions |

### Package Documentation

| Package | Document | Description |
|---------|----------|-------------|
| controller | [controller.md](packages/controller.md) | Core ECS orchestration |
| jobs | [jobs.md](packages/jobs.md) | Job types and execution |
| queue | [queue.md](packages/queue.md) | Queue implementations and worker pools |
| loader | [loader.md](packages/loader.md) | Configuration loading |
| logger | [logger.md](packages/logger.md) | Logging infrastructure |

### Development

| Topic | Document | Description |
|-------|----------|-------------|
| Getting Started | [Developer Guide](developer-guide.md) | Development workflow |
| Adding Jobs | [Developer Guide#adding-a-new-pulse-type](developer-guide.md#adding-a-new-pulse-type) | Creating new job types |
| Testing | [Developer Guide#testing](developer-guide.md#testing) | Test strategies |
| Profiling | [Developer Guide#profiling](developer-guide.md#profiling) | Performance analysis |

### Operations

| Topic | Document | Description |
|-------|----------|-------------|
| Configuration | [README.md](../README.md#configuration) | YAML configuration |
| Command Line | [README.md](../README.md#command-line-options) | CLI options |
| Troubleshooting | [README.md](../README.md#troubleshooting) | Common issues |
| GC Tuning | [ARCHITECTURE.md](architecture/ARCHITECTURE.md) | Memory optimization |

## Source Code Documentation

The following source files contain comprehensive doc comments:

### Core Packages

| File | Description |
|------|-------------|
| `internal/controller/controller.go` | Controller type and lifecycle |
| `internal/controller/doc.go` | Package overview |
| `internal/controller/components/components.go` | ECS component definitions |
| `internal/controller/entities/mapper.go` | Entity management |
| `internal/controller/systems/*.go` | ECS system implementations |

### Jobs Package

| File | Description |
|------|-------------|
| `internal/jobs/doc.go` | Package overview with examples |
| `internal/jobs/types.go` | Job interface and Result type |
| `internal/jobs/base.go` | BaseJob and BaseNetworkJob |
| `internal/jobs/factory.go` | Job creation functions |
| `internal/jobs/TEMPLATE.go` | Template for new job types |

### Queue Package

| File | Description |
|------|-------------|
| `internal/queue/doc.go` | Package overview |
| `internal/queue/queue.go` | Queue interface |
| `internal/queue/hybrid_queue.go` | Primary queue implementation |
| `internal/queue/dynamic_worker_pool.go` | Worker pool with auto-scaling |
| `internal/queue/sizing.go` | M/M/c queueing theory |

### Loader Package

| File | Description |
|------|-------------|
| `internal/loader/pipeline.go` | Concurrent loading pipeline |
| `internal/loader/types.go` | Pipeline types and config |
| `internal/loader/schema/schema.go` | YAML schema definitions |

### Logger Package

| File | Description |
|------|-------------|
| `internal/logger/logger.go` | Logger interface |
| `internal/logger/factory.go` | Logger creation |
| `internal/logger/config.go` | Configuration |

## Generating Documentation

### Go Doc

```bash
# View package documentation
go doc cpra/internal/controller
go doc cpra/internal/jobs
go doc cpra/internal/queue

# View specific type
go doc cpra/internal/controller.Controller
go doc cpra/internal/jobs.Job

# View all exported symbols
go doc -all cpra/internal/controller
```

### MkDocs (if configured)

```bash
# Serve documentation locally
mkdocs serve

# Build static site
mkdocs build
```

## Contributing to Documentation

1. **Source Code Docs**: Update doc comments in `.go` files
2. **Package Docs**: Update files in `docs/packages/`
3. **Architecture Docs**: Update `docs/architecture/ARCHITECTURE.md`
4. **Developer Guide**: Update `docs/developer-guide.md`

### Documentation Standards

- Use Go doc comment conventions
- Include examples in doc comments
- Keep package docs in sync with source
- Update this index when adding new docs

