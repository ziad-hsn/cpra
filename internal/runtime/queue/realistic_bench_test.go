package queue

import (
	"context"
	"cpra/internal/runtime/jobs"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// createTestJob creates a minimal test job for benchmarking
func createTestJob(id uint64) jobs.Job {
	return &testJob{id: id}
}

type testJob struct {
	id          uint64
	enqueueTime time.Time
	startTime   time.Time
}

func (j *testJob) Execute(ctx context.Context) jobs.Result {
	return jobs.Result{Ent: ecs.Entity{}}
}
func (j *testJob) Copy() jobs.Job                    { return &testJob{id: j.id} }
func (j *testJob) GetEnqueueTime() time.Time         { return j.enqueueTime }
func (j *testJob) SetEnqueueTime(t time.Time)        { j.enqueueTime = t }
func (j *testJob) GetStartTime() time.Time           { return j.startTime }
func (j *testJob) SetStartTime(t time.Time)          { j.startTime = t }
func (j *testJob) IsNil() bool                       { return j == nil }

// BenchmarkHybridQueueRealisticWorkload simulates realistic production workload
// with multiple concurrent producers and consumers
func BenchmarkHybridQueueRealisticWorkload(b *testing.B) {
	scenarios := []struct {
		name      string
		producers int
		consumers int
		batchSize int
	}{
		{"1P_1C", 1, 1, 1},
		{"4P_4C", 4, 4, 1},
		{"8P_4C", 8, 4, 1},        // More producers than consumers (common case)
		{"16P_8C", 16, 8, 1},      // High contention
		{"4P_4C_Batch64", 4, 4, 64},
		{"8P_4C_Batch128", 8, 4, 128},
	}

	for _, sc := range scenarios {
		b.Run(sc.name, func(b *testing.B) {
			cfg := DefaultHybridQueueConfig()
			cfg.RingCapacity = 65536  // Smaller for benchmark
			cfg.OverflowCapacity = 8192
			q, err := NewHybridQueue(cfg)
			if err != nil {
				b.Fatal(err)
			}
			defer q.Close()

			var wg sync.WaitGroup
			var enqueued, dequeued atomic.Int64
			ctx, cancel := context.WithCancel(context.Background())

			// Start consumers
			for i := 0; i < sc.consumers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-ctx.Done():
							return
						default:
						}

						if sc.batchSize > 1 {
							batch, _ := q.DequeueBatch(sc.batchSize)
							if len(batch) > 0 {
								dequeued.Add(int64(len(batch)))
							} else {
								runtime.Gosched()
							}
						} else {
							job, _ := q.Dequeue()
							if job != nil {
								dequeued.Add(1)
							} else {
								runtime.Gosched()
							}
						}
					}
				}()
			}

			b.ResetTimer()

			// Run producers
			var producerWg sync.WaitGroup
			jobsPerProducer := b.N / sc.producers
			if jobsPerProducer < 1 {
				jobsPerProducer = 1
			}

			for i := 0; i < sc.producers; i++ {
				producerWg.Add(1)
				go func(pid int) {
					defer producerWg.Done()
					base := uint64(pid * jobsPerProducer)
					for j := 0; j < jobsPerProducer; j++ {
						job := createTestJob(base + uint64(j))
						for {
							if err := q.Enqueue(job); err == nil {
								enqueued.Add(1)
								break
							}
							runtime.Gosched() // Backoff on full queue
						}
					}
				}(i)
			}

			producerWg.Wait()

			// Wait for consumers to drain
			deadline := time.Now().Add(5 * time.Second)
			for dequeued.Load() < enqueued.Load() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}

			b.StopTimer()
			cancel()
			wg.Wait()

			if dequeued.Load() < enqueued.Load() {
				b.Logf("Warning: not all jobs dequeued: enqueued=%d, dequeued=%d",
					enqueued.Load(), dequeued.Load())
			}
		})
	}
}

// BenchmarkHybridQueueHighContention specifically tests high contention scenarios
func BenchmarkHybridQueueHighContention(b *testing.B) {
	numCPU := runtime.GOMAXPROCS(0)

	b.Run("AllCores_Producing", func(b *testing.B) {
		cfg := DefaultHybridQueueConfig()
		cfg.RingCapacity = 16384
		cfg.OverflowCapacity = 4096
		q, _ := NewHybridQueue(cfg)
		defer q.Close()

		// Start a single consumer
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					q.Dequeue()
				}
			}
		}()

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			id := uint64(0)
			for pb.Next() {
				job := createTestJob(id)
				for q.Enqueue(job) != nil {
					runtime.Gosched()
				}
				id++
			}
		})
		b.StopTimer()
		cancel()
	})

	b.Run("AllCores_Consuming", func(b *testing.B) {
		cfg := DefaultHybridQueueConfig()
		cfg.RingCapacity = 16384
		cfg.OverflowCapacity = 4096
		q, _ := NewHybridQueue(cfg)
		defer q.Close()

		// Pre-fill queue
		for i := 0; i < 10000; i++ {
			q.Enqueue(createTestJob(uint64(i)))
		}

		// Start a single producer to keep queue filled
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			id := uint64(10000)
			for {
				select {
				case <-ctx.Done():
					return
				default:
					q.Enqueue(createTestJob(id))
					id++
				}
			}
		}()

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				for {
					job, _ := q.Dequeue()
					if job != nil {
						break
					}
					runtime.Gosched()
				}
			}
		})
		b.StopTimer()
		cancel()
	})

	_ = numCPU // suppress unused variable warning
}

