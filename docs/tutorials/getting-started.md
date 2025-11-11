---
layout: default
title: Getting Started
parent: Tutorials
nav_order: 1
---

# Getting Started

This guide will walk you through the process of setting up and running CPRA on your local machine.

## Prerequisites

*   Go 1.18 or later
*   Docker (optional, for running in a container)

## Building from Source

1.  Clone the repository:
    ```sh
    git clone https://github.com/ziad-mid/cpra.git
    cd cpra
    ```

2.  Build the application:
    ```sh
    go build .
    ```

## Running the Application

To run the application, you need to provide a YAML file with the monitor configurations. An example file is provided at `internal/loader/replicated_test.yaml`.

```sh
./cpra --yaml internal/loader/replicated_test.yaml
```

You should see output indicating that the controller is starting and loading the monitors.

## Running with Docker

You can also run the application in a Docker container.

1.  Build the Docker image:
    ```sh
    docker build -t cpra .
    ```

2.  Run the Docker container:
    ```sh
    docker run -it --rm cpra
    ```

## What's Next?

Now that you have CPRA up and running, you can start to explore its features:

*   Create your own monitor configuration file.
*   Explore the different types of pulses, interventions, and codes.
*   Learn more about the architecture of CPRA in our [Architecture Overview](../explanation/architecture-overview.md).
