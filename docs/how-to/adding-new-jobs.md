# Adding New Job Types

This guide explains how to add new job types to CPRA following the established safety patterns.

## Overview

Jobs in CPRA follow a consistent pattern that ensures:

- **CPU safety**: Global dial limiting prevents spikes during network outages
- **Memory efficiency**: sync.Pool reduces GC pressure at scale
- **Graceful shutdown**: Context cancellation enables clean termination
- **Reliability**: Retry logic with backoff handles transient failures

## Job Categories

CPRA has three categories of jobs:

| Category | Purpose | Examples |
|----------|---------|----------|
| **Pulse** | Health checks | HTTP, TCP, ICMP |
| **Intervention** | Automated recovery | Docker restart/stop/start/kill/pause/unpause/scale |
| **Code** | Alert notifications | Log, Slack, PagerDuty |

## Quick Start

1. Copy `internal/jobs/TEMPLATE.go` to a new file
2. Rename following the convention: `pulse_<driver>.go`, `intervention_<action>.go`, or `code_<channel>.go`
3. Follow the safety checklist in the template
4. Add pool functions to `pool.go`
5. Add factory function to `factory.go`
6. Add predeclared errors to `types.go`

## Safety Checklist

Every network job MUST implement these guardrails:

```
[ ] 1. Embed BaseNetworkJob (or BaseJob for non-network jobs)
[ ] 2. Call AcquireDialSlot(ctx) before ANY network I/O
[ ] 3. defer ReleaseDialSlot() immediately after successful acquire
[ ] 4. Check ctx.Done() before EACH retry attempt
[ ] 5. Use predeclared errors (var ErrXxx = errors.New(...))
[ ] 6. Add sync.Pool getter/putter in pool.go
[ ] 7. Use pre-allocated payloads where possible
[ ] 8. Return Result{Ent, Err, Payload} in ALL code paths
[ ] 9. Implement all Job interface methods
[ ] 10. Add factory function in factory.go
```

## Example: Adding a DNS Pulse Job

### Step 1: Create the Job File

Create `internal/jobs/pulse_dns.go`:

```go
package jobs

import (
    "context"
    "net"
    "time"

    "github.com/mlange-42/ark/ecs"
)

// PulseDNSJob performs DNS lookup health checks.
type PulseDNSJob struct {
    EnqueueTime time.Time
    StartTime   time.Time
    Host        string
    JobType     string
    Driver      string
    Timeout     time.Duration
    Retries     int
    Entity      ecs.Entity
}

func (p *PulseDNSJob) Execute(ctx context.Context) Result {
    payload := GetPulseDNSPayload()

    // REQUIRED: Acquire dial slot before network I/O
    if !GetDialLimiter().Acquire(ctx) {
        return Result{Ent: p.Entity, Err: ErrDialLimiterTimeout, Payload: payload}
    }
    defer GetDialLimiter().Release()

    attempts := p.Retries + 1
    if attempts < 1 {
        attempts = 1
    }

    for attempt := 0; attempt < attempts; attempt++ {
        // Check context before each attempt
        select {
        case <-ctx.Done():
            return Result{Ent: p.Entity, Err: ctx.Err(), Payload: payload}
        default:
        }

        resolver := &net.Resolver{}
        ctx, cancel := context.WithTimeout(ctx, p.Timeout)
        _, err := resolver.LookupHost(ctx, p.Host)
        cancel()

        if err == nil {
            return Result{Ent: p.Entity, Err: nil, Payload: payload}
        }

        if attempt < attempts-1 {
            time.Sleep(50 * time.Millisecond)
        }
    }

    return Result{Ent: p.Entity, Err: ErrDNSCheckFailed, Payload: payload}
}

func (p *PulseDNSJob) Copy() Job                  { job := *p; return &job }
func (p *PulseDNSJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseDNSJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseDNSJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseDNSJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseDNSJob) IsNil() bool                { return p == nil }
```

### Step 2: Add to types.go

Add the predeclared error:

```go
ErrDNSCheckFailed = errors.New("dns check failed after retries")
```

Add the payload:

```go
pulseDNSPayload = map[string]interface{}{"type": "pulse", "driver": "dns"}

func GetPulseDNSPayload() map[string]interface{} { return pulseDNSPayload }
```

### Step 3: Add to pool.go

```go
var pulseDNSJobPool = sync.Pool{
    New: func() interface{} { return &PulseDNSJob{} },
}

func getPulseDNSJob() *PulseDNSJob {
    return pulseDNSJobPool.Get().(*PulseDNSJob)
}

func putPulseDNSJob(j *PulseDNSJob) {
    *j = PulseDNSJob{}
    pulseDNSJobPool.Put(j)
}
```

