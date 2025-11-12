# CPRA - Concurrent Pulse-Remediation-Alerting System

[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Documentation](https://img.shields.io/badge/docs-latest-blue)](docs/explanation/architecture-overview.md)

**Monitor millions of services concurrently with automated remediation and intelligent worker scaling.**

CPRA is a high-performance infrastructure monitoring system designed for platform teams managing large-scale microservice architectures. Built on Entity-Component-System (ECS) architecture and queueing theory principles, CPRA handles 1,000,000+ concurrent health checks with automatic worker pool scaling to meet SLO targets.

---

## Table of Contents

- [Why CPRA?](#why-cpra)
- [Key Features](#key-features)
- [Performance Characteristics](#performance-characteristics)
- [Architecture](#architecture)
- [Quick Start](#quick-start)
- [Installation](#installation)
- [Configuration](#configuration)
- [Command-Line Options](#command-line-options)
- [Documentation](#documentation)
- [Troubleshooting](#troubleshooting)
- [Contributing](#contributing)
- [License](#license)

---

## Why CPRA?

**Use CPRA when you need to:**
- Monitor 100,000+ concurrent services, containers, or endpoints
- Automatically remediate failures without human intervention
- Scale monitoring infrastructure dynamically based on load
- Achieve sub-100ms P95 latency from detection to alerting
- Minimize memory footprint (~100 bytes per monitor)

**CPRA vs. Traditional Monitoring:**
- **Prometheus**: CPRA focuses on active health checking and remediation, not metrics collection
- **Nagios/Icinga**: CPRA scales to 10-100x more monitors with better performance
- **Custom Solutions**: CPRA provides production-ready worker pool management and queueing theory-based scaling out of the box

---

## Key Features

### 🚀 **Massive Scalability**
- Handles **1,000,000+ concurrent monitors** on commodity hardware
- Linear scaling with minimal overhead per monitor
- Memory-efficient design: ~100 bytes per monitor

### ⚡ **High Performance**
- **10,000+ health checks per second** per pipeline
- **P95 latency < 100ms** from schedule to result processing
- Batch processing and lock-free queues minimize overhead

### 🔄 **Automated Remediation**
- **Three independent pipelines:**
  1. **Pulse**: Health checking (HTTP, TCP, ICMP, custom scripts)
  2. **Intervention**: Automated recovery (restart services, scale resources, run scripts)
  3. **Code**: Alerting and notifications (email, SMS, webhooks, PagerDuty)

### 🧠 **Intelligent Scaling**
- **M/M/c queueing theory**: Automatically calculates optimal worker count
- **Allen-Cunneen approximation**: Handles real-world workload variability
- **SLO-driven sizing**: Dynamically scales to meet latency targets

### 🏗️ **Data-Oriented Architecture**
- **Entity-Component-System (ECS)** using [mlange-42/ark](https://github.com/mlange-42/ark)
- Cache-friendly memory layout for maximum performance
- Minimal allocations and GC pressure

### 🔧 **Production-Ready**
- Built-in pprof profiling for debugging
- Graceful shutdown with context cancellation
- Comprehensive logging with debug mode
- Memory management with automatic GC triggering

---

## Performance Characteristics

| Metric | Value |
|--------|-------|
| **Max Concurrent Monitors** | 1,000,000+ |
| **Throughput** | 10,000+ checks/sec/pipeline |
| **Latency (P95)** | < 100ms (configurable via SLO) |
| **Memory per Monitor** | ~100 bytes |
| **Total Memory (1M monitors)** | ~100 MB + worker pool overhead |
| **Worker Scaling** | Dynamic (M/M/c based) |

See [Architecture Overview](docs/explanation/architecture-overview.md#performance-characteristics) for detailed benchmarks and analysis.

---

## Architecture

CPRA uses a three-pipeline architecture built on Entity-Component-System principles:

![ECS Architecture](docs/images/ecs-architecture.png)

### Three Independent Processing Pipelines

![Pipeline Flow](docs/images/pipeline-flow.png)

1. **Pulse Pipeline**: Executes health checks (HTTP requests, TCP connections, custom scripts)
2. **Intervention Pipeline**: Performs automated remediation when monitors fail
3. **Code Pipeline**: Sends alert notifications to incident management systems

Each pipeline operates independently with its own queue and dynamically-scaled worker pool, enabling:
- **Pipeline-specific tuning**: Configure each pipeline separately
- **Fault isolation**: One pipeline failure doesn't affect others
- **Independent scaling**: Scale workers based on per-pipeline load

### Queue and Worker Pool Architecture

![Queue and Worker Pool](docs/images/queue-worker-pool.png)

**Queue Implementations:**
- **HybridQueue**: Ring buffer + min-heap for priority-based scheduling
- **AdaptiveQueue**: Auto-scaling ring buffer for variable load
- **WorkivaQueue**: Lock-free ring buffer for ultra-low latency

**Dynamic Worker Pools:**
- Powered by [panjf2000/ants](https://github.com/panjf2000/ants) goroutine pool
- Automatic scaling using M/M/c queueing theory
- Configurable min/max workers and SLO targets

For a comprehensive architecture explanation, see the [Architecture Overview](docs/explanation/architecture-overview.md).

---

## Quick Start

### Option 1: Run with Docker (Fastest)

```bash
# Clone the repository
git clone https://github.com/ziad/cpra.git
cd cpra

# Build the Docker image and run with 10,000 test monitors
docker build -f docker/Dockerfile -t cpra .
docker run -it --rm \
  -v $(pwd)/mock-servers/test_10k.yaml:/app/monitors.yaml \
  cpra \
  ./cpra --yaml /app/monitors.yaml
```

### Option 2: Build and Run Locally

```bash
# Prerequisites: Go 1.25 or later
go version  # Should show go1.25 or higher

# Build from source
git clone https://github.com/ziad/cpra.git
cd cpra
go build .

# Run with example configuration
./cpra --yaml mock-servers/test_10k.yaml
```

**Expected Output:**
```
Starting CPRA Optimized Controller for 1M Monitors
Profiling server listening at http://localhost:6060/debug/pprof/
Loading monitors from mock-servers/test_10k.yaml...
Monitor loading completed in 1.2s
[INFO] Controller started successfully
[INFO] Pulse pipeline processing 10,000 monitors
[INFO] Worker pool scaled to 143 workers (target SLO: 100ms)
```

---

## Installation

### Prerequisites

- **Go 1.25 or later** ([download](https://go.dev/dl/))
- **Docker** (optional, for containerized deployment)

### Building from Source

1. **Clone the repository:**
   ```bash
   git clone https://github.com/ziad/cpra.git
   cd cpra
   ```

2. **Download dependencies:**
   ```bash
   go mod download
   ```

3. **Build the application:**
   ```bash
   go build .
   ```

4. **Verify installation:**
   ```bash
   ./cpra --help
   ```

### Docker Deployment

1. **Build the Docker image:**
   ```bash
   docker build -f docker/Dockerfile -t cpra:latest .
   ```

2. **Run the container:**
   ```bash
   docker run -it --rm \
     -v $(pwd)/my-monitors.yaml:/app/monitors.yaml \
     cpra:latest \
     ./cpra --yaml monitors.yaml
   ```

---

## Configuration

### Monitor Configuration (YAML)

Create a `monitors.yaml` file to define health checks:

```yaml
monitors:
  - name: "my-service-health-check"
    pulse_check:
      type: http
      interval: 30s
      timeout: 5s
      max_failures: 3
      config:
        method: GET
        url: http://my-service.example.com/health
        retries: 2
    intervention:
      type: script
      config:
        script: /scripts/restart-service.sh
        timeout: 30s
    codes:
      red:
        dispatch: true
        notify: pagerduty
        config:
          url: https://events.pagerduty.com/v2/enqueue
      yellow:
        dispatch: true
        notify: log
        config:
          file: /var/log/cpra-alerts.log
```

**Example Configurations:**
- [10,000 monitors](mock-servers/test_10k.yaml)
- [50,000 monitors](mock-servers/test_50k.yaml)
- [1,000,000 monitors](mock-servers/test_1m.yaml)

### Application Configuration

Configure CPRA behavior programmatically:

```go
package main

import (
    "cpra/internal/controller"
)

func main() {
    config := controller.DefaultConfig()

    // Debug mode
    config.Debug = true

    // Worker pool settings (applies to all three pipelines)
    config.WorkerConfig.MinWorkers = 10
    config.WorkerConfig.MaxWorkers = 500

    // Queue settings
    config.QueueCapacity = 131072  // Must be power of 2

    // Performance tuning
    config.BatchSize = 2000
    config.SizingServiceTime = 20 * time.Millisecond  // Average job duration
    config.SizingSLO = 100 * time.Millisecond         // Target latency
    config.SizingHeadroomPct = 0.15                   // 15% safety buffer

    ctrl := controller.NewController(config)
    // ... rest of initialization
}
```

See the [API Reference](docs/reference/api-reference.md) for complete configuration options.

---

## Command-Line Options

```bash
./cpra [OPTIONS]
```

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `--yaml` | string | `internal/loader/replicated_test.yaml` | Path to monitors YAML file |
| `--config` | string | - | Configuration file path (optional) |
| `--debug` | bool | `false` | Enable debug-level logging |
| `--pprof` | bool | `true` | Enable pprof profiling server |
| `--pprof.addr` | string | `localhost:6060` | Pprof server listen address |

**Examples:**

```bash
# Run with debug logging
./cpra --yaml monitors.yaml --debug

# Run with custom pprof port
./cpra --yaml monitors.yaml --pprof.addr localhost:8080

# Disable profiling
./cpra --yaml monitors.yaml --pprof=false
```

---

## Documentation

### Comprehensive Guides

- **[Architecture Overview](docs/explanation/architecture-overview.md)** - System design, diagrams, and performance analysis
- **[API Reference](docs/reference/api-reference.md)** - Complete API documentation with function signatures
- **[Types Reference](docs/reference/types-reference.md)** - Data structures and component definitions
- **[Quickstart Tutorial](docs/tutorials/quickstart.md)** - Get started in 5-10 minutes
- **[Common Tasks](docs/how-to/common-tasks.md)** - How-to guides for typical operations

### Additional Resources

- **[Getting Started](docs/tutorials/getting-started.md)** - Detailed setup and deployment guide
- **[Examples](docs/examples/)** - Code examples for common use cases

---

## Troubleshooting

### Common Issues

**Issue: `YAML file not found`**
```
Warning: YAML file monitors.yaml not found, starting without loading monitors
```
**Solution:** Verify the file path is correct. Use absolute paths or paths relative to where you run the binary:
```bash
./cpra --yaml $(pwd)/monitors.yaml
```

---

**Issue: `Build fails with Go version error`**
```
go.mod requires go >= 1.25
```
**Solution:** Upgrade Go to version 1.25 or later:
```bash
go version  # Check current version
# Download Go 1.25+ from https://go.dev/dl/
```

---

**Issue: `High memory usage`**
**Solution:** Check memory usage with pprof:
```bash
# While CPRA is running, access pprof
go tool pprof http://localhost:6060/debug/pprof/heap

# View top memory consumers
(pprof) top
```

Adjust memory limits in configuration:
```go
config.WorkerConfig.MaxWorkers = 200  // Reduce max workers
config.QueueCapacity = 65536          // Reduce queue size
```

---

**Issue: `Worker pool not scaling`**
**Solution:** Enable debug logging to see scaling decisions:
```bash
./cpra --yaml monitors.yaml --debug
```

Check queueing theory parameters:
```go
config.SizingServiceTime = 50 * time.Millisecond  // Increase if jobs take longer
config.SizingSLO = 200 * time.Millisecond         // Relax SLO if needed
```

---

**Issue: `Monitors not executing`**
**Solution:** Verify monitor configuration format and check logs:
```bash
./cpra --yaml monitors.yaml --debug 2>&1 | grep ERROR
```

Validate YAML syntax:
```bash
# Use a YAML validator
python -m yaml monitors.yaml
```

---

### Getting Help

- **Documentation**: Check the [docs/](docs/) folder for detailed guides
- **Issues**: [Open an issue](https://github.com/ziad/cpra/issues) for bugs or feature requests
- **Discussions**: Ask questions and share ideas in GitHub Discussions
- **Logs**: Always provide logs when reporting issues (use `--debug` flag)

---

## Contributing

We welcome contributions from the community! CPRA is an open-source project and we appreciate:

- 🐛 Bug reports and fixes
- ✨ Feature requests and implementations
- 📖 Documentation improvements
- 🧪 Test coverage enhancements
- 💡 Performance optimizations

**Getting Started:**

1. Read the [Contributing Guidelines](CONTRIBUTING.md)
2. Check the [Code of Conduct](CODE_OF_CONDUCT.md)
3. Look for issues labeled [`good first issue`](https://github.com/ziad/cpra/labels/good%20first%20issue)
4. Fork the repository and submit a pull request

**Development Resources:**

- [Architecture Overview](docs/explanation/architecture-overview.md) - Understand the system design
- [API Reference](docs/reference/api-reference.md) - Function signatures and usage
- [Examples](docs/examples/) - Code examples for common patterns

---

## License

This project is licensed under the **MIT License** - see the [LICENSE](LICENSE) file for details.

---

## Acknowledgments

CPRA is built on excellent open-source libraries:

- [mlange-42/ark](https://github.com/mlange-42/ark) - High-performance Entity-Component-System
- [panjf2000/ants](https://github.com/panjf2000/ants) - Goroutine pool with dynamic scaling
- [Workiva/go-datastructures](https://github.com/Workiva/go-datastructures) - Lock-free data structures
- [uber-go/zap](https://github.com/uber-go/zap) - Structured logging

---

<div align="center">

**[Documentation](docs/)** • **[Architecture](docs/explanation/architecture-overview.md)** • **[Contributing](CONTRIBUTING.md)** • **[Issues](https://github.com/ziad/cpra/issues)**

Built with ❤️ for platform teams managing large-scale infrastructure

</div>
