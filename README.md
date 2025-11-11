# CPRA - Continuous Pulse and Recovery Agent

> High-performance uptime monitoring system designed to handle **1M+ concurrent monitors** in minimal memory footprint (**≤1GB RAM**)

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Architecture](https://img.shields.io/badge/Architecture-ECS-green.svg)](explanation/architecture-overview.md)

## 🎯 What is CPRA?

CPRA is an open-source monitoring and alerting system built with extreme performance in mind. Using an Entity-Component-System (ECS) architecture powered by the [Ark](https://github.com/mlange-42/ark) framework, CPRA achieves unprecedented scalability for uptime monitoring.

### Key Features

- 🚀 **Extreme Scalability** - Monitor 1M+ endpoints concurrently in ≤1GB RAM
- ⚡ **High Performance** - ECS architecture with optimized queue systems
- 🔄 **Auto-Recovery** - Automated intervention actions (Docker container restarts, etc.)
- 🎨 **Color-Coded Alerts** - Red/Yellow/Green/Cyan/Gray alert levels
- 📊 **Real-time Monitoring** - HTTP, TCP, ICMP health checks (DNS/Docker planned)
- 🔧 **Dynamic Configuration** - YAML/JSON streaming configuration loader
- 📈 **Built-in Profiling** - pprof integration for performance analysis

## 🏗️ Architecture

CPRA uses a **three-state system**:

1. **Pulse** - Health checks (HTTP, TCP, ICMP)
2. **Intervention** - Automated recovery actions
3. **Code** - Color-coded alerting system

**Technology Stack:**
- **Language**: Go 1.22+
- **ECS Framework**: [Ark](https://github.com/mlange-42/ark)
- **Worker Pools**: [Ants](https://github.com/panjf2000/ants)
- **Queue Systems**: Adaptive, Workiva, Hybrid queues
- **Configuration**: Streaming YAML/JSON parsers

For detailed architecture documentation, see [Architecture Overview](explanation/architecture-overview.md).

## ⚡ Quick Start

### Prerequisites

- Go 1.22 or later
- Docker (optional, for intervention features)

### Installation

```bash
# Clone the repository
git clone https://github.com/your-org/cpra.git
cd cpra

# Build the binary
make build

# Or use Go directly
go build -o cpra .
```

### Running CPRA

```bash
# Run with default configuration
./cpra

# Run with custom YAML file
./cpra -yaml path/to/monitors.yaml

# Enable debug logging
./cpra -debug

# Custom pprof address
./cpra -pprof.addr localhost:6060
```

### Your First Monitor

Create a `monitors.yaml` file:

```yaml
monitors:
  - name: "My API Health Check"
    pulse_check:
      type: http
      interval: 1m
      timeout: 10s
      max_failures: 3
      config:
        url: https://api.example.com/health
        method: GET
        expected_status: 200
    codes:
      red:
        dispatch: true
        notify: log
        config:
          file: alerts.log
```

See [Getting Started Tutorial](tutorials/getting-started.md) for a complete walkthrough.

## 📚 Documentation

Our documentation follows the [Diataxis](https://diataxis.fr/) framework:

- **[Tutorials](tutorials/)** - Learning-oriented guides
  - [Getting Started](tutorials/getting-started.md)
  - [Scaling to 1000s of Monitors](tutorials/scaling-guide.md)

- **[How-To Guides](how-to/)** - Task-oriented guides
  - [Configure Monitors](how-to/configure-monitors.md)
  - [Setup Interventions](how-to/setup-interventions.md)
  - [Configure Alerts](how-to/configure-alerts.md)
  - [Performance Tuning](how-to/performance-tuning.md)

- **[Reference](reference/)** - Information-oriented documentation
  - [Configuration Schema](reference/configuration-schema.md)
  - [API Reference](reference/api-documentation.md)
  - [CLI Reference](reference/cli-reference.md)
  - [Metrics Reference](reference/metrics.md)

- **[Explanation](explanation/)** - Understanding-oriented articles
  - [Architecture Overview](explanation/architecture-overview.md)
  - [ECS Design Rationale](explanation/ecs-design.md)
  - [Queue Strategies](explanation/queue-strategies.md)
  - [Memory Optimization](explanation/memory-optimization.md)

## 🔧 Configuration

CPRA supports configuration via:

- **YAML files** - Primary configuration method
- **JSON files** - Alternative format
- **Command-line flags** - Runtime overrides

### Basic Configuration Example

```yaml
monitors:
  - name: "Production API"
    pulse_check:
      type: http
      interval: 30s
      timeout: 5s
      max_failures: 3
      config:
        url: https://api.prod.example.com
        method: GET
    intervention:
      action: docker
      retries: 1
      target:
        container: api-service
    codes:
      red:
        dispatch: true
        notify: pagerduty
        config:
          integration_key: ${PAGERDUTY_KEY}
```

See [Configuration Schema](reference/configuration-schema.md) for all options.

## 📊 Performance

CPRA is designed for extreme performance:

- **Memory Usage**: ≤1GB for 1M monitors
- **Latency**: ≤2s for health checks
- **Uptime**: 99.9% target

### Profiling

CPRA includes built-in pprof support:

```bash
# Start CPRA with pprof enabled (default: localhost:6060)
./cpra -pprof

# Access profiles
# CPU: http://localhost:6060/debug/pprof/profile?seconds=30
# Heap: http://localhost:6060/debug/pprof/heap
# Goroutines: http://localhost:6060/debug/pprof/goroutine
```

See [Performance Tuning Guide](how-to/performance-tuning.md) for optimization strategies.

## 🛠️ Development

### Building

```bash
# Standard build
make build

# Secure build with hardening flags
make buildsec

# Run tests
make test

# Format code
make fmt

# Clean build artifacts
make clean
```

### Project Structure

```
cpra/
├── internal/
│   ├── controller/     # Main controller and ECS systems
│   ├── queue/          # Queue implementations (Adaptive, Workiva, Hybrid)
│   ├── jobs/           # Job definitions (Pulse, Intervention, Code)
│   ├── loader/         # Configuration loaders (YAML/JSON streaming)
│   ├── logger/         # Structured logging
│   └── alerts/         # Alert management
├── main.go             # Application entry point
├── Makefile            # Build automation
└── go.mod              # Go module dependencies
```

## 🤝 Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for:

- Development setup
- Coding standards
- Pull request process
- Issue reporting

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- [Ark](https://github.com/mlange-42/ark) - ECS framework
- [Ants](https://github.com/panjf2000/ants) - Goroutine pool library
- [Workiva](https://github.com/Workiva/go-datastructures) - Lock-free data structures

## 📞 Support

- **Documentation**: [CPRA Docs](https://your-org.github.io/cpra)
- **Issues**: [GitHub Issues](https://github.com/your-org/cpra/issues)
- **Discussions**: [GitHub Discussions](https://github.com/your-org/cpra/discussions)

---

**Made with ❤️ by the CPRA community**
