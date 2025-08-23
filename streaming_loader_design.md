# Streaming Entity Creation Design for 1M Monitors

## Problem
Loading 1M monitors takes forever because:
1. All monitors are parsed into memory first
2. All entities are created synchronously 
3. No progress feedback during loading

## Streaming Solution

### 1. Streaming YAML Parser
- Parse YAML line by line instead of loading entire file
- Emit each monitor as it's parsed
- Memory usage: O(1) per monitor instead of O(n) total

### 2. Pipeline Architecture
```
YAML File → Stream Parser → Entity Creator → ECS World
     ↓           ↓              ↓            ↓
  Read 1KB    Parse 1       Create 1      Add to
  chunks    Monitor      Entity        World
            at a time    at a time     immediately
```

### 3. Progress Reporting
- Show "Created 50,000/1,000,000 entities (5%)" 
- Estimate time remaining
- Memory usage stats

### 4. Batch Processing
- Create entities in batches of 1000
- Commit batches to avoid memory buildup
- Allow for graceful interruption/resume

## Files to Create
1. `streaming_yaml_parser.go` - Line-by-line YAML parsing
2. `streaming_entity_creator.go` - Batched entity creation
3. `streaming_loader.go` - Main orchestrator with progress
4. `streaming_main_example.go` - Example usage