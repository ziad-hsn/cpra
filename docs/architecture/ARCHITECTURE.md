# CPRA Architecture Documentation

## Overview

CPRA (Concurrent Pulse-Remediation-Alerting) is a high-performance infrastructure monitoring system designed for platform teams managing large-scale microservice architectures. Built on Entity-Component-System (ECS) architecture and queueing theory principles, CPRA handles 1,000,000+ concurrent health checks with automatic worker pool scaling to meet SLO targets.

## System Architecture

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              CPRA Controller                                     │
│  ┌─────────────────────────────────────────────────────────────────────────┐   │
│  │                         ECS World (ark)                                   │   │
│  │  ┌──────────────────────────────────────────────────────────────────┐   │   │
│  │  │                         Entities                                   │   │   │
│  │  │  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌────────────┐    │   │   │
│  │  │  │ Monitor 1  │ │ Monitor 2  │ │ Monitor 3  │ │ Monitor N  │    │   │   │
│  │  │  └────────────┘ └────────────┘ └────────────┘ └────────────┘    │   │   │
│  │  └──────────────────────────────────────────────────────────────────┘   │   │
│  │                                                                           │   │
│  │  ┌──────────────────────────────────────────────────────────────────┐   │   │
│  │  │                        Components                                  │   │   │
│  │  │  MonitorState │ PulseConfig │ InterventionConfig │ CodeConfig    │   │   │
│  │  │  JobStorage   │ Shard       │ CodeStatus         │ Disabled      │   │   │
│  │  └──────────────────────────────────────────────────────────────────┘   │   │
│  └─────────────────────────────────────────────────────────────────────────┘   │
│                                                                                  │
│  ┌─────────────────────────────────────────────────────────────────────────┐   │
│  │                           ECS Systems                                     │   │
│  │  ┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐            │   │
│  │  │ BatchPulseSystem│ │BatchIntervention│ │ BatchCodeSystem │            │   │
│  │  │                 │ │     System      │ │                 │            │   │
│  │  └────────┬────────┘ └────────┬────────┘ └────────┬────────┘            │   │
│  │           │                   │                   │                      │   │
│  │  ┌────────▼────────┐ ┌────────▼────────┐ ┌────────▼────────┐            │   │
│  │  │BatchPulseResult │ │BatchIntervention│ │BatchCodeResult  │            │   │
│  │  │     System      │ │  ResultSystem   │ │     System      │            │   │
│  │  └─────────────────┘ └─────────────────┘ └─────────────────┘            │   │
│  └─────────────────────────────────────────────────────────────────────────┘   │
│                                                                                  │
│  ┌──────────────────────────────────────────────────────────────────────────┐  │
│  │                        Three Pipeline Architecture                        │  │
│  │                                                                            │  │
│  │  ┌─────────────────────────────────────────────────────────────────────┐ │  │
│  │  │ PULSE PIPELINE                                                       │ │  │
│  │  │ ┌──────────────┐    ┌───────────────────┐    ┌──────────────────┐  │ │  │
│  │  │ │ HybridQueue  │───▶│DynamicWorkerPool  │───▶│   ResultRouter   │  │ │  │
│  │  │ │   (pulse)    │    │ (ants goroutines) │    │ (PulseResultChan)│  │ │  │
│  │  │ └──────────────┘    └───────────────────┘    └──────────────────┘  │ │  │
│  │  └─────────────────────────────────────────────────────────────────────┘ │  │
│  │                                                                            │  │
│  │  ┌─────────────────────────────────────────────────────────────────────┐ │  │
│  │  │ INTERVENTION PIPELINE                                                │ │  │
│  │  │ ┌──────────────┐    ┌───────────────────┐    ┌──────────────────┐  │ │  │
│  │  │ │ HybridQueue  │───▶│DynamicWorkerPool  │───▶│   ResultRouter   │  │ │  │
│  │  │ │(intervention)│    │ (ants goroutines) │    │(InterventionChan)│  │ │  │
│  │  │ └──────────────┘    └───────────────────┘    └──────────────────┘  │ │  │
│  │  └─────────────────────────────────────────────────────────────────────┘ │  │
│  │                                                                            │  │
│  │  ┌─────────────────────────────────────────────────────────────────────┐ │  │
│  │  │ CODE PIPELINE                                                        │ │  │
│  │  │ ┌──────────────┐    ┌───────────────────┐    ┌──────────────────┐  │ │  │
│  │  │ │ HybridQueue  │───▶│DynamicWorkerPool  │───▶│   ResultRouter   │  │ │  │
│  │  │ │    (code)    │    │ (ants goroutines) │    │  (CodeResultChan)│  │ │  │
│  │  │ └──────────────┘    └───────────────────┘    └──────────────────┘  │ │  │
│  │  └─────────────────────────────────────────────────────────────────────┘ │  │
│  └──────────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────────────┘
```

## Package Structure

```
cpra/
├── main.go                    # Application entry point
├── internal/
│   ├── controller/            # Core ECS controller orchestration
│   │   ├── controller.go      # Main Controller type and lifecycle
│   │   ├── components/        # ECS component definitions
│   │   │   ├── components.go  # MonitorState, configs, jobs
│   │   │   └── config_registry.go # Shared config deduplication
│   │   ├── entities/          # Entity management
│   │   │   ├── mapper.go      # EntityManager for entity creation
│   │   │   └── pools.go       # Object pools for components
│   │   ├── systems/           # ECS systems
│   │   │   ├── batch_pulse_system.go
│   │   │   ├── batch_pulse_result_system.go
│   │   │   ├── batch_intervention_system.go
│   │   │   ├── batch_intervention_result_system.go
│   │   │   ├── batch_code_system.go
│   │   │   ├── batch_code_result_system.go
│   │   │   ├── termination_system.go
│   │   │   └── state_logger.go
│   │   ├── watchdog.go        # Health monitoring
│   │   ├── logger.go          # Controller loggers
│   │   ├── metrics.go         # Metrics collection
│   │   ├── memory.go          # Memory management
│   │   ├── recovery.go        # Panic recovery
│   │   └── tracing.go         # Distributed tracing
│   │
│   ├── jobs/                  # Job types and execution
│   │   ├── types.go           # Job interface, Result struct
│   │   ├── base.go            # BaseJob, BaseNetworkJob
│   │   ├── factory.go         # CreatePulseJob, CreateInterventionJob, CreateCodeJob
│   │   ├── pool.go            # sync.Pool for job reuse
│   │   ├── dial_limiter.go    # Global rate limiting
│   │   ├── pulse_http.go      # HTTP health checks
│   │   ├── pulse_tcp.go       # TCP connection checks
│   │   ├── pulse_icmp.go      # ICMP ping checks
│   │   ├── intervention_docker.go # Docker interventions
│   │   ├── code_log.go        # Log file alerts
│   │   ├── code_slack.go      # Slack notifications
│   │   ├── code_pagerduty.go  # PagerDuty alerts
│   │   ├── code_email.go      # Email notifications
│   │   ├── code_webhook.go    # Webhook notifications
│   │   ├── pool_http.go       # fasthttp client pool
│   │   ├── pool_tcp.go        # TCP dialer pool
│   │   ├── pool_docker.go     # Docker client pool
│   │   └── log_writer.go      # Async log writer
│   │
│   ├── queue/                 # Queue implementations
│   │   ├── queue.go           # Queue interface
│   │   ├── hybrid_queue.go    # Lock-free ring + overflow
│   │   ├── adaptive_queue.go  # Auto-scaling queue
│   │   ├── workiva_queue.go   # Workiva RingBuffer
│   │   ├── bounded_queue.go   # Simple bounded queue
│   │   ├── queue_factory.go   # Queue creation
│   │   ├── dynamic_worker_pool.go # ants-based worker pool
│   │   ├── sizing.go          # M/M/c queueing theory
│   │   └── metrics.go         # Queue metrics
│   │
│   ├── loader/                # Configuration loading
│   │   ├── pipeline.go        # Concurrent loading pipeline
│   │   ├── types.go           # Pipeline types
│   │   ├── validator.go       # Monitor validation
│   │   ├── progress.go        # Progress reporting
│   │   └── schema/            # YAML schema definitions
│   │       ├── schema.go      # Monitor, Pulse, Intervention
│   │       └── manifest.go    # File manifest
│   │
│   ├── logger/                # Logging infrastructure
│   │   ├── logger.go          # Logger interface
│   │   ├── zap_logger.go      # Zap implementation
│   │   ├── factory.go         # Logger creation
│   │   ├── config.go          # Logger configuration
│   │   └── context.go         # Context logging
│   │
│   └── interning/             # String interning
│       └── interning.go       # Memory-efficient strings
│
├── cmd/                       # Command-line tools
│   ├── yaml_to_json.go        # Config converter
│   └── verify_config/         # Config validator
│
└── mock-servers/              # Testing infrastructure
    ├── main.go                # Mock server entry
    └── virtual_service.go     # Virtual service simulation
