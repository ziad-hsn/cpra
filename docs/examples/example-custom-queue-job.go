// This example demonstrates creating a custom queue and worker pool as outlined in
// "How to Use the Queueing System for Custom Jobs".
//
// Usage: go run example-custom-queue-job.go
// Expected output: log lines from the worker pool plus a summary that the custom jobs completed.

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"cpra/internal/jobs"
	"cpra/internal/queue"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "custom queue example failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	qConfig := queue.DefaultQueueConfig()
	qConfig.Name = "audit-jobs"
	qConfig.HybridConfig.Name = qConfig.Name

	auditQueue, err := queue.NewQueue(qConfig)
	if err != nil {
		return fmt.Errorf("create queue: %w", err)
	}

	logger := log.New(os.Stdout, "[AuditPool] ", log.LstdFlags)
	poolConfig := queue.DefaultWorkerPoolConfig()
	poolConfig.MinWorkers = 2
	poolConfig.MaxWorkers = 8
	poolConfig.ResultBatchSize = 4

	pool, err := queue.NewDynamicWorkerPool(auditQueue, poolConfig, logger)
	if err != nil {
		return fmt.Errorf("create worker pool: %w", err)
	}
	pool.Start()
	defer pool.DrainAndStop()

	router := pool.GetRouter()
	var processed atomic.Int32
	const jobCount = 5

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case batch, ok := <-router.CodeResultChan:
				if !ok {
					return
				}
				for _, result := range batch {
					if result.Err != nil {
						log.Printf("custom job %s failed: %v", result.ID, result.Err)
						continue
					}
					payload := result.Payload
					fmt.Printf("Job %v finished: entity=%v detail=%v\n", result.ID, result.Entity(), payload["detail"])
					if processed.Add(1) == jobCount {
						return
					}
				}
			case <-ctx.Done():
				log.Printf("result reader exiting: %v", ctx.Err())
				return
			}
		}
	}()

	for i := 0; i < jobCount; i++ {
		job := &auditJob{
			id:      uuid.New(),
			detail:  fmt.Sprintf("audit-%02d", i+1),
			retries: 1,
		}
		if err := auditQueue.Enqueue(job); err != nil {
			return fmt.Errorf("enqueue job %s: %w", job.id, err)
		}
	}

	wg.Wait()

	fmt.Printf("Processed %d custom jobs using the CPRA queueing primitives.\n", processed.Load())
	return nil
}

type auditJob struct {
	id       uuid.UUID
	detail   string
	retries  int
	enqueue  time.Time
	start    time.Time
	attempts int
}

func (j *auditJob) Execute() jobs.Result {
	j.attempts++
	// Simulate lightweight work; retries would use j.retries in a real implementation.
	time.Sleep(25 * time.Millisecond)
	payload := map[string]interface{}{
		"type":    "code", // reuse the code channel in the router
		"detail":  j.detail,
		"attempt": j.attempts,
	}
	return jobs.Result{Payload: payload, ID: j.id}
}

func (j *auditJob) Copy() jobs.Job {
	clone := *j
	return &clone
}

func (j *auditJob) GetEnqueueTime() time.Time { return j.enqueue }

func (j *auditJob) SetEnqueueTime(t time.Time) { j.enqueue = t }

func (j *auditJob) GetStartTime() time.Time { return j.start }

func (j *auditJob) SetStartTime(t time.Time) { j.start = t }

func (j *auditJob) IsNil() bool { return j == nil }
