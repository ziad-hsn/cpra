package queue_benchmark

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	cprajobs "cpra/internal/jobs"
	cpraqueue "cpra/internal/queue"
	"github.com/Workiva/go-datastructures/queue"
)

// Test job implementation
type testJob struct {
	id   int
	data []byte
}

func (t *testJob) Execute() cprajobs.Result  { return cprajobs.Result{} }
func (t *testJob) Copy() cprajobs.Job        { return &testJob{id: t.id, data: append([]byte(nil), t.data...)} }
func (t *testJob) GetEnqueueTime() time.Time { return time.Time{} }
func (t *testJob) SetEnqueueTime(time.Time)  {}
func (t *testJob) GetStartTime() time.Time   { return time.Time{} }
func (t *testJob) SetStartTime(time.Time)    {}

// Benchmark configurations
const (
	SmallBatchSize   = 10
	MediumBatchSize  = 100
	LargeBatchSize   = 1000
	NumOperations    = 100000
	QueueCapacity    = 1 << 16 // 65536 - power of 2 for AdaptiveQueue
)

// Helper to create test jobs
func createTestJobs(count int) []cprajobs.Job {
	jobs := make([]cprajobs.Job, count)
	for i := 0; i < count; i++ {
		jobs[i] = &testJob{
			id:   i,
			data: make([]byte, 256), // 256 bytes per job
		}
	}
	return jobs
}

func createTestInterfaces(count int) []interface{} {
	items := make([]interface{}, count)
	for i := 0; i < count; i++ {
		items[i] = &testJob{
			id:   i,
			data: make([]byte, 256),
		}
	}
	return items
}

// Convert jobs.Job slice to interface{} slice for AdaptiveQueue
func jobsToInterfaces(jobs []cprajobs.Job) []interface{} {
	items := make([]interface{}, len(jobs))
	for i, job := range jobs {
		items[i] = job
	}
	return items
}

// ========== SINGLE OPERATIONS ==========

func BenchmarkAdaptiveQueue_SingleEnqueueDequeue(b *testing.B) {
	q, err := cpraqueue.NewAdaptiveQueue(QueueCapacity)
	if err != nil {
		b.Fatalf("failed to create adaptive queue: %v", err)
	}

	job := &testJob{id: 1, data: make([]byte, 256)}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := q.Enqueue(job); err != nil {
			b.Fatalf("enqueue: %v", err)
		}
		if _, err := q.Dequeue(); err != nil {
			b.Fatalf("dequeue: %v", err)
		}
	}
}

func BenchmarkWorkivaQueue_SingleEnqueueDequeue(b *testing.B) {
	q := queue.New(QueueCapacity)
	defer q.Dispose()

	job := &testJob{id: 1, data: make([]byte, 256)}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := q.Put(job); err != nil {
			b.Fatalf("put: %v", err)
		}
		if _, err := q.Get(1); err != nil {
			b.Fatalf("get: %v", err)
		}
	}
}

// ========== BATCH OPERATIONS ==========

func BenchmarkAdaptiveQueue_SmallBatch(b *testing.B) {
	q, err := cpraqueue.NewAdaptiveQueue(QueueCapacity)
	if err != nil {
		b.Fatalf("failed to create adaptive queue: %v", err)
	}

	jobs := createTestJobs(SmallBatchSize)
	interfaces := jobsToInterfaces(jobs)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Enqueue batch
		if err := q.EnqueueBatch(interfaces); err != nil {
			b.Fatalf("enqueue batch: %v", err)
		}

		// Dequeue batch
		if _, err := q.DequeueBatch(SmallBatchSize); err != nil {
			b.Fatalf("dequeue batch: %v", err)
		}
	}
}

func BenchmarkWorkivaQueue_SmallBatch(b *testing.B) {
	q := queue.New(QueueCapacity)
	defer q.Dispose()

	items := createTestInterfaces(SmallBatchSize)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Enqueue batch
		if err := q.Put(items...); err != nil {
			b.Fatalf("put batch: %v", err)
		}

		// Dequeue batch
		if _, err := q.Get(SmallBatchSize); err != nil {
			b.Fatalf("get batch: %v", err)
		}
	}
}

func BenchmarkAdaptiveQueue_MediumBatch(b *testing.B) {
	q, err := cpraqueue.NewAdaptiveQueue(QueueCapacity)
	if err != nil {
		b.Fatalf("failed to create adaptive queue: %v", err)
	}

	jobs := createTestJobs(MediumBatchSize)
	interfaces := jobsToInterfaces(jobs)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := q.EnqueueBatch(interfaces); err != nil {
			b.Fatalf("enqueue batch: %v", err)
		}
		if _, err := q.DequeueBatch(MediumBatchSize); err != nil {
			b.Fatalf("dequeue batch: %v", err)
		}
	}
}