```

## Core Concepts

### Entity-Component-System (ECS)

CPRA uses the [mlange-42/ark](https://github.com/mlange-42/ark) ECS library for data-oriented design:

- **Entities**: Unique identifiers for monitors (lightweight integer IDs)
- **Components**: Data attached to entities (MonitorState, PulseConfig, etc.)
- **Systems**: Logic that operates on entities with specific component combinations

Benefits:
- Cache-friendly memory layout for maximum iteration speed
- Minimal allocations through value-type components
- Efficient batch processing of millions of entities

### Three Pipeline Architecture

CPRA processes work through three independent pipelines:

| Pipeline | Purpose | Job Types | Drop Policy |
|----------|---------|-----------|-------------|
| **Pulse** | Health checking | HTTP, TCP, ICMP | DropNewest |
| **Intervention** | Automated recovery | Docker restart/stop/start/kill/pause/unpause/scale | DropOldest |
| **Code** | Alert notifications | Log, Slack, PagerDuty, Email, Webhook | DropNewest |

Each pipeline has:
- Its own `HybridQueue` for job storage
- Its own `DynamicWorkerPool` for execution
- Its own `ResultRouter` for result delivery

### State Machine

Monitor entities follow a state machine pattern using bitflags:

```
                    ┌──────────────────────────────────────────────────┐
                    │                    IDLE                          │
                    │         (no flags set, waiting for interval)     │
                    └──────────────────────────────────────────────────┘
                                           │
                                           │ interval elapsed
                                           ▼
                    ┌──────────────────────────────────────────────────┐
                    │              PULSE_FIRST_CHECK                   │
                    │          (initial check on startup)              │
                    └──────────────────────────────────────────────────┘
                                           │
                                           │ job enqueued
                                           ▼
                    ┌──────────────────────────────────────────────────┐
                    │               PULSE_PENDING                      │
                    │         (waiting for job result)                 │
                    └──────────────────────────────────────────────────┘
                                           │
                          ┌────────────────┴────────────────┐
                          │                                 │
                          ▼ success                         ▼ failure
                    ┌──────────────┐                 ┌──────────────┐
                    │   HEALTHY    │                 │   UNHEALTHY  │
                    │ (green code) │                 │(yellow code) │
                    └──────────────┘                 └──────────────┘
                                                            │
                                                            │ threshold exceeded
                                                            ▼
                    ┌──────────────────────────────────────────────────┐
                    │            INTERVENTION_NEEDED                   │
                    │         (recovery action required)               │
                    └──────────────────────────────────────────────────┘
                                           │
                                           │ job enqueued
                                           ▼
                    ┌──────────────────────────────────────────────────┐
                    │           INTERVENTION_PENDING                   │
                    │         (waiting for recovery)                   │
                    └──────────────────────────────────────────────────┘
                                           │
                          ┌────────────────┴────────────────┐
                          │                                 │
                          ▼ success                         ▼ failure
                    ┌──────────────┐                 ┌──────────────┐
                    │  VERIFYING   │                 │ INCIDENT_OPEN│
                    │(post-check)  │                 │  (red code)  │
                    └──────────────┘                 └──────────────┘