### Step 4: Add to factory.go

In `CreatePulseJob`:

```go
case *schema.PulseDNSConfig:
    job := getPulseDNSJob()
    job.Entity = jobID
    job.Host = cfg.Host
    job.Timeout = timeout
    job.Retries = cfg.Retries
    job.JobType = InternedPulse
    job.Driver = InternedDNS
    return job, nil
```

### Step 5: Add Schema Support

In `internal/loader/schema/manifest.go`:

```go
type PulseDNSConfig struct {
    Host    string `yaml:"host" json:"host"`
    Retries int    `yaml:"retries" json:"retries"`
}
```

## Why These Patterns Matter

### Dial Limiter

Without dial limiting, 100k monitors becoming unreachable simultaneously causes CPU to spike from 50% to 800%+ as goroutines pile up waiting on network timeouts.

```go
// BAD: No dial limiting
func (j *MyJob) Execute(ctx context.Context) Result {
    conn, err := net.Dial("tcp", j.Host) // Unconstrained!
    // ...
}

// GOOD: With dial limiting
func (j *MyJob) Execute(ctx context.Context) Result {
    if !GetDialLimiter().Acquire(ctx) {
        return Result{Err: ErrDialLimiterTimeout}
    }
    defer GetDialLimiter().Release()
    
    conn, err := net.Dial("tcp", j.Host) // Controlled
    // ...
}
```

### Context Checking

Without context checks, jobs continue running during shutdown, causing delays and resource leaks.

```go
// BAD: No context check in retry loop
for i := 0; i < attempts; i++ {
    err := doOperation()
    // Continues even during shutdown!
}

// GOOD: Check context each iteration
for i := 0; i < attempts; i++ {
    select {
    case <-ctx.Done():
        return Result{Err: ctx.Err()}
    default:
    }
    err := doOperation()
}
```

### sync.Pool

At 1M monitors with 5-second intervals, CPRA processes 200k jobs/second. Without pooling, this creates massive GC pressure.

```go
// BAD: New allocation per job
job := &PulseHTTPJob{...}

// GOOD: Reuse from pool
job := getPulseHTTPJob()
defer putPulseHTTPJob(job)
```

## Testing New Jobs

1. Write unit tests in `pulse_dns_test.go`
2. Test with 1k monitors to verify basic functionality
3. Test with 100k monitors to verify no memory leaks
4. Test with all monitors unreachable to verify CPU stays bounded

## Docker Intervention Examples

CPRA supports multiple Docker intervention types via the `target.type` field:

### Container Restart (default)

```yaml
intervention:
  action: docker
  target:
    type: restart
    container: my-app
    timeout: 30s
  retries: 2
```

### Graceful Stop (SIGTERM)

```yaml
intervention:
  action: docker
  target:
    type: stop
    container: my-app
    timeout: 10s
```

### Start Stopped Container

```yaml
intervention:
  action: docker
  target:
    type: start
    container: my-app
    timeout: 10s
```

### Force Kill (SIGKILL)

```yaml
intervention:
  action: docker
  target:
    type: kill
    container: my-app
    signal: SIGKILL  # Optional, default: SIGKILL
```

### Pause Container Processes

```yaml
intervention:
  action: docker
  target:
    type: pause
    container: my-app
```

### Unpause Container Processes

```yaml
intervention:
  action: docker
  target:
    type: unpause
    container: my-app
```

### Scale Swarm Service

```yaml
intervention:
  action: docker
  target:
    type: scale
    service: my-service  # Swarm service name
    replicas: 5
    timeout: 60s
```

## Package Structure

```
internal/jobs/
├── README.md            # Package overview
├── TEMPLATE.go          # Template for new jobs
├── base.go              # BaseJob, BaseNetworkJob structs
├── types.go             # Job interface, Result, errors, payloads
├── factory.go           # CreatePulseJob, CreateInterventionJob, CreateCodeJob
├── pool.go              # sync.Pool definitions
├── dial_limiter.go      # Global rate/concurrency limiting
│
├── pulse_http.go        # HTTP health checks
├── pulse_tcp.go         # TCP connection checks
├── pulse_icmp.go        # ICMP ping checks
│
├── intervention_docker.go # Docker: restart, stop, start, kill, pause, unpause, scale
│
├── code_log.go          # Log file alerts
├── code_slack.go        # Slack notifications
├── code_pagerduty.go    # PagerDuty alerts
├── code_email.go        # Email notifications
├── code_webhook.go      # Webhook notifications
│
├── pool_http.go         # fasthttp client pool
├── pool_tcp.go          # TCP dialer optimization
├── pool_docker.go       # Docker client pool
└── log_writer.go        # Async log writer
```
