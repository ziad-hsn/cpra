package streaming

import (
	"context"
	"github.com/mlange-42/ark/ecs"
	"testing"
	"time"
)

func TestParallelCompletion(t *testing.T) {
	w := ecs.NewWorld()
	c := NewParallelEntityCreator(&w, EntityCreationConfig{MaxWorkers: 1})
	batches := make(chan MonitorBatch)
	close(batches)
	if err := c.ProcessBatches(context.Background(), batches, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
}
