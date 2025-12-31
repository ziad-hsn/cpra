# Jobs Reference

This document provides a reference for all job types in CPRA's `internal/jobs` package.

## Job Interface

All jobs implement the `Job` interface:

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

## Result Structure

Jobs return a `Result` struct:

```go
type Result struct {
    Err     error
    Payload map[string]interface{}
    Ent     ecs.Entity
}
```

## Base Job Types

### BaseJob

Common fields for all jobs:

| Field | Type | Description |
|-------|------|-------------|
| `EnqueueTime` | `time.Time` | When the job was added to the queue |
| `StartTime` | `time.Time` | When execution began |
| `Entity` | `ecs.Entity` | Associated ECS entity |
| `JobType` | `string` | Category: "pulse", "intervention", "code" |
| `Driver` | `string` | Specific type: "http", "tcp", "docker", etc. |
| `Timeout` | `time.Duration` | Operation timeout |
| `Retries` | `int` | Number of retry attempts |

### BaseNetworkJob

Extends `BaseJob` with dial limiter integration. Use for any job performing network I/O.

**Methods:**

- `AcquireDialSlot(ctx) bool` - Acquire permission to dial
- `ReleaseDialSlot()` - Release the dial slot
- `TryAcquireDialSlot() bool` - Non-blocking acquire attempt

## Pulse Jobs

Health check jobs that verify service availability.

### PulseHTTPJob

HTTP health checks using fasthttp.

| Field | Type | Description |
|-------|------|-------------|
| `URL` | `string` | Target URL |
| `Method` | `string` | HTTP method (GET, POST, etc.) |
| `Host` | `string` | Extracted host for client pooling |
| `IsTLS` | `bool` | Whether to use HTTPS |

**Success criteria:** HTTP 2xx status code

### PulseTCPJob

TCP connection health checks.

| Field | Type | Description |
|-------|------|-------------|
| `Host` | `string` | Target hostname |
| `Port` | `int` | Target port |

**Success criteria:** TCP connection established

### PulseICMPJob

ICMP ping health checks using pro-bing library.

| Field | Type | Description |
|-------|------|-------------|
| `Host` | `string` | Target hostname or IP |
| `Count` | `int` | Number of ping packets |
| `IgnorePrivilege` | `bool` | Ignore privilege errors |

**Success criteria:** At least one packet received

## Intervention Jobs

Automated recovery actions for Docker containers and Swarm services.

### InterventionDockerJob (Restart)

Restarts Docker containers (stop + start).

| Field | Type | Description |
|-------|------|-------------|
| `Container` | `string` | Container name or ID |
| `DockerHost` | `string` | Docker daemon address |
| `Timeout` | `time.Duration` | Stop timeout before force kill |

**Success criteria:** Container restarted without error

### InterventionDockerStopJob

Gracefully stops containers with SIGTERM.

| Field | Type | Description |
|-------|------|-------------|
| `Container` | `string` | Container name or ID |
| `DockerHost` | `string` | Docker daemon address |
| `Timeout` | `time.Duration` | Time to wait before force kill |

**Success criteria:** Container stopped

### InterventionDockerStartJob

Starts a stopped container.

| Field | Type | Description |
|-------|------|-------------|
| `Container` | `string` | Container name or ID |
| `DockerHost` | `string` | Docker daemon address |
| `Timeout` | `time.Duration` | Operation timeout |

**Success criteria:** Container started

### InterventionDockerKillJob

Force kills a container with SIGKILL (or custom signal).

| Field | Type | Description |
|-------|------|-------------|
| `Container` | `string` | Container name or ID |
| `DockerHost` | `string` | Docker daemon address |
| `Signal` | `string` | Kill signal (default: SIGKILL) |

**Success criteria:** Container killed

### InterventionDockerPauseJob

Pauses (freezes) container processes.

| Field | Type | Description |
|-------|------|-------------|
| `Container` | `string` | Container name or ID |
| `DockerHost` | `string` | Docker daemon address |

**Success criteria:** Container paused

### InterventionDockerUnpauseJob

Unpauses (resumes) container processes.

| Field | Type | Description |
|-------|------|-------------|
| `Container` | `string` | Container name or ID |
| `DockerHost` | `string` | Docker daemon address |

**Success criteria:** Container unpaused

### InterventionDockerScaleJob

Scales a Docker Swarm service's replica count.

| Field | Type | Description |
|-------|------|-------------|
| `Service` | `string` | Swarm service name |
| `DockerHost` | `string` | Docker daemon address |
| `Replicas` | `uint64` | Target replica count |
| `Timeout` | `time.Duration` | Operation timeout |

**Success criteria:** Service updated with new replica count

**Note:** Requires Docker Swarm mode. Returns `ErrNotReplicatedService` if the service is not in replicated mode.

