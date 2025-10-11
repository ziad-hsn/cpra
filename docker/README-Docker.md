# CPRA Docker Memory Testing

This guide shows how to run the CPRA application in a Docker container with configurable memory limits.

**Note:** All Docker files are now organized in the `docker/` directory.

## Quick Start

1. **Navigate to docker directory and copy the example environment file:**
   ```bash
   cd docker
   cp .env.example .env
   ```

2. **Build and run with default settings (1GB memory limit):**
   ```bash
   docker-compose up --build
   ```

## Configuration

### Memory Limit Testing

Edit the `.env` file to change memory settings:

```bash
# For 512MB limit
GOMEMLIMIT=536870912

# For 1GB limit (default)
GOMEMLIMIT=1073741824

# For 2GB limit
GOMEMLIMIT=2147483648
```

### Changing YAML Files

To test with different YAML files:

```bash
# Method 1: Edit .env file
YAML_FILE=samples/another_test_file.yaml

# Method 2: Override environment variable
YAML_FILE=samples/your_file.yaml docker-compose up
```

## Docker Commands

### Build and Run
```bash
# Build and run in foreground
docker-compose up --build

# Build and run in background
docker-compose up -d --build

# Stop the container
docker-compose down
```

### Monitor Resource Usage
```bash
# Check memory usage in real-time
docker stats cpra-memory-test

# View container logs
docker-compose logs -f cpra-memory-test
```

### Alternative: Direct Docker Run

```bash
# Navigate to docker directory first
cd docker

# Build the image
docker build -t cpra-memory-test -f Dockerfile ..

# Run with 1GB memory limit
docker run --memory=1g \
  --name cpra-memory-test \
  -e GOMEMLIMIT=1073741824 \
  -v $(pwd)/../samples:/app/samples:ro \
  cpra-memory-test \
  --yaml samples/replicated_test_10k.yaml

# Run with 512MB memory limit
docker run --memory=512m \
  --name cpra-memory-test \
  -e GOMEMLIMIT=536870912 \
  -v $(pwd)/../samples:/app/samples:ro \
  cpra-memory-test \
  --yaml samples/replicated_test_10k.yaml
```

## Memory Testing Scenarios

### Test Different Memory Limits

1. **Low Memory Test (256MB):**
   ```bash
   GOMEMLIMIT=268435456 docker-compose up
   ```

2. **Medium Memory Test (512MB):**
   ```bash
   GOMEMLIMIT=536870912 docker-compose up
   ```

3. **High Memory Test (2GB):**
   ```bash
   GOMEMLIMIT=2147483648 docker-compose up
   ```

### Test Different YAML Files

```bash
# Test with specific file
YAML_FILE=samples/replicated_test_10k.yaml docker-compose up

# Test with debug logging
CPRA_DEBUG=true docker-compose up

# Test with CPU profiling
docker-compose run --rm cpra-memory-test ./cpra --yaml samples/replicated_test_10k.yaml --profile
```

## Troubleshooting

### Out of Memory Errors

If you encounter OOM errors:
1. Reduce the `GOMEMLIMIT` value
2. Lower the `GOGC` value (e.g., `GOGC=50` for more frequent GC)
3. Reduce the memory limit in docker-compose.yml

### Debug Mode

Enable debug logging to see detailed information:
```bash
CPRA_DEBUG=true docker-compose up
```

### Container Inspection

```bash
# Inspect container resources
docker inspect cpra-memory-test | grep -A 10 "Memory"

# Check Go memory stats
docker exec cpra-memory-test ./cpra --debug
```

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `GOMEMLIMIT` | Go memory limit in bytes | `1073741824` (1GB) |
| `GOGC` | GC trigger percentage | `100` |
| `CPRA_DEBUG` | Enable debug logging | `false` |
| `YAML_FILE` | Path to YAML configuration | `samples/replicated_test_10k.yaml` |