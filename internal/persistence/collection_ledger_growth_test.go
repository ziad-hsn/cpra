package persistence

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the Windows mapping boundary with the production opener. A frozen
// reader can delay remapping, but exporting and releasing it must not depend on
// the writer's ledger mutex or on a later transaction.
func TestCollectionLedgerGrowthDuringFrozenSnapshot(t *testing.T) {
	if os.Getenv("CPRA_COLLECTION_GROWTH_CHILD") == "1" {
		collectionLedgerGrowthDuringFrozenSnapshot(t)
		return
	}
	// A lock regression must fail this fixture rather than hang the whole
	// suite in Close. Keep child temporary files under the parent's cleanup.
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCollectionLedgerGrowthDuringFrozenSnapshot$", "-test.count=1", "-test.v", "-test.timeout=60s")
	directory := t.TempDir()
	cmd.Env = append(os.Environ(), "CPRA_COLLECTION_GROWTH_CHILD=1", "TMPDIR="+directory, "TMP="+directory, "TEMP="+directory)
	output, err := cmd.CombinedOutput()
	t.Log(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("growth fixture process failed: %v (deadline: %v)", err, ctx.Err())
	}
}

func collectionLedgerGrowthDuringFrozenSnapshot(t *testing.T) {
	const frozenRows, liveRows = 16, 48
	l := newLedgerTest(t, true, 256<<20)
	op := ledgerTestOperation(1)
	appendItem := func(ordinal uint64) error {
		item := ledgerTestItem(ordinal)
		item.Payload.Ciphertext = bytes.Repeat([]byte{byte(ordinal)}, 1<<20)
		return l.Append(op, item)
	}
	for ordinal := uint64(1); ordinal <= frozenRows; ordinal++ {
		if err := appendItem(ordinal); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(l.directory, l.generation, "ledger.db")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && before.Size() != 64<<20 {
		t.Fatalf("fixture must start below the Windows mapping boundary: %d", before.Size())
	}
	view, err := l.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	snapshot, err := os.CreateTemp(t.TempDir(), "collection-snapshot-")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var attempted, committed atomic.Uint64
	done := make(chan error, 1)
	go func() {
		for ordinal := uint64(frozenRows + 1); ordinal <= liveRows; ordinal++ {
			attempted.Store(ordinal)
			if err := appendItem(ordinal); err != nil {
				done <- err
				return
			}
			committed.Store(ordinal)
		}
		done <- nil
	}()
	if runtime.GOOS == "windows" {
		// The pinned bbolt version exposes no remap notification. Observe its
		// blocked writer stack so a delayed goroutine cannot satisfy this test
		// merely by outlasting a sleep without reaching the mapping boundary.
		deadline := time.Now().Add(10 * time.Second)
		for !collectionLedgerWriterWaitingForMap() {
			select {
			case err := <-done:
				t.Fatalf("writer left before the remap boundary: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("writer did not reach the pinned mapping boundary")
			}
			time.Sleep(5 * time.Millisecond)
		}
		if committed.Load() <= frozenRows || attempted.Load() != committed.Load()+1 {
			t.Fatal("writer did not commit and then wait while the reader was pinned")
		}
	}
	// Export while writes are active. The frozen stream excludes every row
	// admitted after Freeze, whether a writer is copying data or awaiting mmap.
	if _, err := view.WriteTo(snapshot); err != nil {
		t.Fatal(err)
	}
	var completed bool
	select {
	case err := <-done:
		completed = true
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" {
			t.Fatal("writer unexpectedly crossed the mapping boundary with a frozen reader")
		}
	default:
	}
	t.Logf("before release: attempted=%d committed=%d completed=%t file_bytes=%d", attempted.Load(), committed.Load(), completed, before.Size())
	if _, found, err := view.Item(op, frozenRows+1); err != nil || found {
		t.Fatalf("snapshot observed a later row: found=%t err=%v", found, err)
	}
	released := time.Now()
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if !completed {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(20 * time.Second):
			// The test command's process deadline is the final guard if a
			// broken implementation also prevents deferred ledger cleanup.
			t.Fatal("writer did not resume after snapshot release")
		}
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() <= 64<<20 {
		t.Fatalf("fixture did not grow beyond the initial Windows mapping: %d", after.Size())
	}
	t.Logf("after release: committed=%d file_bytes=%d writer_resume=%s", committed.Load(), after.Size(), time.Since(released))
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	restored := newLedgerTest(t, true, 256<<20)
	if err := importCollectionLedger(snapshot, restored); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		ledger *collectionLedger
		count  int
	}{{"frozen", restored, frozenRows}, {"live", l, liveRows}} {
		t.Run(tc.name, func(t *testing.T) {
			// Pages have a byte ceiling; inspect one row at a time.
			for ordinal := uint64(1); ordinal <= uint64(tc.count); ordinal++ {
				item, found, err := tc.ledger.Item(op, ordinal)
				if err != nil || !found || len(item.Payload.Ciphertext) != 1<<20 || !bytes.Equal(item.Payload.Ciphertext, bytes.Repeat([]byte{byte(ordinal)}, 1<<20)) {
					t.Fatalf("row %d differs after growth: found=%t err=%v", ordinal, found, err)
				}
			}
			if _, found, err := tc.ledger.Item(op, uint64(tc.count+1)); err != nil || found {
				t.Fatalf("unexpected row after %d: found=%t err=%v", tc.count, found, err)
			}
		})
	}
}

func collectionLedgerWriterWaitingForMap() bool {
	stack := make([]byte, 64<<10)
	for {
		n := runtime.Stack(stack, true)
		if n < len(stack) {
			for _, goroutine := range bytes.Split(stack[:n], []byte("\n\n")) {
				if bytes.Contains(goroutine, []byte("go.etcd.io/bbolt.(*DB).mmap(")) &&
					bytes.Contains(goroutine, []byte("sync.(*RWMutex).Lock(")) &&
					bytes.Contains(goroutine, []byte("(*collectionLedger).AppendBatch(")) {
					return true
				}
			}
			return false
		}
		if len(stack) >= 1<<20 {
			return false
		}
		stack = make([]byte, len(stack)*2)
	}
}
