# Mock Server Generator

This project provides a lightweight, scalable mock server generator using Go and Docker. It's designed to simulate a large number of HTTP servers on a single machine for testing purposes, such as for uptime monitoring tools.

## Overview

The setup consists of a single Go application (`main.go`) that acts as a server pool manager. This application can launch and manage tens of thousands of individual, lightweight HTTP servers, each on its own port. The entire system is containerized with Docker for easy deployment and scaling.

A management interface is provided to monitor the status of the server pool, retrieve server endpoints, and scale the number of running servers up or down.

## Features

-   **Scalable:** Can run up to ~50,000 mock servers on a single machine, limited primarily by available TCP ports and system resources.
-   **Lightweight:** Each server is a minimal Go HTTP server, consuming very few resources.
-   **Dynamic Management:** A central management API allows you to get statistics, list server endpoints, and start new servers on the fly.
-   **Individual Control:** Each mock server has its own `/health` and `/kill` endpoints, allowing you to check its status or terminate it individually.
-   **Containerized:** Packaged in a Docker image for portability and easy deployment.

## Getting Started

### Prerequisites

-   [Docker](https://docs.docker.com/get-docker/) installed and running.
-   Python 3 (for the CLI client and helper scripts)
-   The required Python packages installed. You can install them using pip:
    ```bash
    pip install -r requirements.txt
    ```

### Quick Start

The easiest way to start mock servers is using the provided command-line script:

```bash
# Start 10 servers with default settings
python3 start_servers.py 10

# Start 100 servers with custom port range
python3 start_servers.py 100 --base-port 20000

# Interactive management
python3 client.py
```

### Manual Docker Setup

If you prefer to use Docker directly, you can build and run the image manually:

#### Building the Docker Image

To build the Docker image for the mock server generator, run the following command from the project's root directory:

```bash
docker build -t mock-server:latest .
```

This will compile the Go application and create a Docker image named `mock-server` with the tag `latest`.

#### Running the Mock Servers

You can run the mock servers using the `docker run` command. You need to specify the number of servers to start as an argument.

### Example: Running 100 Servers

To start 100 mock servers, run the following command:

```bash
docker run -d --name mock-servers-100 -p 9999:9999 -p 10000-10100:10000-10100 mock-server:latest ./main 100
```

This command does the following:
-   `-d`: Runs the container in detached mode (in the background).
-   `--name mock-servers-100`: Assigns a name to the container for easy reference.
-   `-p 9999:9999`: Maps the management server port to port 9999 on your host machine.
-   `-p 10000-10100:10000-10100`: Maps the ports for the mock servers. The range should be large enough to accommodate the number of servers you are starting.
-   `mock-server:latest`: Specifies the image to use.
-   `./main 100`: This is the command run inside the container, telling the application to start 100 servers.

### Stopping the Servers

To stop and remove the container, you can use the name you assigned to it:

```bash
docker stop mock-servers-100
docker rm mock-servers-100
```

## Management API

The management API is available on port `9999` (or whichever port you mapped it to).

### Get Statistics

-   **Endpoint:** `/stats`
-   **Method:** `GET`
-   **Description:** Returns a JSON object with statistics about the server pool, including the number of active servers, total started, and total killed.

-   **Example:**
    ```bash
    curl http://localhost:9999/stats
    ```

### Get Server Endpoints

-   **Endpoint:** `/endpoints`
-   **Method:** `GET`
-   **Description:** Returns a plain text list of health check URLs for the running servers.
-   **Query Parameters:**
    -   `limit` (optional, integer): The maximum number of endpoints to return. Defaults to `1000`.

-   **Example:**
    ```bash
    # Get the first 50 endpoints
    curl "http://localhost:9999/endpoints?limit=50"
    ```

### Scale Up

-   **Endpoint:** `/scale`
-   **Method:** `GET`
-   **Description:** Starts a new batch of servers.
-   **Query Parameters:**
    -   `count` (required, integer): The number of new servers to start.

-   **Example:**
    ```bash
    # Start 200 more servers
    curl "http://localhost:9999/scale?count=200"
    ```

## Interacting with Individual Servers

Each mock server exposes its own simple API.

### Health Check

-   **Endpoint:** `/health`
-   **Method:** `GET`
-   **Description:** Returns a `200 OK` with a JSON body indicating the server is healthy. This is useful for uptime checkers.

-   **Example (for a server on port 10021):**
    ```bash
    curl http://localhost:10021/health
    ```

### Kill Server

-   **Endpoint:** `/kill`
-   **Method:** `GET`
-   **Description:** Shuts down that specific server instance. This simulates a server failure.

-   **Example (to kill the server on port 10021):**
    ```bash
    curl http://localhost:10021/kill
    ```

## Simulating Server Failures

To test an uptime monitor or alerting system, you need to be able to simulate server failures. You can easily do this by killing individual mock servers while the rest of the pool remains active.

Here’s how to do it:

1.  **Get the list of active server endpoints.**

    Use the management API to get the list of running servers:

    ```bash
    curl "http://localhost:9999/endpoints?limit=10"
    ```

    This will give you a list of URLs, for example:

    ```
    http://localhost:10001/health
    http://localhost:10002/health
    http://localhost:10003/health
    ...
    ```

2.  **Choose a server to kill.**

    Let's say you want to kill the server running on port `10002`.

3.  **Send the kill command.**

    Use `curl` to hit the `/kill` endpoint of that specific server:

    ```bash
    curl http://localhost:10002/kill
    ```

    This will shut down the server on port `10002`. The other servers will continue to run.

4.  **Verify that the server is down.**

    If you try to access the health check endpoint of the killed server, you will get an error:

    ```bash
    curl http://localhost:10002/health
    # curl: (7) Failed to connect to localhost port 10002 after 0 ms: Connection refused
    ```

    You can also check the `/stats` endpoint of the management server to see the `current_alive` count decrease.

### Restarting a Server

You can restart a server that you have previously killed by using the `/revive` endpoint.

-   **Endpoint:** `/revive`
-   **Method:** `GET`
-   **Description:** Restarts a stopped server on a specific port.
-   **Query Parameters:**
    -   `port` (required, integer): The port of the server to restart.

-   **Example (to restart the server on port 10002):**

    ```bash
    curl "http://localhost:9999/revive?port=10002"
    ```

    After running this command, the server on port `10002` will be active again. You can verify this by checking its `/health` endpoint or the main `/stats` endpoint.

## CLI Client

A Python-based interactive shell is provided to manage the mock server environment. This client simplifies starting, stopping, and interacting with the servers.

### Prerequisites

-   Python 3
-   Docker installed and running
-   The required Python packages installed. You can install them using pip:
    ```bash
    pip install -r requirements.txt
    ```

### Usage

To start the client, run the following command:

```bash
python3 client.py
```

This will launch an interactive shell. Type `help` to see a list of available commands.

### Commands

-   `start <num_servers> [--base-port <port>] [--max-port <port>]`: Starts the Docker container with the specified number of mock servers. Optional base port (default: 10000) and max port (default: 60000) parameters for port range configuration.
-   `stop_all`: Stops and removes the mock server container.
-   `stats`: Fetches and displays statistics from the management API.
-   `endpoints [limit]`: Fetches and displays the health check endpoints of the running servers.
-   `kill <port>`: Kills the server running on the specified port.
-   `revive <port>`: Restarts a killed server on the specified port.
-   `scale <num_servers>`: Starts a new batch of servers.
-   `status`: Check if the mock servers are running and show statistics.
-   `logs [--tail <lines>]`: Show logs from the running container.
-   `help`: Displays a list of commands and their usage.
-   `quit` or `exit`: Exits the shell.

### Quick Start Examples

```bash
# Start the client
python3 client.py

# Start 10 servers with default port range (10000-60000)
(mock-server) start 10

# Start 100 servers with custom port range (20000-30000)
(mock-server) start 100 --base-port 20000 --max-port 30000

# Check status
(mock-server) status

# Get statistics
(mock-server) stats

# Get first 20 endpoints
(mock-server) endpoints 20

# Kill a server on port 10005
(mock-server) kill 10005

# Stop all servers
(mock-server) stop_all
```

## Command-Line Interface

In addition to the interactive client, a simple command-line script `start_servers.py` is provided for quick server startup:

### Usage

```bash
python3 start_servers.py <num_servers> [--base-port <port>] [--max-port <port>] [--rebuild]
```

### Arguments

-   `num_servers`: Number of mock servers to start (required)
-   `--base-port`: Starting port number for servers (default: 10000)
-   `--max-port`: Maximum port number (default: 60000)
-   `--rebuild`: Force rebuild of Docker image before starting

### Examples

```bash
# Start 10 servers with default ports
python3 start_servers.py 10

# Start 100 servers starting from port 20000
python3 start_servers.py 100 --base-port 20000

# Start 50 servers in a specific port range
python3 start_servers.py 50 --base-port 15000 --max-port 20000

# Rebuild image and start servers
python3 start_servers.py 10 --rebuild
```

The script handles Docker image building automatically and provides helpful output including:
- Container status and ID
- Management API endpoint
- Server port range
- Health check verification
- Next step commands

## Generating Configuration Files

The `replace_endpoints.py` script can be used to take an existing YAML configuration file and replace all `url` or `host` fields with the endpoints from the running mock server container.

### Prerequisites

-   Python 3
-   The required Python packages installed. You can install them using pip:
    ```bash
    pip install -r requirements.txt
    ```

### Usage

The script can fetch endpoints directly from the management server of the running container.

Here is a step-by-step example:

1.  **Create a sample input YAML file.**

    Create a file named `sample_config.yaml` with the following content:

    ```yaml
    services:
      - name: service-a
        url: http://example.com/api/v1
      - name: service-b
        host: another-service.example.com

    - name: another-service
      url: http://yet-another-service.com/api
    ```

2.  **Start the mock servers.**

    Make sure the mock server container is running. For example, to start 10 servers:

    ```bash
    python3 client.py
    (mock-server) start 10
    ```

3.  **Run the `replace_endpoints.py` script.**

    This command will fetch the endpoints from the running container and use them to create a new YAML file.

    ```bash
    python3 replace_endpoints.py sample_config.yaml generated_config.yaml --endpoints-url http://localhost:9999/endpoints
    ```

4.  **Check the output.**

    The `generated_config.yaml` file will now contain the new endpoints:

    ```yaml
    services:
      - name: service-a
        url: http://localhost:10000/health
      - name: service-b
        host: http://localhost:10001/health

    - name: another-service
      url: http://localhost:10002/health
    ```

    The original URLs and hosts have been replaced with the actual endpoints of the running mock servers.

## Scalability

The system is designed to be scalable. The primary limiting factor is the number of available TCP ports on the host machine (up to 65,535). The `main.go` application is configured to use ports from `10000` to `60000`, allowing for a maximum of **50,001** servers.

To run a very large number of servers (e.g., 50,000), you will need a machine with sufficient RAM and CPU resources. You may also need to adjust OS-level limits, such as the maximum number of open file descriptors.

### Example: Running 50,000 Servers

```bash
# Make sure your system has enough resources before running this!
docker run -d --name mock-servers-50k \
  -p 9999:9999 \
  -p 10000-60000:10000-60000 \
  mock-server:latest \
  ./main 50000
```

