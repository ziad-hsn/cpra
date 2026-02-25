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
// The package provides three queue implementations:
//
//   - HybridQueue: Combines a lock-free ring buffer with a fallback channel-based
//     queue. Optimized for high-throughput scenarios with configurable drop policies.
//   - AdaptiveQueue: Lock-free circular queue with fixed capacity (power of 2).
//     Best for predictable workloads with bounded memory requirements.
//   - WorkivaQueue: Capacity-expanding queue using Workiva's RingBuffer.
//     Automatically grows to handle bursty workloads.
//
// # Worker Pools
//
// DynamicWorkerPool provides automatic worker scaling based on:
//   - Queue depth and capacity
//   - Target queue latency (SLO)
//   - Current worker utilization
//
// Worker pools can be paused/resumed and support graceful draining during shutdown.
//
// # Queueing Theory
//
// The package includes utilities for queueing theory calculations:
//   - M/M/c queue model for wait time prediction
//   - Worker count recommendations based on arrival rate, service time, and SLO
//   - Headroom calculations for safety margins
//
// # Example
//
//	cfg := queue.DefaultQueueConfig()
//	cfg.Name = "pulse"
//	q, err := queue.NewQueue(cfg)
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer q.Close()
//
//	job := jobs.CreatePulseJob(...)
//	if err := q.Enqueue(job); err != nil {
//		log.Printf("Failed to enqueue: %v", err)
//	}
package queue

import (
	"cpra/internal/runtime/jobs"
	"time"
)

// Queue defines the interface for a generic, thread-safe queue system.
//
// Queue implementations must be safe for concurrent use by multiple goroutines.
// The interface allows the controller and systems to be decoupled from specific
// queue implementations, enabling runtime selection based on workload.
//
// All methods should handle nil jobs gracefully and return appropriate errors
// for closed queues or capacity exceeded scenarios.
type Queue interface {
	// Enqueue adds a single job to the queue.
	Enqueue(job jobs.Job) error

	// EnqueueBatch adds a slice of jobs to the queue.
	EnqueueBatch(jobs []interface{}) error

	// Dequeue removes and returns a single job from the queue.
	Dequeue() (jobs.Job, error)

	// DequeueBatch removes and returns a batch of jobs from the queue.
	DequeueBatch(maxSize int) ([]jobs.Job, error)

	// Close shuts down the queue and prevents new jobs from being enqueued.
	Close()

	// Stats returns statistics about the queue's performance.
	Stats() Stats

	// Notify returns a channel that signals when new jobs are available.
	Notify() <-chan struct{}
}

// Stats holds performance metrics for a queue.
//
// Stats provides comprehensive metrics for monitoring queue health and performance.
// All time-based metrics are computed over a sliding window (SampleWindow).
// Rate metrics (EnqueueRate, DequeueRate) are computed as moving averages.
type Stats struct {
	LastEnqueue   time.Time
	LastDequeue   time.Time
	AvgQueueTime  time.Duration
	Dequeued      int64
	Dropped       int64
	MaxQueueTime  time.Duration
	QueueDepth    int
	MaxJobLatency time.Duration
	AvgJobLatency time.Duration
	EnqueueRate   float64
	DequeueRate   float64
	Enqueued      int64
	Capacity      int
	SampleWindow  time.Duration
	// Rolling latency over recent samples (not lifetime averages)
	RollingAvgWait time.Duration
	RollingP50Wait time.Duration
	RollingP95Wait time.Duration
	RollingSamples int

	// EMA wait for responsive drift tracking
	EMAWait time.Duration

	// Time-bucketed stats for per-minute analysis
	BucketAvg1m time.Duration // avg wait in last minute
	BucketMax1m time.Duration // max wait in last minute
	BucketCnt1m int64         // samples in last minute
}
