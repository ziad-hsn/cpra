# CPRA Project Context & Technical Requirements

## Project Overview

**CPRA** (Continuous Pulse and Recovery Agent) is an open-source uptime monitoring tool with automated intervention capabilities. The name is a medical analogy to CPR (Cardiopulmonary Resuscitation) - it keeps services alive through continuous health checks and recovery actions.

**Core Purpose**: Monitor service health, execute interventions when failures occur, and send color-coded alerts. Runs as a lightweight binary on-premises, not a cloud service.

## Architecture Design

### Three-State System
1. **Pulse**: Health checks (HTTP, ICMP, TCP, Docker, DNS)
   - Checks service health at configured intervals
   - Returns success/failure results

2. **Intervention**: Automated recovery actions
   - Triggers after N failures
   - Currently: Docker restart
   - Future: Service restart, custom scripts (security considerations pending)

3. **Code**: Color-coded alerts
   - **Yellow**: First failure
   - **Green**: Service recovered
   - **Cyan**: Intervention succeeded
   - **Red**: Max failures reached
   - **Gray/Black**: Intervention failed
   - Smart deduplication needed (e.g., don't send both cyan and green)

### Technology Stack
- **Language**: Go
- **ECS Framework**: Ark (Entity Component System)
- **Queue**: Currently circular queue (buggy, needs replacement)
- **Workers**: Ants library with batch processors
- **Config**: YAML/JSON (problematic at 1M scale)

### Current Implementation Flow
1. Monitors defined in YAML/JSON config
2. Loaded as ECS entities with components (PulseConfig, InterventionConfig, etc.)
3. Systems process entities: PulseSystem → Queue → Workers → Results → InterventionSystem → CodeSystem
4. Results flow back through channels to update entity states

## Requirements

### Performance Targets
- **Scale**: 1,000,000 concurrent monitors
- **Memory**: 1GB maximum
- **Reliability**: 99.9% uptime
- **Latency**: 2s max for health checks
- **Configuration**: Everything dynamic from config file, no hardcoded values

### Future Roadmap (context only, not immediate)
- API library for runtime monitor injection
- CTL tool using Cobra
- Integration with Kafka/Redis for dynamic monitor creation
- Secret management (local solution needed)
- AI integration: "Observability for AI" and "AI for Observability"

After implementation:
- Zero stuck entities after 24h runtime
- Memory ≤1GB with 1M monitors
- 99.9% of health checks complete within 2s
- No OS resource exhaustion at any utilization level
- Graceful degradation under load
