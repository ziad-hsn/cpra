# Package: loader

## Overview

Package `loader` provides concurrent configuration loading for CPRA monitors.

The loader uses a multi-stage pipeline to efficiently parse, validate, and create ECS entities from YAML configuration files. It supports streaming mode for memory-efficient loading of large configurations (1M+ monitors).

## Import Path

```go
import "cpra/internal/loader"
```

## Pipeline Architecture

```
┌─────────────┐    ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│ File Reader │───▶│  Workers    │───▶│   Batcher   │───▶│  Creator    │
│ (streaming) │    │(parse+valid)│    │(deduplicate)│    │(ECS entities)│
└─────────────┘    └─────────────┘    └─────────────┘    └─────────────┘
     Stage 1           Stage 2           Stage 3           Stage 4
```

| Stage | Concurrency | Purpose |
|-------|-------------|---------|
| 1. File Reader | Sequential | I/O-bound YAML streaming |
| 2. Workers | Parallel (N) | CPU-bound parse + validate |
| 3. Batcher | Sequential | Fan-in, deduplication |
| 4. Creator | Sequential | ECS entity creation (Ark constraint) |

## Key Types

### Pipeline

Orchestrates concurrent loading:

```go
type Pipeline struct {
    world         *ecs.World
    entityManager *entities.EntityManager
    validator     *MonitorValidator
    rawChan       chan RawMonitor
    validatedChan chan ValidatedMonitor
    batchChan     chan MonitorBatch
    config        PipelineConfig
}
```

### PipelineConfig

Configuration for the loading pipeline:

```go
type PipelineConfig struct {
    Workers              int           // Number of parse workers
    BatchSize            int           // Entities per batch
    RawChannelSize       int           // Raw monitor buffer
    ValidatedChannelSize int           // Validated monitor buffer
    BatchChannelSize     int           // Batch buffer
    BufferSize           int           // I/O buffer size
    StreamingMode        bool          // Enable memory-efficient streaming
    StrictUnknownFields  bool          // Fail on unknown YAML fields
    FailFast             bool          // Stop on first error
    ProgressInterval     time.Duration // Progress callback interval
    ProgressCallback     func(LoadProgress)
}
```

### PipelineStats

Loading statistics:

```go
type PipelineStats struct {
    TotalMonitors     int64
    EntitiesCreated   int64
    SkippedMonitors   int64
    DuplicateMonitors int64
    LoadingTime       time.Duration
    ParseRate         float64
    CreationRate      float64
    PulseRate         float64  // Aggregate pulse rate (jobs/sec)
}
```

### LoadProgress

Progress reporting:

```go
type LoadProgress struct {
    BytesRead      int64
    TotalBytes     int64
    MonitorsParsed int64
    Elapsed        time.Duration
    Stage          string
}
```

## Loading Modes

### Traditional Mode

Loads full YAML node tree into memory:
- Fast parsing
- ~500MB+ for 1M monitors
- May OOM on constrained systems

### Streaming Mode

Parses YAML line-by-line:
- ~10MB for 1M monitors
- Slightly slower parsing
- Memory-efficient for large files

Enable with:
```go
config.StreamingMode = true
```

## Validation

The `MonitorValidator` validates each monitor:

- Required fields (name, pulse type)
- Valid pulse configuration
- Valid intervention configuration
- Valid code configurations
- Threshold values

## Usage Example

```go
// Create pipeline configuration
config := loader.DefaultPipelineConfig()
config.Workers = runtime.NumCPU() * 2
config.BatchSize = 1000
config.StreamingMode = true

// Set up progress reporting
config.ProgressCallback = func(p loader.LoadProgress) {
    pct := float64(p.BytesRead) / float64(p.TotalBytes) * 100
    fmt.Printf("Loading: %.1f%% (%d monitors)\n", pct, p.MonitorsParsed)
}

// Create and run pipeline
pipeline := loader.NewPipeline(world, entityManager, config)
stats, err := pipeline.Load(ctx, "monitors.yaml")
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Loaded %d monitors in %v (%.0f/sec)\n",
    stats.EntitiesCreated, stats.LoadingTime, stats.CreationRate)
```

## YAML Schema

### Monitor Configuration

```yaml
monitors:
  - name: "my-service-health"
    enabled: true
    pulse:
      type: http
      interval: 30s
      timeout: 5s
      unhealthy_threshold: 3
      healthy_threshold: 2
      config:
        method: GET
        url: http://my-service:8080/health
        retries: 2
    intervention:
      action: docker
      max_failures: 1
      target:
        type: restart
        container: my-service
        docker_host: unix:///var/run/docker.sock
    codes:
      red:
        dispatch: true
        notify: pagerduty
        config:
          routing_key: "abc123"
      yellow:
        dispatch: true
        notify: log
        config:
          file: /var/log/alerts.log
      green:
        dispatch: true
        notify: log
        config:
          file: /var/log/alerts.log
```

### Pulse Types

**HTTP:**
```yaml
pulse:
  type: http
  config:
    method: GET
    url: http://example.com/health
    retries: 2
```

**TCP:**
```yaml
pulse:
  type: tcp
  config:
    host: example.com
    port: 3306
    retries: 2
```

**ICMP:**
```yaml
pulse:
  type: icmp
  config:
    host: example.com
    count: 3
    privilege: false
```

### Intervention Types

**Docker Restart:**
```yaml
intervention:
  action: docker
  target:
    type: restart
    container: my-container
    timeout: 30s
```

**Docker Stop:**
```yaml
intervention:
  action: docker
  target:
    type: stop
    container: my-container
    timeout: 30s
```

**Docker Scale (Swarm):**
```yaml
intervention:
  action: docker
  target:
    type: scale
    service: my-service
    replicas: 3
```

### Code Notification Types

**Log:**
```yaml
codes:
  red:
    notify: log
    config:
      file: /var/log/alerts.log
```

**PagerDuty:**
```yaml
codes:
  red:
    notify: pagerduty
    config:
      routing_key: "your-routing-key"
```

**Slack:**
```yaml
codes:
  yellow:
    notify: slack
    config:
      webhook_url: "https://hooks.slack.com/..."
      channel: "#alerts"
```

## Performance Tips

1. **Use streaming mode** for files > 100MB
2. **Increase workers** for CPU-bound parsing
3. **Increase batch size** for faster entity creation
4. **Use gzip compression** (.gz) for large files
5. **Pre-validate** YAML syntax before loading

## Dependencies

- `gopkg.in/yaml.v3`: YAML parsing
- `golang.org/x/sync/errgroup`: Error propagation
- `github.com/mlange-42/ark/ecs`: Entity creation

