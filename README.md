# CPRA - High-Performance Monitoring System

CPRA is a high-performance, event-driven monitoring application designed for massive-scale environments. It is built on a data-oriented **Entity-Component-System (ECS)** architecture, which provides a highly decoupled, modular, and performant foundation.

## TL;DR

CPRA is a monitoring system that can handle over a million concurrent monitors. It's fast, resilient, and extensible.

**Key features:**
- **Scalability:** Designed to handle over one million concurrent monitors.
- **High Performance:** Optimized for high throughput and low latency, with features like batch processing, backpressure, and memory optimization.
- **Resilience:** The system is designed to be resilient to failures, with automated recovery and remediation mechanisms.

## Features

*   **Scalability:** Designed to handle over one million concurrent monitors.
*   **High Performance:** Optimized for high throughput and low latency, with features like batch processing, backpressure, and memory optimization.
*   **Resilience:** The system is designed to be resilient to failures, with automated recovery and remediation mechanisms.
*   **Modularity:** The ECS architecture makes the system highly modular and extensible.
*   **Advanced Performance Modeling:** Uses M/M/c queuing theory to proactively calculate the optimal number of workers to meet SLOs.

## Architecture

The core of the application consists of three independent processing pipelines:

1.  **Pulse Pipeline:** The primary health-checking mechanism.
2.  **Intervention Pipeline:** An automated recovery/remediation system.
3.  **Code Pipeline:** An alerting and notification system.

These pipelines are implemented as state machines using an ECS architecture, and they communicate indirectly through component state changes.

## Getting Started

### Prerequisites

*   Go 1.18 or later

### Building from Source

1.  Clone the repository:
    ```sh
    git clone https://github.com/ziad-mid/cpra.git
    cd cpra
    ```

2.  Build the application:
    ```sh
    go build .
    ```

### Running the Application

```sh
./cpra --yaml <path_to_your_monitors.yaml>
```

## Configuration

The application can be configured through a YAML file. See `internal/loader/replicated_test.yaml` for an example.

## Docker

The application can also be run in a Docker container.

1.  Build the Docker image:
    ```sh
    docker build -t cpra .
    ```

2.  Run the Docker container:
    ```sh
    docker run -it --rm cpra
    ```

## Contributing

Contributions are welcome! Please see our [Contributing Guidelines](CONTRIBUTING.md) for more information on how to get started. We also have a [Code of Conduct](CODE_OF_CONDUCT.md) that we expect all contributors to adhere to.

We use GitHub Issues to track bugs and feature requests. Please search for existing issues before creating a new one. If you're looking for a good place to start, check out our issues labeled "good first issue".

## License

This project is licensed under the terms of the MIT license. See the [LICENSE](LICENSE) file for details.
