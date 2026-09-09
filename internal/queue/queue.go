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

	// EnqueueBatch adds jobs in order and can accept a prefix before returning
	// an error. Callers that need exact admission accounting must use Enqueue
	// per job; retrying the entire batch can duplicate already accepted work.
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
	ArrivalCV      float64       `json:"arrival_cv"`
	ArrivalSamples int           `json:"arrival_samples"`
	LastEnqueue    time.Time     `json:"last_enqueue"`
	LastDequeue    time.Time     `json:"last_dequeue"`
	AvgQueueTime   time.Duration `json:"avg_queue_time"`
	Dequeued       int64         `json:"dequeued"`
	Dropped        int64         `json:"dropped"`
	MaxQueueTime   time.Duration `json:"max_queue_time"`
	QueueDepth     int           `json:"queue_depth"`
	MaxJobLatency  time.Duration `json:"max_job_latency"`
	AvgJobLatency  time.Duration `json:"avg_job_latency"`
	EnqueueRate    float64       `json:"enqueue_rate"`
	DequeueRate    float64       `json:"dequeue_rate"`
	Enqueued       int64         `json:"enqueued"`
	Capacity       int           `json:"capacity"`
	SampleWindow   time.Duration `json:"sample_window"`
}
