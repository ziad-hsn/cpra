# Package: jobs

## Overview

Package `jobs` provides job types and execution logic for CPRA monitor operations.

Jobs are created from monitor schemas via factory functions, executed by worker pools, and produce Results consumed by ECS systems for state updates and alerting.

## Import Path

```go
import "cpra/internal/jobs"
```

## Package Organization

The package is organized into focused files:

| File | Purpose |
|------|---------|
| `base.go` | BaseJob and BaseNetworkJob with common functionality |
| `types.go` | Job interface, Result struct, errors, and payloads |
| `factory.go` | CreatePulseJob, CreateInterventionJob, CreateCodeJob |
| `pool.go` | sync.Pool definitions for job struct reuse |
| `dial_limiter.go` | Global rate/concurrency limiting for network operations |
| `TEMPLATE.go` | Template for creating new job types |

## Key Types

### Job Interface

All job types implement this interface:

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

### Result

Generic structure for job outcomes:

```go
type Result struct {
    Err     error
    Payload map[string]interface{}
    Ent     ecs.Entity
}
```

### BaseJob

Common fields for all job types:

```go
type BaseJob struct {
    EnqueueTime time.Time
    StartTime   time.Time
    Entity      ecs.Entity
    JobType     string        // "pulse", "intervention", "code"
    Driver      string        // "http", "tcp", "icmp", "docker", etc.
    Timeout     time.Duration
    Retries     int
}
```

### BaseNetworkJob

Extended base for network I/O jobs with dial limiter integration:

```go
type BaseNetworkJob struct {
    BaseJob
}

func (b *BaseNetworkJob) AcquireDialSlot(ctx context.Context) bool
func (b *BaseNetworkJob) ReleaseDialSlot()
func (b *BaseNetworkJob) TryAcquireDialSlot() bool
```

## Job Types

### Pulse Jobs (Health Checks)

| Type | Description | Config |
|------|-------------|--------|
| `PulseHTTPJob` | HTTP health checks with fasthttp | URL, Method, Timeout, Retries |
| `PulseTCPJob` | TCP connection checks | Host, Port, Timeout, Retries |
| `PulseICMPJob` | ICMP ping checks | Host, Count, Timeout, Retries |

### Intervention Jobs (Recovery Actions)

| Type | Description | Config |
|------|-------------|--------|
| `InterventionDockerJob` | Container restart (default) | Container, DockerHost, Timeout |
| `InterventionDockerStopJob` | Graceful stop (SIGTERM) | Container, DockerHost, Timeout |
| `InterventionDockerStartJob` | Start stopped container | Container, DockerHost, Timeout |
| `InterventionDockerKillJob` | Force kill (SIGKILL) | Container, DockerHost, Signal |
| `InterventionDockerPauseJob` | Pause processes | Container, DockerHost |
| `InterventionDockerUnpauseJob` | Resume processes | Container, DockerHost |
| `InterventionDockerScaleJob` | Scale Swarm replicas | Service, DockerHost, Replicas |

### Code Jobs (Notifications)

| Type | Description | Config |
|------|-------------|--------|
| `CodeLogJob` | JSON log file output | File path, Monitor, Color |
| `CodeSlackJob` | Slack notifications | Webhook URL, Channel |
| `CodePagerDutyJob` | PagerDuty alerts | Routing key, Severity |
| `CodeEmailJob` | Email notifications | SMTP config, Recipients |
| `CodeWebhookJob` | Generic webhooks | URL, Headers, Body |

## Factory Functions

### CreatePulseJob

Creates a pulse job based on schema configuration:

```go
func CreatePulseJob(pulseSchema schema.Pulse, jobID ecs.Entity) (Job, error)
```

### CreateInterventionJob

Creates an intervention job based on schema configuration:

```go
func CreateInterventionJob(interventionSchema schema.Intervention, jobID ecs.Entity) (Job, error)
```

### CreateCodeJob

Creates a code alert job based on configuration:

```go
func CreateCodeJob(monitor string, config schema.CodeConfig, jobID ecs.Entity, color string) (Job, error)
```

## Safety Guardrails

All network jobs implement critical safety patterns:

### 1. Global Dial Limiter

Prevents CPU spikes during network outages by limiting both rate (requests/sec) and concurrency (in-flight dials). Without this, 100k monitors becoming unreachable simultaneously can spike CPU from 50% to 800%.

```go
if !j.AcquireDialSlot(ctx) {
    return Result{Ent: j.Entity, Err: ErrDialLimiterTimeout, Payload: payload}
}
defer j.ReleaseDialSlot()
```

### 2. Context Cancellation

Jobs check `ctx.Done()` before each retry, enabling graceful shutdown without stuck goroutines:

```go
if err := j.CheckContext(ctx); err != nil {
    return Result{Ent: j.Entity, Err: err, Payload: payload}
}
```

### 3. Retry with Backoff

Jobs retry with 50ms delays between attempts to handle transient failures:

```go
j.SleepBetweenRetries(attempt, maxAttempts)
```

### 4. Object Pooling

sync.Pool reduces GC pressure by reusing job structs. At 200k jobs/sec, this prevents massive allocation churn:

```go
job := getPulseHTTPJob()  // Get from pool
// ... use job ...
putPulseHTTPJob(job)      // Return to pool
```

### 5. Pre-allocated Payloads

Immutable result payloads are shared to reduce per-job allocations:

```go
var pulseHTTPPayload = map[string]interface{}{"type": "pulse", "driver": "http"}
```

## Creating New Jobs

See `TEMPLATE.go` for a complete template. Key steps:

1. Copy TEMPLATE.go to a new file (e.g., `pulse_dns.go`)
2. Implement the job struct and Execute method
3. Add sync.Pool functions to `pool.go`
4. Add factory function to `factory.go`
5. Add predeclared errors to `types.go`

## Predefined Errors

```go
// Factory errors
ErrUnknownPulseConfig        = errors.New("unknown pulse config type")
ErrDockerMissingTarget       = errors.New("docker intervention missing target configuration")
ErrUnknownInterventionAction = errors.New("unknown intervention action")
ErrUnknownCodeNotification   = errors.New("unknown code notification type")

// Execution errors - pulse jobs
ErrHTTPNon2xxStatus = errors.New("received non-2xx status code")
ErrHTTPCheckFailed  = errors.New("http check failed after retries")
ErrTCPCheckFailed   = errors.New("tcp check failed after retries")
ErrICMPCheckFailed  = errors.New("icmp check failed after retries")

// Execution errors - intervention jobs
ErrDockerActionFailed = errors.New("docker intervention failed after retries")
ErrDockerStopFailed   = errors.New("docker stop failed after retries")
// ... more errors

// Resource limit errors
ErrDialLimiterTimeout = errors.New("dial limiter timeout (system under load)")
```

## Usage Example

```go
// Create a pulse job
pulseSchema := schema.Pulse{
    Config:  &schema.PulseHTTPConfig{Url: "https://example.com", Method: "GET"},
    Timeout: 5 * time.Second,
    Retries: 2,
}
job, err := jobs.CreatePulseJob(pulseSchema, entityID)
if err != nil {
    return err
}

// Execute the job
result := job.Execute(ctx)
if result.Err != nil {
    log.Printf("Pulse failed: %v", result.Err)
}
```

## Dependencies

- `github.com/mlange-42/ark/ecs`: Entity references
- `github.com/valyala/fasthttp`: HTTP client
- `github.com/moby/moby/client`: Docker client
- `github.com/prometheus-community/pro-bing`: ICMP ping