// BenchmarkHybridQueueSignalCoalescing tests the signal coalescing under burst traffic
func BenchmarkHybridQueueSignalCoalescing(b *testing.B) {
	b.Run("BurstEnqueue", func(b *testing.B) {
		cfg := DefaultHybridQueueConfig()
		cfg.RingCapacity = 65536
		cfg.OverflowCapacity = 16384
		q, _ := NewHybridQueue(cfg)
		defer q.Close()

		// Consumer that uses signal channel
		ctx, cancel := context.WithCancel(context.Background())
		var consumed atomic.Int64
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-q.Notify():
					q.AckSignal() // Use the new AckSignal method
					for {
						job, _ := q.Dequeue()
						if job == nil {
							break
						}
						consumed.Add(1)
					}
				}
			}
		}()

		b.ResetTimer()

		// Burst enqueue pattern
		for i := 0; i < b.N; i++ {
			job := createTestJob(uint64(i))
			for q.Enqueue(job) != nil {
				runtime.Gosched()
			}
		}

		// Wait for consumer
		deadline := time.Now().Add(5 * time.Second)
		for consumed.Load() < int64(b.N) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}

		b.StopTimer()
		cancel()

		if consumed.Load() < int64(b.N) {
			b.Errorf("Signal loss detected: enqueued=%d, consumed=%d", b.N, consumed.Load())
		}
	})
}

// BenchmarkHybridQueueOverflowPath tests performance when overflow is actively used
func BenchmarkHybridQueueOverflowPath(b *testing.B) {
	b.Run("ForceOverflow", func(b *testing.B) {
		cfg := DefaultHybridQueueConfig()
		cfg.RingCapacity = 64      // Very small ring to force overflow
		cfg.OverflowCapacity = 10000
		q, _ := NewHybridQueue(cfg)
		defer q.Close()

		// Single consumer with backpressure
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					q.Dequeue()
					time.Sleep(10 * time.Microsecond) // Slow consumer
				}
			}
		}()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			job := createTestJob(uint64(i))
			for q.Enqueue(job) != nil {
				runtime.Gosched()
			}
		}
		b.StopTimer()
		cancel()
	})
}

// BenchmarkAdaptiveQueueHighContention tests the adaptive queue backoff improvements
func BenchmarkAdaptiveQueueHighContention(b *testing.B) {
	b.Run("AllCores_CAS_Contention", func(b *testing.B) {
		q, err := NewAdaptiveQueue(65536)
		if err != nil {
			b.Fatal(err)
		}
		defer q.Close()

		// Consumer
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					q.Dequeue()
				}
			}
		}()

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			id := uint64(0)
			for pb.Next() {
				job := createTestJob(id)
				for q.Enqueue(job) != nil {
					runtime.Gosched()
				}
				id++
			}
		})
		b.StopTimer()
		cancel()
	})
}

// BenchmarkQueueLatencyDistribution measures latency distribution
func BenchmarkQueueLatencyDistribution(b *testing.B) {
	if b.N < 1000 {
		b.Skip("Need at least 1000 iterations for meaningful latency measurement")
	}

	cfg := DefaultHybridQueueConfig()
	cfg.RingCapacity = 32768
	cfg.OverflowCapacity = 8192
	q, _ := NewHybridQueue(cfg)
	defer q.Close()

	latencies := make([]time.Duration, 0, b.N)
	var mu sync.Mutex

	// Consumer that measures latency
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				job, _ := q.Dequeue()
				if job != nil && !job.GetEnqueueTime().IsZero() {
					lat := time.Since(job.GetEnqueueTime())
					mu.Lock()
					latencies = append(latencies, lat)
					mu.Unlock()
				}
			}
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		job := createTestJob(uint64(i))
		q.Enqueue(job)
	}

	// Wait for consumer
	deadline := time.Now().Add(5 * time.Second)
	for len(latencies) < b.N && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	b.StopTimer()
	cancel()

	// Report latency stats
	if len(latencies) > 0 {
		var sum time.Duration
		var maxLat time.Duration
		for _, l := range latencies {
			sum += l
			if l > maxLat {
				maxLat = l
			}
		}
		avg := sum / time.Duration(len(latencies))
		b.ReportMetric(float64(avg.Nanoseconds()), "avg_latency_ns")
		b.ReportMetric(float64(maxLat.Nanoseconds()), "max_latency_ns")
	}
}
