# CPRA Package Documentation

This directory contains detailed documentation for each major package in the CPRA codebase.

## Packages

| Package | Description | Documentation |
|---------|-------------|---------------|
| `controller` | Core ECS-based controller orchestration | [controller.md](controller.md) |
| `jobs` | Job types and execution logic | [jobs.md](jobs.md) |
| `queue` | Queue implementations and worker pools | [queue.md](queue.md) |
| `loader` | Configuration loading pipeline | [loader.md](loader.md) |
| `logger` | Structured logging interface | [logger.md](logger.md) |

## Package Hierarchy

```
internal/
├── controller/           # Core orchestration
│   ├── components/       # ECS components (MonitorState, configs)
│   ├── entities/         # Entity management
│   └── systems/          # ECS systems (BatchPulseSystem, etc.)
├── jobs/                 # Job types and execution
├── queue/                # Queue implementations
├── loader/               # Configuration loading
│   └── schema/           # YAML schema definitions
├── logger/               # Logging infrastructure
└── interning/            # String interning for memory efficiency
```

## Key Interfaces

### Job Interface (`jobs.Job`)
All job types implement this interface for execution by worker pools.

### Queue Interface (`queue.Queue`)
All queue implementations provide this interface for job storage.

### Logger Interface (`logger.Logger`)
Structured logging interface used throughout the codebase.

## Getting Started

1. Start with [controller.md](controller.md) to understand the overall architecture
2. Read [jobs.md](jobs.md) to understand how work is executed
3. Review [queue.md](queue.md) for the queueing and scaling mechanisms
4. See [loader.md](loader.md) for configuration loading
5. Check [logger.md](logger.md) for logging conventions

## Related Documentation

- [Architecture Overview](../architecture/ARCHITECTURE.md)
- [Developer Guide](../developer-guide.md)
- [API Reference](../reference/api-reference.md)

