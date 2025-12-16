---
title: Monitor Configuration Schema
parent: Reference
---

# Monitor Configuration Schema

The CPRA system is configured via a single YAML file that defines a list of monitors. This document provides a complete reference for the `Monitor` object schema.

## Top-Level Monitor Object

| Field | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `name` | `string` | Yes | A unique, human-readable name for the monitor. |
| `enabled` | `boolean` | No | If `false`, the monitor is loaded but never scheduled. Defaults to `true`. |
| `pulse_check` | `PulseConfig` | Yes | Configuration for the health check pipeline. |
| `intervention` | `InterventionConfig` | No | Configuration for the automated remediation pipeline. |
| `codes` | `map[string]CodeConfig` | No | Configuration for the alerting pipeline, mapped by alert "color" (e.g., red, yellow). |

## PulseConfig (Health Check)

Defines the parameters for the health check.

| Field | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `type` | `string` | Yes | The type of check: `http`, `tcp`, or `icmp`. |
| `interval` | `duration` | Yes | How often the check should run (e.g., `30s`, `5m`). |
| `timeout` | `duration` | Yes | Maximum time to wait for the check to complete (e.g., `5s`). |
| `unhealthy_threshold` | `integer` | No | Number of consecutive failures before the monitor is considered unhealthy and triggers Intervention. Defaults to `1`. |
| `healthy_threshold` | `integer` | No | Number of consecutive successes required to transition from unhealthy to healthy. Defaults to `1`. |
| `config` | `map[string]any` | Yes | Type-specific configuration (e.g., `http` details). |

### `config` Examples

**HTTP Check:**
```yaml
config:
  method: GET
  url: https://api.example.com/health
  headers:
    - "Authorization: Bearer token"
  expected_status: 200
```

**TCP Check:**
```yaml
config:
  host: database.internal
  port: 5432
```

**ICMP Check:**
```yaml
config:
  host: server.internal
  count: 3
```

## InterventionConfig (Remediation)

Defines the automated action to take when the `unhealthy_threshold` is met.

| Field | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `action` | `string` | Yes | The action to perform: `docker`. |
| `max_failures` | `integer` | No | Maximum number of times to attempt the intervention before giving up and triggering the Code pipeline. Defaults to `1`. |
| `config` | `map[string]any` | Yes | Action-specific configuration. |

### `config` Example (Docker Action)

```yaml
config:
  container: my-api-container
  action: restart
```

## CodeConfig (Alerting)

Defines the alerting policy. The map key (e.g., `red`) is the name of the alert "color" or severity.

| Field | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `dispatch` | `boolean` | Yes | If `true`, dispatch the alert when the condition is met. |
| `notify` | `string` | Yes | The notification method: `log`, `slack`, or `pagerduty`. |
| `config` | `map[string]any` | Yes | Notification-specific configuration. |

### `config` Example (PagerDuty Notification)

```yaml
config:
  url: https://events.pagerduty.com/v2/enqueue
```

### `config` Example (Slack Notification)

```yaml
config:
  hook: https://hooks.slack.com/services/T00000000/B00000000/XXX
```

### `config` Example (Log Notification)

```yaml
config:
  file: /var/log/cpra-alerts.log
```

---

## Controller Configuration (Go API)

The controller can be configured programmatically using the `Config` struct:

```go
type Config struct {
    Logger            *Logger
    WorkerConfig      queue.WorkerPoolConfig
    PipelineConfig    loader.PipelineConfig
    QueueCapacity     uint64        // Default: 8192 (power of 2)
    BatchSize         int           // Default: 1000
    UpdateInterval    time.Duration // System update interval
    SizingServiceTime time.Duration // Expected job execution time (τ)
    SizingSLO         time.Duration // Maximum acceptable latency (W)
    SizingHeadroomPct float64       // Safety margin percentage
    Debug             bool          // Enable debug logging
}
```

### Default Configuration

```go
config := controller.DefaultConfig()
// QueueCapacity:  8192
// BatchSize:      1000
// WorkerConfig:   queue.DefaultWorkerPoolConfig()
```

### Environment Variables

Worker sizing parameters can be overridden via environment variables:

| Variable | Description | Default |
| :--- | :--- | :--- |
| `CPRA_SIZING_TAU_MS` | Expected service time in milliseconds | Auto-calculated |
| `CPRA_SIZING_SLO_MS` | Target SLO in milliseconds | Auto-calculated |
| `CPRA_SIZING_HEADROOM_PCT` | Safety margin percentage | 15% |

---

## Worker Pool Configuration

The `WorkerPoolConfig` struct controls dynamic worker scaling using M/M/c queueing theory:

```go
type WorkerPoolConfig struct {
    // Core scaling parameters
    MinWorkers         int           // Minimum workers (never scale below)
    MaxWorkers         int           // Maximum workers (never scale above)
    AdjustmentInterval time.Duration // How often to check for scaling
    TargetQueueLatency time.Duration // Target queue wait time (SLO)

    // Result processing
    ResultBatchSize    int           // Batch size for result processing
    ResultBatchTimeout time.Duration // Timeout for result batching
    ResultChannelDepth int           // Buffer size for result channels

    // M/M/c scaling parameters (asymmetric cooldowns)
    ScaleUpCooldown    time.Duration // Min time between scale-ups (default 30s)
    ScaleDownCooldown  time.Duration // Min time between scale-downs (default 120s)

    // Hysteresis thresholds to prevent oscillation
    ScaleUpThreshold   float64       // Ratio above current to trigger up (default 1.10)
    ScaleDownThreshold float64       // Ratio below current to trigger down (default 0.80)

    // Warm-up period during which no scaling occurs
    WarmupDuration     time.Duration // Default 60s - system stabilization

    // Ants goroutine pool options
    PreAlloc           bool          // Pre-allocate worker pool
    NonBlocking        bool          // Non-blocking task submission
    MaxBlockingTasks   int           // Max tasks waiting for workers
    ExpiryDuration     time.Duration // Idle worker expiry time
}
```

### Default Worker Pool Configuration

```go
queue.DefaultWorkerPoolConfig()
// MinWorkers:         5
// MaxWorkers:         8192
// AdjustmentInterval: 5s
// TargetQueueLatency: 100ms
// ScaleUpCooldown:    30s   (react quickly to load)
// ScaleDownCooldown:  120s  (conservative reduction)
// ScaleUpThreshold:   1.10  (10% above current)
// ScaleDownThreshold: 0.80  (20% below current)
// WarmupDuration:     60s   (no scaling first minute)
```

### Worker Pool Statistics

Runtime metrics exposed by `WorkerPoolStats`:

| Metric | Description |
| :--- | :--- |
| `CurrentCapacity` | Current number of allocated workers |
| `RunningWorkers` | Workers actively processing jobs |
| `WaitingTasks` | Tasks queued waiting for workers |
| `TasksSubmitted` | Total tasks submitted to pool |
| `TasksCompleted` | Total tasks completed |
| `ScalingEvents` | Number of scale up/down events |
| `PendingResults` | Results awaiting processing |

---

## Production Environment Tuning

Source the production environment script before starting CPRA:

```bash
source scripts/production-env.sh
./cpra -yaml monitors.yaml
```

This configures Go runtime parameters:

| Variable | Description | Default |
| :--- | :--- | :--- |
| `GOGC` | Garbage collection frequency (higher = less frequent) | 150 |
| `GOMEMLIMIT` | Soft memory cap (Go 1.19+) | 12GiB |
| `GOMAXPROCS` | Number of OS threads | CPU count |