func BenchmarkWorkivaQueue_MediumBatch(b *testing.B) {
	q := queue.New(QueueCapacity)
	defer q.Dispose()

	items := createTestInterfaces(MediumBatchSize)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := q.Put(items...); err != nil {
			b.Fatalf("put batch: %v", err)
		}
		if _, err := q.Get(MediumBatchSize); err != nil {
			b.Fatalf("get batch: %v", err)
		}
	}
}

func BenchmarkAdaptiveQueue_LargeBatch(b *testing.B) {
	q, err := cpraqueue.NewAdaptiveQueue(QueueCapacity)
	if err != nil {
		b.Fatalf("failed to create adaptive queue: %v", err)
	}

	jobs := createTestJobs(LargeBatchSize)
	interfaces := jobsToInterfaces(jobs)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := q.EnqueueBatch(interfaces); err != nil {
			b.Fatalf("enqueue batch: %v", err)
		}
		if _, err := q.DequeueBatch(LargeBatchSize); err != nil {
			b.Fatalf("dequeue batch: %v", err)
		}
	}
}

func BenchmarkWorkivaQueue_LargeBatch(b *testing.B) {
	q := queue.New(QueueCapacity)
	defer q.Dispose()

	items := createTestInterfaces(LargeBatchSize)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := q.Put(items...); err != nil {
			b.Fatalf("put batch: %v", err)
		}
		if _, err := q.Get(LargeBatchSize); err != nil {
			b.Fatalf("get batch: %v", err)
		}
	}
}

// ========== CONCURRENT OPERATIONS ==========

func BenchmarkAdaptiveQueue_ConcurrentProducersConsumers(b *testing.B) {
	q, err := cpraqueue.NewAdaptiveQueue(QueueCapacity)
	if err != nil {
		b.Fatalf("failed to create adaptive queue: %v", err)
	}

	numProducers := runtime.NumCPU()
	numConsumers := runtime.NumCPU()
	operationsPerProducer := b.N / numProducers

	var wg sync.WaitGroup
	wg.Add(numProducers + numConsumers)

	// Start producers
	for i := 0; i < numProducers; i++ {
		go func(id int) {
			defer wg.Done()
			job := &testJob{id: id, data: make([]byte, 256)}
			for j := 0; j < operationsPerProducer; j++ {
				if err := q.Enqueue(job); err != nil {
					b.Errorf("enqueue error: %v", err)
					return
				}
			}
		}(i)
	}

	// Start consumers
	for i := 0; i < numConsumers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < operationsPerProducer; j++ {
				if _, err := q.Dequeue(); err != nil {
					b.Errorf("dequeue error: %v", err)
					return
				}
			}
		}()
	}

	wg.Wait()
}

func BenchmarkWorkivaQueue_ConcurrentProducersConsumers(b *testing.B) {
	q := queue.New(QueueCapacity)
	defer q.Dispose()

	numProducers := runtime.NumCPU()
	numConsumers := runtime.NumCPU()
	operationsPerProducer := b.N / numProducers

	var wg sync.WaitGroup
	wg.Add(numProducers + numConsumers)

	// Start producers
	for i := 0; i < numProducers; i++ {
		go func(id int) {
			defer wg.Done()
			item := &testJob{id: id, data: make([]byte, 256)}
			for j := 0; j < operationsPerProducer; j++ {
				if err := q.Put(item); err != nil {
					b.Errorf("put error: %v", err)
					return
				}
			}
		}(i)
	}

	// Start consumers
	for i := 0; i < numConsumers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < operationsPerProducer; j++ {
				if _, err := q.Get(1); err != nil {
					b.Errorf("get error: %v", err)
					return
				}
			}
		}()
	}

	wg.Wait()
}

// ========== THROUGHPUT TESTS ==========

func BenchmarkAdaptiveQueue_Throughput(b *testing.B) {
	q, err := cpraqueue.NewAdaptiveQueue(QueueCapacity)
	if err != nil {
		b.Fatalf("failed to create adaptive queue: %v", err)
	}

	jobs := createTestJobs(b.N)

	b.ResetTimer()
	b.ReportAllocs()

	// Enqueue all
	for i := 0; i < b.N; i++ {
		if err := q.Enqueue(jobs[i]); err != nil {
			b.Fatalf("enqueue: %v", err)
		}
	}

	// Dequeue all
	for i := 0; i < b.N; i++ {
		if _, err := q.Dequeue(); err != nil {
			b.Fatalf("dequeue: %v", err)
		}
	}
}

