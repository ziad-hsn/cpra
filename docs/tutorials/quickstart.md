---
title: Quickstart
parent: Tutorials
---

# Quickstart

This tutorial provides the fastest way to get CPRA running on your local machine using Docker. In just a few minutes, you will build the application, run it with a sample configuration of 10,000 monitors, and observe the system's core functionality.

This approach is ideal for a first-time evaluation of CPRA, as it requires no Go environment setup.

## Prerequisites

*   **Docker:** You must have Docker installed and the Docker daemon running. You can find installation instructions at the [official Docker website](https://docs.docker.com/get-docker/).
*   **Git:** You will need Git to clone the project repository.

## Step 1: Clone the Repository

First, open your terminal and clone the `ziad-hsn/cpra` repository to your local machine.

```bash
$ git clone https://github.com/ziad-hsn/cpra.git
$ cd cpra
```

This will download the project source code, including the Dockerfile and the sample monitor configurations we will use.

## Step 2: Build the Docker Image

Next, use the provided Dockerfile to build a container image for CPRA. This command compiles the Go application inside a controlled Docker environment and packages it into a lightweight image named `cpra:latest`.

```bash
$ docker build -f docker/Dockerfile -t cpra:latest .
```

## Step 3: Run the CPRA Container

Now, run the container you just built. This command starts CPRA and uses a volume mount (`-v`) to provide the `test_10k.yaml` file from your local machine as the monitor configuration inside the container.

```bash
$ docker run -it --rm \
  -v $(pwd)/mock-servers/test_10k.yaml:/app/monitors.yaml \
  cpra:latest \
  ./cpra --yaml /app/monitors.yaml
```

## Step 4: Analyze the Output

If successful, you will see log messages indicating that the controller has started, loaded the 10,000 monitors, and dynamically scaled its worker pool to meet the default performance targets.

```text
# Expected Log Output

Starting CPRA Optimized Controller for 1M Monitors
Profiling server listening at http://localhost:6060/debug/pprof/
Loading monitors from mock-servers/test_10k.yaml...
Monitor loading completed in 1.2s
[INFO] Controller started successfully
[INFO] Pulse pipeline processing 10,000 monitors
[INFO] Worker pool scaled to 143 workers (target SLO: 100ms)
```

This output confirms that:
1.  The application started correctly.
2.  It successfully parsed the YAML configuration file.
3.  The **Pulse Pipeline** is active and processing checks.
4.  The **dynamic scaling** feature has calculated and provisioned the optimal number of workers (143 in this example) to handle the load while respecting the 100ms Service Level Objective (SLO).

---

### **Next Steps**

Congratulations! You have successfully started CPRA. To learn how to define your own health checks, proceed to the next tutorial:

*   **[Your First Custom Monitor](your-first-monitor.md)**
