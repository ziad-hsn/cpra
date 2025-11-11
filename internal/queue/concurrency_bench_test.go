package queue

import (
	"cpra/internal/jobs"
	"sync"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// BenchmarkBatchCollector benchmarks the sync.Cond-based batch collector.
func BenchmarkBatchCollector(b *testing.B) {
	const batchSize = 512
	const timeout = 10 * time.Millisecond

	b.Run("Add", func(b *testing.B) {
		collector := newBatchCollector(batchSize, timeout)
		defer collector.Close()

		// Start a consumer that drains batches
		done := make(chan struct{})
		go func() {
			for {
				batch := collector.Wait()
				if batch == nil {
					close(done)
					return
				}
			}
		}()

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				collector.Add(jobs.Result{Ent: ecs.Entity{}})
			}
		})

		collector.Close()
		<-done
	})

	b.Run("FullBatchCycle", func(b *testing.B) {
		// Measure full cycle: fill batch, signal, drain
		for i := 0; i < b.N; i++ {
			collector := newBatchCollector(batchSize, timeout)

			// Fill a batch
			for j := 0; j < batchSize; j++ {
				collector.Add(jobs.Result{Ent: ecs.Entity{}})
			}

			// Consume it
			batch := collector.Wait()
			if len(batch) != batchSize {
				b.Fatalf("expected batch size %d, got %d", batchSize, len(batch))
			}

			collector.Close()
		}
	})
}

// BenchmarkOptimalResultChannelDepth benchmarks the buffer sizing function.
func BenchmarkOptimalResultChannelDepth(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = optimalResultChannelDepth(8192, 5, 512)
	}
}

// BenchmarkTimerVsTicker compares time.AfterFunc vs time.Ticker for batch timeouts.
func BenchmarkTimerVsTicker(b *testing.B) {
	const timeout = 10 * time.Millisecond

	b.Run("AfterFunc", func(b *testing.B) {
		// Simulates the new pattern: timer only when needed
		for i := 0; i < b.N; i++ {
			timer := time.AfterFunc(timeout, func() {})
			timer.Stop()
		}
	})

	b.Run("Ticker", func(b *testing.B) {
		// Simulates the old pattern: continuous ticker
		for i := 0; i < b.N; i++ {
			ticker := time.NewTicker(timeout)
			ticker.Stop()
		}
	})
}

// BenchmarkSyncOnceVsLoadOrStore compares sync.Once vs sync.Map for initialization.
func BenchmarkSyncOnceVsLoadOrStore(b *testing.B) {
	b.Run("SyncOnce", func(b *testing.B) {
		var once sync.Once
		var value int

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				once.Do(func() {
					value = 42
				})
				_ = value
			}
		})
	})

	b.Run("SyncMapLoadOrStore", func(b *testing.B) {
		var m sync.Map

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				actual, _ := m.LoadOrStore("key", 42)
				_ = actual.(int)
			}
		})
	})
}

// BenchmarkChannelSend benchmarks channel operations with different buffer sizes.
func BenchmarkChannelSend(b *testing.B) {
	sizes := []int{64, 256, 512, 1024, 2048}

	for _, size := range sizes {
		b.Run(formatSize(size), func(b *testing.B) {
			ch := make(chan int, size)

			// Start consumer
			done := make(chan struct{})
			go func() {
				for range ch {
				}
				close(done)
			}()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ch <- i
			}
			close(ch)
			<-done
		})
	}
}

func formatSize(n int) string {
	if n >= 1024 {
		return string(rune('0'+n/1024)) + "K"
	}
	return string(rune('0' + n/100))
}

