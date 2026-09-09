package queue

import (
	"testing"
	"time"
)

func TestHistoryRingBuffer(t *testing.T) {
	h := NewHistory[int](3)

	if h.Len() != 0 {
		t.Fatalf("expected 0 samples, got %d", h.Len())
	}
	if got := h.Snapshot(); len(got) != 0 {
		t.Fatalf("expected empty snapshot, got %d", len(got))
	}

	t0 := time.Unix(100, 0)
	h.RecordAt(t0, 1)
	h.RecordAt(t0.Add(time.Second), 2)
	h.RecordAt(t0.Add(2*time.Second), 3)
	if h.Len() != 3 {
		t.Fatalf("expected 3 samples, got %d", h.Len())
	}
	snap := h.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(snap))
	}
	for i, want := range []int{1, 2, 3} {
		if snap[i].Value != want {
			t.Fatalf("sample %d: expected %d, got %d", i, want, snap[i].Value)
		}
	}

	// Wrapping past capacity drops the oldest sample.
	h.RecordAt(t0.Add(3*time.Second), 4)
	snap = h.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 samples after wrap, got %d", len(snap))
	}
	for i, want := range []int{2, 3, 4} {
		if snap[i].Value != want {
			t.Fatalf("sample %d: expected %d, got %d", i, want, snap[i].Value)
		}
	}
}
