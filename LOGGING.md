# CPRa Logging System

## Overview
The CPRa monitoring system now includes comprehensive logging with debug and production modes for better visibility into controller operations.

## Usage

### Debug Mode
Enable detailed logging for development and troubleshooting:

```bash
# Environment variable
export CPRA_DEBUG=true
./cpra

# Command line flag
./cpra --debug
./cpra -d
```

### Production Mode
Optimized logging for production environments:

```bash
# Set production environment
export CPRA_ENV=production
./cpra
```

## Log Levels

### Debug Mode Output
- **DEBUG**: Detailed component state changes, entity operations, timing information
- **INFO**: System startup, configuration, worker scaling, monitor state changes
- **WARN**: Channel congestion, component issues, retry operations
- **ERROR**: System errors, validation failures
- **FATAL**: Critical errors that cause shutdown

### Production Mode Output
- **WARN**: Important warnings and issues
- **ERROR**: System errors and failures  
- **FATAL**: Critical system failures

## Log Categories

### System Logger (`SYSTEM`)
- Application startup/shutdown
- Configuration loading
- Memory management
- Worker pool initialization

### Scheduler Logger (`SCHEDULER`) 
- Entity scheduling decisions
- Timing analysis (debug mode)
- Performance metrics
- Component state transitions

### Dispatch Logger (`DISPATCH`)
- Job dispatching
- Channel states
- Worker pool utilization
- Component transitions

### Result Logger (`RESULT`)
- Monitor results processing
- Success/failure analysis
- Recovery notifications
- Intervention triggers

### Worker Pool Logger (`WORKER`)
- Pool statistics
- Throughput metrics
- Queue depths
- Performance monitoring

### Entity Logger (`ENTITY`)
- Entity lifecycle events
- Component additions/removals
- State transitions

## Sample Debug Output

```
2025-08-16 16:54:04.123 [INFO] [SYSTEM] Starting CPRa Monitoring System
2025-08-16 16:54:04.124 [DEBUG] [SYSTEM] Debug mode enabled - verbose logging active
2025-08-16 16:54:04.125 [INFO] [SYSTEM] Configuration loaded successfully: 10000 monitors
2025-08-16 16:54:04.126 [INFO] [SYSTEM] Worker scaling: 1000 workers for 10000 monitors (CPU: 16, ratio: 62.5x)
2025-08-16 16:54:04.127 [DEBUG] [SCHEDULER] Entity[1001] first check scheduled (age: 1.2s, interval: 1s)
2025-08-16 16:54:04.128 [DEBUG] [DISPATCH] Dispatched API-Check-001 job for entity: 1001
2025-08-16 16:54:04.129 [DEBUG] [RESULT] Monitor API-Check-001 pulse successful
```

## Sample Production Output

```
2025-08-16 16:54:04 [INFO] [SYSTEM] Starting CPRa Monitoring System
2025-08-16 16:54:04 [INFO] [SYSTEM] Configuration loaded successfully: 10000 monitors  
2025-08-16 16:54:04 [INFO] [SYSTEM] Worker scaling: 1000 workers for 10000 monitors
2025-08-16 16:54:04 [INFO] [RESULT] Monitor API-Check-001 recovered after failure
2025-08-16 16:54:04 [WARN] [DISPATCH] Job channel full, skipping dispatch for API-Check-002
```

## Log Files

### Production Mode
- Logs written to: `cpra-YYYY-MM-DD.log`
- Automatic daily rotation
- Structured format for log analysis

### Debug Mode
- Console output only
- Color-coded by log level
- Real-time visibility

## Performance Monitoring

The system provides performance statistics every 30 seconds:

```
=== Worker Pool Performance (Uptime: 5m30s) ===
Pool: pulse
  Workers: 1000
  Jobs Processed: 15000 (45.5/sec)
  Jobs Dropped: 0
  Results Dropped: 12
  Current Queue Depth: 150
  Avg Latency: 1.2s
  Max Latency: 5.8s
  Efficiency: 99.9%
```

## Troubleshooting

### High Queue Depths
```
[WARN] [DISPATCH] Channel[pulse] is full (500000/500000)
```
**Solution**: Increase worker count or buffer sizes

### Component State Issues  
```
[WARN] [RESULT] Entity 1001 (API-Check-001) missing required components
```
**Solution**: Check entity initialization logic

### Performance Issues
```
[DEBUG] [SCHEDULER] Performance: PulseScheduler processed 1000 entities in 2.5s (400.0/sec)
```
**Analysis**: Use debug mode to identify bottlenecks

## Configuration

### Environment Variables
- `CPRA_DEBUG=true` - Enable debug mode
- `CPRA_ENV=production` - Enable production mode
- `CPRA_LOG_LEVEL` - Override log level (DEBUG/INFO/WARN/ERROR)

### Command Line Options
- `--debug` or `-d` - Enable debug mode
- `--verbose` or `-v` - Increase verbosity