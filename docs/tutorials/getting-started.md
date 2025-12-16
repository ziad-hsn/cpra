---
layout: default
title: Getting Started
parent: Tutorials
nav_order: 1
---

# Getting Started

This guide will walk you through the process of setting up and running CPRA on your local machine.

## Prerequisites

*   Go 1.25 or later
*   Docker (optional, for running in a container)

## Building from Source

1.  Clone the repository:
    ```sh
    git clone https://github.com/ziad/cpra.git
    cd cpra
    ```

2.  Build the application:
    ```sh
    go build .
    ```

## Running the Application

To run the application, you need to provide a YAML file with the monitor configurations. Use `mock-servers/generate_monitors.py` to generate test configurations:

```sh
# Generate test monitors
cd mock-servers && python generate_monitors.py --count 10000 --output test_10k.yaml && cd ..

# Run CPRA
./cpra --yaml mock-servers/test_10k.yaml
```

You should see output indicating that the controller is starting and loading the monitors.

## Running with Docker

You can also run the application in a Docker container.

1.  Build the Docker image using the provided Dockerfile:
    ```sh
    docker build -f docker/Dockerfile -t cpra .
    ```

    Note: If the build fails due to a missing `samples/` directory referenced in the Dockerfile, either create `cpra/samples` with your YAML files, or remove the `COPY samples samples` line from `docker/Dockerfile` before building.



## What's Next?

Now that you have CPRA up and running, you can start to explore its features:

*   Create your own monitor configuration file.
*   Explore the different types of pulses, interventions, and codes.
*   Learn more about the architecture of CPRA in our [Architecture Overview](../explanation/architecture-overview.md).