```

## Component Details

### MonitorState

The central state component for all monitors:

```go
type MonitorState struct {
    LastPulseCheckTime   time.Time     // When last pulse was executed
    LastEventTime        time.Time     // When last state change occurred
    LastSuccessTime      time.Time     // When last successful check occurred
    NextCheckTime        time.Time     // When next check is due
    LastError            error         // Most recent error
    Name                 string        // Monitor name (interned)
    ConsecutiveFailures  int           // Current failure streak
    PulseFailures        int           // Failures since last success
    InterventionFailures int           // Failed intervention attempts
    RecoveryStreak       int           // Consecutive successes after failure
    VerifyRemaining      int           // Remaining verification checks
    Flags                uint32        // State bitflags
    PendingColor         ColorCode     // Alert color to dispatch
}
```

State flags (bitfield for efficiency):
- `StatePulseNeeded` (1<<1): Pulse check is due
- `StatePulsePending` (1<<2): Pulse job is in-flight
- `StatePulseFirstCheck` (1<<3): First check after startup
- `StateInterventionNeeded` (1<<5): Intervention required
- `StateInterventionPending` (1<<6): Intervention in-flight
- `StateCodeNeeded` (1<<7): Alert dispatch required
- `StateCodePending` (1<<8): Alert in-flight
- `StateIncidentOpen` (1<<9): Active incident
- `StateVerifying` (1<<10): Post-intervention verification

### PulseConfig

Configuration for health checks:

```go
type PulseConfig struct {
    Config             schema.PulseConfig // HTTP/TCP/ICMP specific config
    Type               string             // "http", "tcp", "icmp"
    Timeout            time.Duration      // Check timeout
    Interval           time.Duration      // Check frequency
    Retries            int                // Retry attempts
    UnhealthyThreshold int                // Failures before intervention
    HealthyThreshold   int                // Successes to recover
}
```

### ColorCode Alert System

CPRA uses a color-coded alert system with priority levels:

| Color | Priority | Meaning |
|-------|----------|---------|
| Red | 5 (highest) | Critical failure, incident opened |
| Orange | 4 | Severe warning |
| Yellow | 4 | First failure detected |
| Green | 2 | Recovery confirmed |
| Cyan | 2 | Informational |
| Blue | 1 | Debug/trace |
| Purple | 1 | Custom |
| Gray | 0 (lowest) | Maintenance |

## Queue System

### HybridQueue

The primary queue implementation combines:
- **Lock-free ring buffer** (xsync.MPMCQueue): Fast path for steady-state
- **Mutex-protected overflow slice**: Absorbs bursts before dropping

Features:
- Configurable drop policies (Reject, DropNewest, DropOldest)
- Soft/hard watermarks for backpressure signaling
- Comprehensive metrics (enqueue rate, dequeue rate, wait times)

### Dynamic Worker Pool

Built on [panjf2000/ants](https://github.com/panjf2000/ants):

- **M/M/c queueing theory** for optimal worker sizing
- **Allen-Cunneen approximation** for variability handling
- **Asymmetric cooldowns**: Fast scale-up (30s), slow scale-down (120s)
- **Hysteresis thresholds**: Prevents oscillation (10% up, 20% down)

Worker sizing formula:
```
c_min = FindCForSLO(λ, τ, W_target)
c_safe = ceil(c_min * 1.15)  // 15% headroom
```

Where:
- λ = arrival rate (jobs/second)
- τ = service time (seconds/job)
- W_target = SLO latency target (seconds)

## Job Execution

### Job Interface

All jobs implement:

```go
type Job interface {
    Execute(ctx context.Context) Result
    Copy() Job
    GetEnqueueTime() time.Time
    SetEnqueueTime(time.Time)
    GetStartTime() time.Time
    SetStartTime(time.Time)
    IsNil() bool
}
```

### Safety Guardrails

1. **Global Dial Limiter**: Prevents CPU spikes during network outages
2. **Context Cancellation**: Enables graceful shutdown
3. **Retry with Backoff**: 50ms delays between attempts
4. **Object Pooling**: sync.Pool reduces GC pressure
5. **Pre-allocated Payloads**: Immutable result payloads are shared

### Job Types

**Pulse Jobs** (health checks):
- `PulseHTTPJob`: fasthttp-based HTTP checks
- `PulseTCPJob`: TCP connection verification
- `PulseICMPJob`: ICMP ping with privilege handling

**Intervention Jobs** (recovery actions):
- `InterventionDockerJob`: Container restart
- `InterventionDockerStopJob`: Graceful stop (SIGTERM)
- `InterventionDockerStartJob`: Start stopped container
- `InterventionDockerKillJob`: Force kill (SIGKILL)
- `InterventionDockerPauseJob`: Pause processes
- `InterventionDockerUnpauseJob`: Resume processes
- `InterventionDockerScaleJob`: Scale Swarm replicas

**Code Jobs** (notifications):
- `CodeLogJob`: JSON log file output
- `CodeSlackJob`: Slack webhooks
- `CodePagerDutyJob`: PagerDuty Events API
- `CodeEmailJob`: SMTP notifications
- `CodeWebhookJob`: Generic webhooks

## Loading Pipeline

The loader uses a concurrent pipeline for efficient configuration loading:

```
┌─────────────┐    ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│ File Reader │───▶│  Workers    │───▶│   Batcher   │───▶│  Creator    │
│ (streaming) │    │(parse+valid)│    │(deduplicate)│    │(ECS entities)│
└─────────────┘    └─────────────┘    └─────────────┘    └─────────────┘
     Stage 1           Stage 2           Stage 3           Stage 4
