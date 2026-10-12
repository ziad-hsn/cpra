package queue

import (
	"io"
	"log"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
)

func resultProcessorFixture(t *testing.T, size int) (*DynamicWorkerPool, func()) {
	t.Helper()
	q, err := NewHybridQueue(HybridQueueConfig{RingCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultWorkerPoolConfig()
	cfg.MinWorkers, cfg.MaxWorkers, cfg.NumShards = 1, 1, 1
	cfg.ResultBatchSize, cfg.ResultBatchTimeout = size, 10*time.Millisecond
	pool, err := NewDynamicWorkerPool(q, cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	pool.wg.Add(1)
	go pool.resultProcessor(0)
	return pool, func() {
		pool.cancel()
		pool.wg.Wait()
		pool.DrainAndStop()
		q.Close()
	}
}

func publishProcessorResult(t *testing.T, pool *DynamicWorkerPool, generation uint64) {
	t.Helper()
	if !pool.resultQueues[0].TryEnqueue(jobs.Result{Type: "pulse", Generation: generation}) {
		t.Fatal("fixture result queue full")
	}
	pool.pendingResults.Add(1)
	pool.notify(0)
	synctest.Wait()
}

func TestResultPartialBatchDeadlineDoesNotFollowLaterResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, close := resultProcessorFixture(t, 8)
		defer close()
		publishProcessorResult(t, pool, 1)
		time.Sleep(9 * time.Millisecond)
		publishProcessorResult(t, pool, 2)
		time.Sleep(time.Millisecond)
		synctest.Wait()
		select {
		case batch := <-pool.GetRouter().PulseResultChan:
			if len(batch) != 2 || batch[0].Generation != 1 || batch[1].Generation != 2 {
				t.Fatal("partial deadline lost result ordering", batch)
			}
		default:
			t.Fatal("later result postponed the first result's flush deadline")
		}
	})
}

func TestResultFullBatchAndNewDeadlineAfterIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, close := resultProcessorFixture(t, 2)
		defer close()
		publishProcessorResult(t, pool, 1)
		publishProcessorResult(t, pool, 2)
		select {
		case batch := <-pool.GetRouter().PulseResultChan:
			if len(batch) != 2 {
				t.Fatal("full batch did not flush immediately")
			}
		default:
			t.Fatal("full batch waited for a timer")
		}
		time.Sleep(27 * time.Millisecond)
		publishProcessorResult(t, pool, 3)
		time.Sleep(9 * time.Millisecond)
		synctest.Wait()
		select {
		case <-pool.GetRouter().PulseResultChan:
			t.Fatal("an old idle timer flushed a new batch early")
		default:
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		select {
		case batch := <-pool.GetRouter().PulseResultChan:
			if len(batch) != 1 || batch[0].Generation != 3 {
				t.Fatal("new partial batch was lost")
			}
		default:
			t.Fatal("new partial batch missed its deadline")
		}
	})
}

func TestResultCancellationFlushesPendingBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, close := resultProcessorFixture(t, 8)
		publishProcessorResult(t, pool, 1)
		close()
		batch := <-pool.GetRouter().PulseResultChan
		if len(batch) != 1 || batch[0].Generation != 1 || pool.pendingResults.Load() != 0 || pool.tasksCompleted.Load() != 1 {
			t.Fatal("shutdown discarded a pending result", batch)
		}
	})
}
