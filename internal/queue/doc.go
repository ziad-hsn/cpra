// Package queue provides thread-safe queue implementations and worker pool management
// for the CPRA system.
//
// The queue package abstracts job queuing behind a common interface, allowing
// different queue implementations to be used based on workload characteristics.
// It also provides dynamic worker pool management with automatic scaling based
// on queue depth and latency metrics.
//
// # Queue Implementations
//
// The package provides multiple queue implementations:
//
//   - HybridQueue: Combines a lock-free ring buffer with a fallback overflow slice.
//     Optimized for high-throughput scenarios with configurable drop policies.
//   - AdaptiveQueue: Lock-free circular queue with fixed capacity (power of 2).
//     Best for predictable workloads with bounded memory requirements.
//   - WorkivaQueue: Capacity-expanding queue using Workiva's RingBuffer.
//     Automatically grows to handle bursty workloads.
//   - BoundedQueue: Simple bounded queue for testing and low-volume scenarios.
//
// # Worker Pools
//
// DynamicWorkerPool provides automatic worker scaling based on:
//   - Queue depth and capacity
//   - Target queue latency (SLO)
//   - Current worker utilization
//   - M/M/c queueing theory calculations
//
// Worker pools can be paused/resumed and support graceful draining during shutdown.
//
// # Queueing Theory
//
// The package includes utilities for queueing theory calculations:
//   - M/M/c queue model for wait time prediction
//   - Allen-Cunneen approximation for variability handling
//   - Worker count recommendations based on arrival rate, service time, and SLO
//   - Headroom calculations for safety margins
//
// # Drop Policies
//
// When queues are full, the following policies are available:
//   - DropPolicyReject: Reject new jobs (returns error)
//   - DropPolicyDropNewest: Drop the just-arrived job
//   - DropPolicyDropOldest: Evict oldest job to admit new one
//
// # Example
//
//	cfg := queue.DefaultQueueConfig()
//	cfg.Name = "pulse"
//	cfg.HybridConfig.DropPolicy = queue.DropPolicyDropNewest
//	q, err := queue.NewQueue(cfg)
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer q.Close()
//
//	// Create worker pool
//	poolCfg := queue.DefaultWorkerPoolConfig()
//	pool, err := queue.NewDynamicWorkerPool(ctx, q, poolCfg, logger)
//	if err != nil {
//		log.Fatal(err)
//	}
//	pool.Start()
//	defer pool.DrainAndStop()
//
//	// Enqueue jobs
//	job := createJob()
//	if err := q.Enqueue(job); err != nil {
//		log.Printf("Failed to enqueue: %v", err)
//	}
package queue
