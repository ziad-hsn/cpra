package cpra_test

import (
	"context"
	"testing"
	"time"
)

func TestOperationsWaitInterruptedReturnsOriginalTLS(t *testing.T) {
	client, original, requests, mutations := operationWaitTLSFixture(t, "interrupted")
	// Interruption is the original attempt's retained terminal disposition.
	// Waiting must not acquire a new attempt or follow its long retry interval.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := client.Operations.Wait(ctx, original.ID)
	if err != nil {
		t.Fatal("interrupted operation waited instead of returning", err)
	}
	assertOriginalWaitResponse(t, response, original)
	if requests.Load() != 1 || mutations.Load() != 0 {
		t.Fatal("interruption reconciliation polled or mutated", requests.Load(), mutations.Load())
	}
}
