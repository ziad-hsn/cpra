//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestWorkerExecutionsRetainedPages(t *testing.T) {
	s := openCatalogMemory(t)
	page, err := s.WorkerExecutions(t.Context(), "", 1)
	if err != nil || len(page.Items) != 0 || page.More || page.Next != "" {
		t.Fatal("empty execution state", err)
	}
	s.fsm.mu.Lock()
	s.fsm.image.WorkerExecutions = &workerExecutionImage{Records: make(map[string]WorkerExecutionRecord), EncodedBytes: workerExecutionImageOverhead}
	s.fsm.mu.Unlock()
	page, err = s.WorkerExecutions(t.Context(), "", 1)
	if err != nil || len(page.Items) != 0 || page.More || page.Next != "" {
		t.Fatal("allocated empty execution state", err)
	}
	var originals []WorkerExecutionRecord
	for n := 0; n < 3; n++ {
		in, _ := workerExecutionFixture(t, s, "check")
		in.ID = fmt.Sprintf("execution-%02d", n)
		in.Scheduled = time.Now().Add(-2 * time.Hour).UTC()
		in.Deadline = in.Scheduled.Add(time.Hour)
		in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(s.nodeID), []byte("private-execution-parameters-canary"))
		if err != nil {
			t.Fatal(err)
		}
		result := workerExecutionSubmitAt(t, s, in, s.executorSession, in.Scheduled)
		if result.Err != nil || result.WorkerExecution == nil {
			t.Fatal("historical admission", result.Err)
		}
		originals = append(originals, result.WorkerExecution.Clone())
	}
	// The read traversal has no dispatch eligibility filter.
	submit(t, s, Command{Kind: "local_session", At: time.Now().UTC(), ExecutorSession: "different-owner"})
	after := ""
	for n, want := range originals {
		page, err = s.WorkerExecutions(t.Context(), after, 1)
		if err != nil || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0], want) || page.Next != want.Intent.ID || page.More != (n < len(originals)-1) {
			t.Fatal("retained page changed original", n, err)
		}
		after = page.Next
		page.Items[0].Intent.Payload.Ciphertext[0] ^= 1
		page.Items[0].Intent.Guard.Conditions[0].Revision = "changed"
		got, _, err := s.WorkerExecution(t.Context(), want.Intent.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("page shares mutable state", err)
		}
	}
	page, err = s.WorkerExecutions(t.Context(), after, 1)
	if err != nil || len(page.Items) != 0 || page.More {
		t.Fatal("end of traversal", err)
	}
}

func TestWorkerExecutionsPageByteBound(t *testing.T) {
	s := openCatalogMemory(t)
	for n := 0; n < 26; n++ {
		in, _ := workerExecutionFixture(t, s, "check")
		in.ID = fmt.Sprintf("large-%02d", n)
		var err error
		in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(s.nodeID), bytes.Repeat([]byte{'x'}, MaxWorkerExecutionPayload))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.CommitWorkerExecution(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.WorkerExecutions(t.Context(), "", 256)
	if err != nil || !page.More || len(page.Items) == 0 || len(page.Items) >= 26 {
		t.Fatal("byte ceiling did not split page", len(page.Items), err)
	}
	var size int64
	for _, record := range page.Items {
		cost, err := workerExecutionRecordCost(record.Intent.ID, record)
		if err != nil {
			t.Fatal(err)
		}
		size += cost
	}
	if size > MaxWorkerExecutionPageBytes {
		t.Fatal("page exceeds byte ceiling", size)
	}
	rest, err := s.WorkerExecutions(t.Context(), page.Next, 256)
	if err != nil || rest.More || len(page.Items)+len(rest.Items) != 26 {
		t.Fatal("byte pagination lost records", err)
	}
}

func TestWorkerExecutionsReadBoundsAndCancellation(t *testing.T) {
	s := openCatalogMemory(t)
	for _, limit := range []int{0, -1, 257} {
		if _, err := s.WorkerExecutions(t.Context(), "", limit); !errors.Is(err, ErrWorkerExecutionInvalid) {
			t.Fatal("invalid page limit", err)
		}
	}
	if _, err := s.WorkerExecutions(nil, "", 1); !errors.Is(err, ErrWorkerExecutionInvalid) {
		t.Fatal("nil context", err)
	}
	if _, err := s.WorkerExecutions(t.Context(), "unsafe\n", 1); !errors.Is(err, ErrWorkerExecutionInvalid) {
		t.Fatal("invalid cursor", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.WorkerExecutions(ctx, "", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled traversal", err)
	}
	s.fsm.mu.Lock()
	ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := s.WorkerExecutions(ctx, "", 1)
	s.fsm.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock acquisition ignored deadline", err)
	}
}