func BenchmarkWorkivaQueue_Throughput(b *testing.B) {
	q := queue.New(QueueCapacity)
	defer q.Dispose()

	items := make([]interface{}, b.N)
	for i := 0; i < b.N; i++ {
		items[i] = &testJob{id: i, data: make([]byte, 256)}
	}

	b.ResetTimer()
	b.ReportAllocs()

	// Enqueue all
	if err := q.Put(items...); err != nil {
		b.Fatalf("put: %v", err)
	}

	// Dequeue all
	for i := 0; i < b.N; {
		batch, err := q.Get(1000) // Get in batches
		if err != nil {
			b.Fatalf("get: %v", err)
		}
		i += len(batch)
	}
}

// ========== MEMORY EFFICIENCY TESTS ==========

func BenchmarkAdaptiveQueue_MemoryUsage(b *testing.B) {
	q, err := cpraqueue.NewAdaptiveQueue(QueueCapacity)
	if err != nil {
		b.Fatalf("failed to create adaptive queue: %v", err)
	}

	jobs := createTestJobs(MediumBatchSize)
	interfaces := jobsToInterfaces(jobs)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := q.EnqueueBatch(interfaces); err != nil {
			b.Fatalf("enqueue batch: %v", err)
		}
		if _, err := q.DequeueBatch(MediumBatchSize); err != nil {
			b.Fatalf("dequeue batch: %v", err)
		}
	}
}

func BenchmarkWorkivaQueue_MemoryUsage(b *testing.B) {
	q := queue.New(QueueCapacity)
	defer q.Dispose()

	items := createTestInterfaces(MediumBatchSize)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := q.Put(items...); err != nil {
			b.Fatalf("put batch: %v", err)
		}
		if _, err := q.Get(MediumBatchSize); err != nil {
			b.Fatalf("get: %v", err)
		}
	}
}

// ========== QUEUE STATS TESTS ==========

func BenchmarkAdaptiveQueue_WithStats(b *testing.B) {
	q, err := cpraqueue.NewAdaptiveQueue(QueueCapacity)
	if err != nil {
		b.Fatalf("failed to create adaptive queue: %v", err)
	}

	job := &testJob{id: 1, data: make([]byte, 256)}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := q.Enqueue(job); err != nil {
			b.Fatalf("enqueue: %v", err)
		}
		if _, err := q.Dequeue(); err != nil {
			b.Fatalf("dequeue: %v", err)
		}

		// Get stats every 1000 operations
		if i%1000 == 0 {
			_ = q.Stats()
		}
	}
}

func BenchmarkWorkivaQueue_WithStats(b *testing.B) {
	q := queue.New(QueueCapacity)
	defer q.Dispose()

	job := &testJob{id: 1, data: make([]byte, 256)}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := q.Put(job); err != nil {
			b.Fatalf("put: %v", err)
		}
		if _, err := q.Get(1); err != nil {
			b.Fatalf("get: %v", err)
		}

		// Get length every 1000 operations (equivalent to stats)
		if i%1000 == 0 {
			_ = q.Len()
		}
	}
}

// ========== TEST HELPER ==========

func TestQueueImplementations(b *testing.T) {
	fmt.Println("Running basic functionality tests...")

	// Test AdaptiveQueue
	aq, err := cpraqueue.NewAdaptiveQueue(QueueCapacity)
	if err != nil {
		b.Fatalf("failed to create adaptive queue: %v", err)
	}

	job := &testJob{id: 1, data: []byte("test")}
	if err := aq.Enqueue(job); err != nil {
		b.Fatalf("adaptive enqueue: %v", err)
	}

	retrieved, err := aq.Dequeue()
	if err != nil {
		b.Fatalf("adaptive dequeue: %v", err)
	}

	if retrieved == nil {
		b.Fatal("adaptive queue returned nil job")
	}

	fmt.Println("✓ AdaptiveQueue basic functionality works")

	// Test WorkivaQueue
	wq := queue.New(QueueCapacity)
	defer wq.Dispose()

	if err := wq.Put(job); err != nil {
		b.Fatalf("workiva put: %v", err)
	}

	items, err := wq.Get(1)
	if err != nil {
		b.Fatalf("workiva get: %v", err)
	}

	if len(items) == 0 {
		b.Fatal("workiva queue returned no items")
	}

	fmt.Println("✓ WorkivaQueue basic functionality works")
	fmt.Println("All basic tests passed! Ready for benchmarking.")
}