## Code Alert Jobs

Notification jobs for alerting.

### CodeLogJob

Writes alerts to JSON log files.

| Field | Type | Description |
|-------|------|-------------|
| `Monitor` | `string` | Monitor name |
| `Color` | `string` | Alert color (red, yellow, green, cyan, gray) |
| `Status` | `string` | Status message |
| `Severity` | `string` | Alert severity |
| `Summary` | `string` | Alert summary |
| `Action` | `string` | Recommended action |
| `NextSteps` | `string` | Follow-up steps |
| `File` | `string` | Output file path |

### CodePagerDutyJob (Placeholder)

PagerDuty integration via Events API v2.

### CodeSlackJob (Placeholder)

Slack notifications via webhook or API.

### CodeEmailJob (Placeholder)

Email notifications via SMTP.

### CodeWebhookJob (Placeholder)

Generic webhook POST notifications.

## Dial Limiter

Global rate and concurrency limiter for network operations.

### Configuration

```go
type DialLimiterConfig struct {
    MaxConcurrentDials int           // Max in-flight dials (default: 2048)
    DialsPerSecond     int           // Max rate (default: 10000)
    BurstSize          int           // Token bucket burst (default: 500)
    AcquireTimeout     time.Duration // Max wait time (default: 5s)
}
```

### Tuning Guidelines

| Monitor Count | MaxConcurrentDials | DialsPerSecond | BurstSize |
|---------------|-------------------|----------------|-----------|
| 10k | 512 | 5000 | 250 |
| 100k | 2048 | 10000 | 500 |
| 1M | 4096 | 20000 | 1000 |

### Stats

```go
stats := jobs.GetDialLimiter().Stats()
// Returns: InFlightDials, MaxConcurrentDials, TokensAvailable, DialsPerSecond
```

## Predeclared Errors

Errors are predeclared to avoid allocations in hot paths:

```go
// Factory errors
ErrUnknownPulseConfig
ErrDockerMissingTarget
ErrUnknownInterventionAction
ErrUnknownDockerAction
ErrUnknownCodeNotification

// Execution errors - Pulse jobs
ErrHTTPCheckFailed
ErrTCPCheckFailed
ErrICMPCheckFailed

// Execution errors - Docker intervention jobs
ErrDockerActionFailed      // Restart failed
ErrDockerStopFailed        // Stop failed
ErrDockerStartFailed       // Start failed
ErrDockerKillFailed        // Kill failed
ErrDockerPauseFailed       // Pause failed
ErrDockerUnpauseFailed     // Unpause failed
ErrDockerScaleFailed       // Scale failed
ErrNotReplicatedService    // Service not in replicated mode

// Execution errors - Code jobs
ErrLogMarshalFailed

// Resource errors
ErrSemaphoreTimeout
ErrDialLimiterTimeout
```

## Factory Functions

### CreatePulseJob

```go
func CreatePulseJob(pulseSchema schema.Pulse, jobID ecs.Entity) (Job, error)
```

Creates HTTP, TCP, or ICMP pulse jobs based on schema config type.

### CreateInterventionJob

```go
func CreateInterventionJob(interventionSchema schema.Intervention, jobID ecs.Entity) (Job, error)
```

Creates intervention jobs based on `target.type`:

| Type | Job Created |
|------|-------------|
| `restart` (default) | `InterventionDockerJob` |
| `stop` | `InterventionDockerStopJob` |
| `start` | `InterventionDockerStartJob` |
| `kill` | `InterventionDockerKillJob` |
| `pause` | `InterventionDockerPauseJob` |
| `unpause` | `InterventionDockerUnpauseJob` |
| `scale` | `InterventionDockerScaleJob` |

### CreateCodeJob

```go
func CreateCodeJob(monitor string, config schema.CodeConfig, jobID ecs.Entity, color string) (Job, error)
```

Creates code alert jobs based on notification type.

## File Organization

```
internal/jobs/
├── README.md            # Package overview
├── TEMPLATE.go          # Template for new jobs
├── base.go              # BaseJob, BaseNetworkJob
├── types.go             # Job interface, Result, errors
├── factory.go           # Factory functions
├── pool.go              # sync.Pool definitions
├── dial_limiter.go      # Rate/concurrency limiting
│
├── pulse_http.go        # HTTP checks
├── pulse_tcp.go         # TCP checks
├── pulse_icmp.go        # ICMP checks
│
├── intervention_docker.go  # Docker: restart, stop, start, kill, pause, unpause, scale
│
├── code_log.go
├── code_slack.go
├── code_pagerduty.go
├── code_email.go
├── code_webhook.go
│
├── pool_http.go         # fasthttp clients
├── pool_tcp.go          # TCP dialer
├── pool_docker.go       # Docker clients
└── log_writer.go        # Async logging
```