```

- **Stage 1**: Sequential I/O (streaming YAML parsing)
- **Stage 2**: Parallel CPU (parse + validate with N workers)
- **Stage 3**: Fan-in (batch collection, deduplication)
- **Stage 4**: Sequential (ECS entity creation)

Memory optimization:
- Streaming mode: ~10MB for 1M monitors
- Traditional mode: ~500MB+ for 1M monitors

## Performance Characteristics

| Metric | Value |
|--------|-------|
| Max Concurrent Monitors | 1,000,000+ |
| Throughput | 10,000+ checks/sec/pipeline |
| Latency (P95) | < 100ms (configurable via SLO) |
| Memory per Monitor | ~100 bytes |
| Total Memory (1M monitors) | ~100 MB + worker pool overhead |
| Worker Scaling | Dynamic (M/M/c based) |

## Configuration

### Controller Config

```go
type Config struct {
    Logger            *zap.SugaredLogger
    WorkerConfig      queue.WorkerPoolConfig
    PipelineConfig    loader.PipelineConfig
    QueueCapacity     uint64        // Default: 8192
    BatchSize         int           // Default: 1000
    SizingServiceTime time.Duration // τ for M/M/c
    SizingSLO         time.Duration // W target
    SizingHeadroomPct float64       // Safety margin
    ShardSlots        int           // Scheduling shards
    ShardTargetSweep  time.Duration // Sweep duration
    Debug             bool
}
```

### Environment Variables

- `CPRA_SIZING_TAU_MS`: Service time in milliseconds
- `CPRA_SIZING_SLO_MS`: SLO target in milliseconds
- `CPRA_SIZING_HEADROOM_PCT`: Headroom percentage (0.15 = 15%)

### GC Tuning

For 1M+ monitors:
- `GOMEMLIMIT=3200MiB` (70-80% of container limit)
- `GOGC=100` (default, tune based on workload)

## Graceful Shutdown

The shutdown sequence ensures no data loss:

1. **Signal Handling**: SIGINT/SIGTERM triggers cancellation
2. **ECS Termination**: TerminationSystem signals app exit
3. **Worker Pool Drain**: Wait for in-flight jobs to complete
4. **Queue Close**: Prevent new enqueues
5. **Log Flush**: Ensure all logs are written
6. **Metrics Collection**: Final statistics

Timeout: 30 seconds maximum for graceful shutdown.

## Monitoring & Observability

### Expvar Metrics

Exposed at `/debug/vars`:
- `cpra_controller`: Queue depths, worker counts, world stats

### Pprof Profiling

Exposed at `/debug/pprof/`:
- CPU profile: `/debug/pprof/profile`
- Heap profile: `/debug/pprof/heap`
- Goroutine profile: `/debug/pprof/goroutine`

### Logging

Structured logging via Uber's zap:
- Component-tagged loggers (CONTROLLER, PULSE, etc.)
- Debug mode for verbose output
- State transition logging for debugging

## Dependencies

| Library | Purpose |
|---------|---------|
| [mlange-42/ark](https://github.com/mlange-42/ark) | High-performance ECS |
| [panjf2000/ants](https://github.com/panjf2000/ants) | Goroutine pool |
| [puzpuzpuz/xsync](https://github.com/puzpuzpuz/xsync) | Lock-free data structures |
| [Workiva/go-datastructures](https://github.com/Workiva/go-datastructures) | Ring buffer |
| [uber-go/zap](https://github.com/uber-go/zap) | Structured logging |
| [valyala/fasthttp](https://github.com/valyala/fasthttp) | High-performance HTTP |
| [moby/moby](https://github.com/moby/moby) | Docker client |
| [prometheus-community/pro-bing](https://github.com/prometheus-community/pro-bing) | ICMP ping |

