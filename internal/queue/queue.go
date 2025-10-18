package queue

import (
	"cpra/internal/jobs"
	"time"
)

// Queue defines the interface for a generic, thread-safe queue system.
// This allows the controller and systems to be decoupled from a specific queue implementation.
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
}

// Stats holds performance metrics for a queue.
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
}
