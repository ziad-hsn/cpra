//go:build externaljobs

package worker

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func queueDeliveryOutcome(t *testing.T, r *Runner, id string) {
	t.Helper()
	a := assignment(id, "notification")
	if err := r.journal.reserve(a); err != nil {
		t.Fatal(err)
	}
	if err := r.journal.update(id, func(rec *journalRecord) error {
		rec.State, rec.GrantID = "started", "grant-"+id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.saveOutcome(id, api.Outcome{Status: "delivered", Diagnostic: "original-result-" + id}, func(err error) { t.Fatal(err) })
}

func waitDeliveryStop(t *testing.T, done <-chan struct{}) bool {
	t.Helper()
	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		t.Error("delivery calls were not joined")
		return false
	}
}

func TestDeliveryBatchRotatesRetainedRecords(t *testing.T) {
	records := []journalRecord{
		{ExecutionID: "m", State: "unknown"}, {ExecutionID: "h", State: "outcome"},
		{ExecutionID: "a", State: "unknown"}, {ExecutionID: "k", State: "unknown"},
		{ExecutionID: "b", State: "outcome"}, {ExecutionID: "c", State: "unknown"},
		{ExecutionID: "i", State: "unknown"}, {ExecutionID: "e", State: "unknown"},
		{ExecutionID: "g", State: "unknown"},
	}
	cursor := ""
	for _, step := range []struct {
		name, remove, cursor string
		want                 []string
	}{
		{"first wave", "", "g", []string{"b", "h", "a", "c", "e", "g"}},
		{"complete rotation and wrap", "", "a", []string{"b", "h", "i", "k", "m", "a"}},
		{"continue after wrap", "", "i", []string{"b", "h", "c", "e", "g", "i"}},
		{"removed cursor record", "i", "c", []string{"b", "h", "k", "m", "a", "c"}},
	} {
		t.Run(step.name, func(t *testing.T) {
			if step.remove != "" {
				kept := records[:0]
				for _, rec := range records {
					if rec.ExecutionID != step.remove {
						kept = append(kept, rec)
					}
				}
				records = kept
			}
			var batch []journalRecord
			batch, cursor = deliveryBatch(records, cursor, 4)
			got := make([]string, 0, len(batch))
			for _, rec := range batch {
				got = append(got, rec.ExecutionID)
			}
			if !reflect.DeepEqual(got, step.want) || cursor != step.cursor {
				t.Fatalf("batch=%v cursor=%q; want %v cursor=%q", got, cursor, step.want, step.cursor)
			}
		})
	}
}

func TestDeliveryPrioritizesOriginalDeadlinesWithoutDiscardingExpiredOutcomes(t *testing.T) {
	c := testConfig(t)
	c.Limits.Concurrency = 1
	p := &protocolFuncs{fakeProtocol: newFake()}
	c.Client = p
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	now := time.Now()
	for _, entry := range []struct {
		id       string
		deadline time.Time
	}{
		{"a-latest", now.Add(time.Minute)},
		{"b-middle", now.Add(30 * time.Second)},
		{"z-expired", now.Add(-time.Minute)},
		{"held", now.Add(-time.Hour)},
	} {
		queueDeliveryOutcome(t, r, entry.id)
		if err := r.journal.update(entry.id, func(rec *journalRecord) error {
			rec.Deadline = entry.deadline
			if entry.id == "held" {
				rec.State, rec.ReceiptID = "unknown", "receipt-held"
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var called []string
	p.result = func(ctx context.Context, out api.Outcome) (*api.Receipt, error) {
		called = append(called, out.ExecutionID)
		return p.fakeProtocol.Result(ctx, out)
	}
	p.start = func(_ context.Context, request api.StartRequest) (*api.StartResponse, error) {
		called = append(called, request.ExecutionID)
		return startReply(request, api.StartDispositionTerminal), nil
	}
	records, err := r.journal.records()
	if err != nil {
		t.Fatal(err)
	}
	if !r.deliverRecords(t.Context(), records, func(err error) { t.Errorf("delivery: %v", err) }) {
		t.Fatal("delivery stopped unexpectedly")
	}
	want := []string{"z-expired", "b-middle", "a-latest", "held"}
	if !reflect.DeepEqual(called, want) {
		t.Fatalf("delivery order=%v; want %v", called, want)
	}
	remaining, err := r.journal.records()
	if err != nil || len(remaining) != 1 || remaining[0].ExecutionID != "held" || remaining[0].State != "unknown" {
		t.Fatal("outcome receipt or retained-action ownership changed", err)
	}
}

func TestFlushReconsidersNewOutcomeBeforeRetainedInventory(t *testing.T) {
	c := testConfig(t)
	p := &protocolFuncs{fakeProtocol: newFake()}
	c.Client = p
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	const count = 12
	for n := range count {
		id := fmt.Sprintf("held-%02d", n)
		queueDeliveryOutcome(t, r, id)
		if err := r.journal.update(id, func(rec *journalRecord) error {
			rec.State, rec.ReceiptID = "unknown", "receipt-"+id
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done, release := make(chan struct{}), make(chan struct{})
	entered, delivered := make(chan string, count), make(chan struct{}, 1)
	p.start = func(ctx context.Context, request api.StartRequest) (*api.StartResponse, error) {
		entered <- request.ExecutionID
		if request.ExecutionID < "held-04" {
			select {
			case <-release:
				return startReply(request, api.StartDispositionTerminal), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		<-ctx.Done() // Later retained records must not hold the new result.
		return nil, ctx.Err()
	}
	p.result = func(ctx context.Context, out api.Outcome) (*api.Receipt, error) {
		delivered <- struct{}{}
		return p.fakeProtocol.Result(ctx, out)
	}
	go func() { defer close(done); r.flush(ctx, func(err error) { t.Errorf("flush: %v", err); cancel() }) }()
	t.Cleanup(func() {
		cancel()
		if waitDeliveryStop(t, done) {
			_ = r.Close()
		}
	})
	for range 4 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("initial reconciliation did not start")
		}
	}
	queueDeliveryOutcome(t, r, "new-outcome")
	close(release)
	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("new outcome waited for the whole retained inventory")
	}
	eventually(t, func() bool { _, found, err := r.journal.record("new-outcome"); return err == nil && !found })
	cancel()
	if !waitDeliveryStop(t, done) {
		return
	}
	for n := range count {
		rec, found, err := r.journal.record(fmt.Sprintf("held-%02d", n))
		if err != nil || !found || rec.State != "unknown" || rec.ReceiptID != "receipt-"+rec.ExecutionID {
			t.Fatal("bounded reconciliation discarded a held action", err)
		}
	}
}

func TestFlushFatalReceiptCancelsSiblingsBeforeJournalRelease(t *testing.T) {
	c := testConfig(t)
	p := &protocolFuncs{fakeProtocol: newFake()}
	c.Client = p
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	queueDeliveryOutcome(t, r, "blocked")
	queueDeliveryOutcome(t, r, "invalid")
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p.result = func(ctx context.Context, out api.Outcome) (*api.Receipt, error) {
		if out.ExecutionID == "blocked" {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release // Deliberately uncooperative after observing cancellation.
			return nil, ctx.Err()
		}
		<-entered
		return &api.Receipt{ServerID: "wrong-server", WorkerUID: c.WorkerUID, ExecutionID: out.ExecutionID,
			ReceiptID: "invalid-receipt", Disposition: api.ReceiptDispositionAccepted}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	var releaseOnce sync.Once
	go func() { defer close(done); runErr = r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		releaseOnce.Do(func() { close(release) })
		if waitDeliveryStop(t, done) {
			_ = r.Close()
		}
	})
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("identity failure did not cancel the other delivery call")
	}
	eventually(t, func() bool {
		status, err := r.Status()
		return err == nil && status.Draining && !status.AdmissionAvailable
	})
	if !errors.Is(r.Close(), ErrRunning) {
		t.Fatal("journal closed while a protocol call remained active")
	}
	if second, err := New(c); err == nil {
		_ = second.Close()
		t.Fatal("a second runner acquired the active journal")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
		if !errors.Is(runErr, ErrIdentity) {
			t.Fatal("sibling cancellation replaced the primary identity error", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not join the released delivery call")
	}
	restored, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for _, id := range []string{"blocked", "invalid"} {
		rec, found, err := restored.journal.record(id)
		if err != nil || !found || rec.State != "outcome" || rec.Outcome.Status != "delivered" {
			t.Fatal("unconfirmed receipt discarded or changed the original outcome", err)
		}
	}
}

func TestFlushConcurrentLostReceiptPreservesOriginalOutcomes(t *testing.T) {
	c := testConfig(t)
	f := c.Client.(*fakeProtocol)
	f.loseFirstReceipt = true
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	for n := range 8 {
		queueDeliveryOutcome(t, r, fmt.Sprintf("result-%d", n))
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.flush(ctx, func(err error) { t.Errorf("flush: %v", err); cancel() }) }()
	t.Cleanup(func() {
		cancel()
		if waitDeliveryStop(t, done) {
			_ = r.Close()
		}
	})
	eventually(t, func() bool { status, err := r.Status(); return err == nil && status.Records == 0 })
	cancel()
	if !waitDeliveryStop(t, done) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := make(map[string]api.Outcome)
	for _, out := range f.results {
		if original, exists := seen[out.ExecutionID]; exists && !reflect.DeepEqual(original, out) {
			t.Fatal("lost receipt changed the persisted result envelope")
		}
		seen[out.ExecutionID] = out
	}
	if len(seen) != 8 || len(f.results) != 9 || len(f.receipts) != 8 || f.starts != 0 {
		t.Fatal("result retry changed execution ownership or duplicated receipts", len(seen), len(f.results), len(f.receipts), f.starts)
	}
}

func TestFlushBlockedResultPreservesProgressAndRestartEnvelope(t *testing.T) {
	c := testConfig(t)
	p := &protocolFuncs{fakeProtocol: newFake()}
	c.Client = p
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	queueDeliveryOutcome(t, r, "blocked")
	for n := range 5 {
		queueDeliveryOutcome(t, r, fmt.Sprintf("fast-%d", n))
	}
	original, _, _ := r.journal.record("blocked")
	var blocked api.Outcome
	p.result = func(ctx context.Context, out api.Outcome) (*api.Receipt, error) {
		if out.ExecutionID == "blocked" {
			blocked = out
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return p.fakeProtocol.Result(ctx, out)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.flush(ctx, func(err error) { t.Errorf("flush: %v", err); cancel() }) }()
	t.Cleanup(func() {
		cancel()
		if waitDeliveryStop(t, done) {
			_ = r.Close()
		}
	})
	eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.receipts) == 5
	})
	cancel()
	if !waitDeliveryStop(t, done) {
		return
	}
	if !reflect.DeepEqual(original.Outcome, &blocked) {
		t.Fatal("delivery changed the original journal envelope")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	p.result = nil
	restored, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	retained, found, err := restored.journal.record("blocked")
	if err != nil || !found || !reflect.DeepEqual(retained, original) {
		t.Fatal("cancellation or restart changed the pending result", err)
	}
	if err := restored.deliverInactive(t.Context(), "blocked"); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.receipts) != 6 || p.starts != 0 || !reflect.DeepEqual(p.results[len(p.results)-1], blocked) {
		t.Fatal("restart replayed execution or changed its outcome envelope")
	}
}

func TestFlushBoundsConcurrentReceiptDelivery(t *testing.T) {
	for _, concurrency := range []int{16, 2, 1} {
		t.Run(fmt.Sprint(concurrency), func(t *testing.T) {
			c := testConfig(t)
			c.Limits.Concurrency = concurrency
			p := &protocolFuncs{fakeProtocol: newFake()}
			c.Client = p
			r, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			const count = 6
			for n := range count {
				queueDeliveryOutcome(t, r, fmt.Sprintf("result-%d", n))
			}
			ctx, cancel := context.WithCancel(t.Context())
			done, release := make(chan struct{}), make(chan struct{})
			entered := make(chan string, count)
			var mu sync.Mutex
			active, peak := 0, 0
			calls := make(map[string]int)
			p.result = func(ctx context.Context, out api.Outcome) (*api.Receipt, error) {
				mu.Lock()
				active++
				peak = max(peak, active)
				calls[out.ExecutionID]++
				mu.Unlock()
				defer func() { mu.Lock(); active--; mu.Unlock() }()
				entered <- out.ExecutionID
				select {
				case <-release:
					return p.fakeProtocol.Result(ctx, out)
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			go func() {
				defer close(done)
				r.flush(ctx, func(err error) { t.Errorf("flush failed: %v", err); cancel() })
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("delivery calls were not joined")
					return
				}
				if err := r.Close(); err != nil {
					t.Error(err)
				}
			})
			want := min(4, concurrency)
			for range want {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("a blocked receipt prevented independent outcome delivery")
				}
			}
			status, err := r.Status()
			if err != nil || status.PendingOutcomes != count {
				t.Fatal("unacknowledged outcomes were discarded", status, err)
			}
			close(release)
			eventually(t, func() bool { status, err := r.Status(); return err == nil && status.Records == 0 })
			cancel()
			if !waitDeliveryStop(t, done) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if active != 0 || peak != want || len(calls) != count {
				t.Fatal("delivery concurrency or ownership changed", active, peak, calls)
			}
			for id, count := range calls {
				if count != 1 {
					t.Fatal("one outcome had concurrent or duplicate delivery", id, count)
				}
			}
		})
	}
}
