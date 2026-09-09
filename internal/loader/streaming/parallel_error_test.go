package streaming

import (
	"context"
	"cpra/internal/loader/schema"
	"errors"
	"github.com/mlange-42/ark/ecs"
	"testing"
	"time"
)

func TestParallelReturnsWorkerErrorWithoutMoreInput(t *testing.T) {
	w := ecs.NewWorld()
	creator := NewParallelEntityCreator(&w, EntityCreationConfig{MaxWorkers: 1})
	batches := make(chan MonitorBatch, 1)
	batches <- MonitorBatch{Monitors: []schema.Monitor{{Name: "bad-config"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := creator.ProcessBatches(ctx, batches, nil)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("worker creation error was not surfaced until input or cancellation: %v", err)
	}
}
