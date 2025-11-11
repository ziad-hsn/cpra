// Package controller coordinates the ECS-based controller for CPRA.
//
// The controller package is the central orchestration layer that wires together
// ECS systems, queues, worker pools, and logging/tracing to manage monitor
// lifecycle, job execution, and graceful shutdown.
//
// # Architecture
//
// The controller uses the ark ECS library to manage monitor entities and their
// components. Key architectural decisions:
//
//   - Batch Processing: Systems process entities in batches to maximize throughput
//   - Queue Abstraction: Multiple queue implementations can be used based on workload
//   - Worker Pools: Dynamic worker pools with automatic M/M/c-based scaling
//   - Streaming Loader: Efficient YAML/JSON parsing for large monitor configurations
//
// # Three Pipeline Architecture
//
// CPRA processes work through three independent pipelines:
//
//   - Pulse Pipeline: Health checking (HTTP, TCP, ICMP)
//   - Intervention Pipeline: Automated recovery (Docker restart/stop/start/kill)
//   - Code Pipeline: Alert notifications (Log, Slack, PagerDuty, Email, Webhook)
//
// Each pipeline has its own queue, worker pool, and result router, enabling
// independent scaling and fault isolation.
//
// # Sub-packages
//
//   - controller/components: ECS component definitions (MonitorState, configs, etc.)
//   - controller/entities: Entity management and creation
//   - controller/systems: ECS systems for processing (BatchPulseSystem, etc.)
//
// # Example
//
//	config := controller.DefaultConfig()
//	config.Debug = true
//
//	oc, err := controller.NewController(config)
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	ctx := context.Background()
//	if err := oc.LoadMonitors(ctx, "monitors.yaml"); err != nil {
//		log.Fatal(err)
//	}
//
//	if err := oc.Start(ctx); err != nil {
//		log.Fatal(err)
//	}
//	defer oc.Stop()
//
// # Graceful Shutdown
//
// The controller supports graceful shutdown with configurable timeout:
//
//  1. Signal handling (SIGINT/SIGTERM)
//  2. ECS termination system signals app exit
//  3. Worker pools drain in-flight jobs
//  4. Queues close to prevent new enqueues
//  5. Logs flush and metrics collected
//
// # GC Tuning
//
// For large deployments (1M+ monitors):
//
//	GOMEMLIMIT=3200MiB ./cpra  # 70-80% of container memory
//	GOGC=100 ./cpra            # Default, tune based on workload
package controller
