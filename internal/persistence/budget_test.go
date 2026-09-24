package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/slo"
)

func budgetUsed(b *commitBudget) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

func waitBudgetCondition(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for " + description)
		case <-ticker.C:
		}
	}
}

// Admission runs before the owner goroutine starts, letting these tests stop
// precisely after queue ownership transfers without timing sleeps or test-only
// production hooks. Starting it later exercises the real batching/commit path.
func dormantCatalogStore(t *testing.T) (*Store, func()) {
	t.Helper()
	c := runtimeconfig.Default()
	c.Storage.Mode = "memory"
	history, err := openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{config: c, nodeID: "test-store", requests: make(chan submission, 1024),
		stop: make(chan struct{}), done: make(chan struct{}),
		fsm: &machine{image: image{Version: FormatVersion, Monitors: make(map[string]Monitor)}, history: history}}
	var once sync.Once
	start := func() { once.Do(func() { go s.run() }) }
	t.Cleanup(func() {
		start()
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, start
}

func TestCommitBudgetBoundsCancellationAndWakeup(t *testing.T) {
	var b commitBudget
	stop := make(chan struct{})
	for _, size := range []int{-1, 0, maxCommitBytes + 1} {
		if err := b.acquire(context.Background(), stop, size); err == nil {
			t.Fatalf("invalid size %d reserved", size)
		}
	}
	for range maxPendingCommitBytes / maxCommitBytes {
		if err := b.acquire(context.Background(), stop, maxCommitBytes); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.acquire(ctx, stop, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("budget wait ignored cancellation: %v", err)
	}
	if got := budgetUsed(&b); got != maxPendingCommitBytes {
		t.Fatalf("canceled waiter changed reservation count: %d", got)
	}
	ready := make(chan error, 1)
	go func() { ready <- b.acquire(context.Background(), stop, maxCommitBytes) }()
	b.release(maxCommitBytes)
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("released capacity did not wake a waiting submitter")
	}
	close(stop)
	if err := b.acquire(context.Background(), stop, 1); err == nil {
		t.Fatal("stopped store admitted a waiting write")
	}
	b.release(maxPendingCommitBytes)
	if budgetUsed(&b) != 0 {
		t.Fatal("budget remained reserved")
	}
}

func TestEncodedBoundCoversWireTypesAndRejectsOversizedBatch(t *testing.T) {
	s, _ := dormantCatalogStore(t)
	r := catalogRecord(t, s, "Monitor", "one", "uid", "v1", "payload")
	mutation := CatalogMutation{Record: r, Create: true}
	m := testMonitor()
	m.Name = "escaped-\"-\\-<>&-\x00-\u2028-\xff-🙂"
	m.Cooldowns = map[string]time.Time{"\"\x00\xff": time.Now()}
	sloState := slo.State{ByDriver: map[string]*[slo.Slots]slo.Bucket{"http": {}}, QueueTarget: 250 * time.Millisecond, ResultTarget: 5 * time.Second}
	cases := []envelope{
		{Version: CatalogFormatVersion, Commands: []Command{{Kind: "catalog", At: r.UpdatedAt, Catalog: &mutation}}},
		{Version: FormatVersion, Commands: []Command{{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: time.Now()}}},
		{Version: FormatVersion, Commands: []Command{{Kind: "slo", At: time.Now(), SLO: &sloState}}},
		{Version: FormatVersion, Commands: []Command{{Kind: "pulse", At: time.Now(), MonitorID: m.ID, Revision: m.Revision,
			Generation: ^uint64(0), Missed: ^uint64(0), ExecutionStart: time.Now(), ExecutionEnd: time.Now(), Retryable: true}}},
	}
	for _, wire := range cases {
		bound, err := encodedBound(wire)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > bound {
			t.Fatalf("admission bound %d undercounts %d encoded bytes", bound, len(encoded))
		}
	}
	large := catalogRecord(t, s, "Monitor", "large", "uid", "v1", strings.Repeat("x", 1<<20))
	largeMutation := CatalogMutation{Record: large, Create: true}
	commands := make([]Command, 32)
	for i := range commands {
		commands[i] = Command{Kind: "catalog", At: large.UpdatedAt, Catalog: &largeMutation}
	}
	// The caller owns one shared input. Rejecting it must not clone/encode tens
	// of MiB before checking the encoded quota. TotalAlloc includes background
	// runtime activity, so leave a generous allowance above this small walk.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := s.Submit(context.Background(), commands); err == nil {
		t.Fatal("oversized ciphertext batch was admitted")
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
		t.Fatalf("oversized admission allocated %d bytes before rejection", allocated)
	}
	if budgetUsed(&s.budget) != 0 || len(s.requests) != 0 {
		t.Fatal("rejected batch leaked a reservation or reached the owner queue")
	}
}

func TestEncodedBoundSupportsFiniteFractionsAndRejectsNonfiniteNumbers(t *testing.T) {
	for _, value := range []float64{0, -0.000001, 0.125, 1e20, -1e-9, math.SmallestNonzeroFloat64, math.MaxFloat64, -math.MaxFloat64} {
		bound, err := encodedBound(value)
		encoded, jsonErr := json.Marshal(value)
		if err != nil || jsonErr != nil || len(encoded) > bound {
			t.Fatal("finite floating-point encoding was not bounded", value, err, jsonErr)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := encodedBound(value); err == nil {
			t.Fatal("nonfinite JSON value admitted")
		}
	}
}

func TestSLOFractionalPauseExposureCommitsAndRestores(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC().Truncate(slo.SlotDuration)
	s.SLO().ChangePaused("http", 1, at)
	s.SLO().ChangePaused("http", -1, at.Add(1250*time.Millisecond))
	state := s.SLO().Snapshot(at.Add(2 * time.Second))
	result := submit(t, s, Command{Kind: "slo", At: at.Add(2 * time.Second), SLO: &state})[0]
	if result.Err != nil || !result.Allowed {
		t.Fatal("fractional pause was not committed", result.Err)
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	encoded, err := json.Marshal(snapshot.(*frozenSnapshot).image)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := decodeImage(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, bucket := range restored.SLO.ByDriver["http"] {
		total += bucket.PausedSeconds
	}
	if total != 1.25 {
		t.Fatalf("pause exposure changed on persistence: %v", total)
	}
}

func TestSubmitFreezesBeforeCallerStopsWaiting(t *testing.T) {
	s, start := dormantCatalogStore(t)
	r := catalogRecord(t, s, "Monitor", "one", "uid", "v1", "original encrypted configuration")
	mutation := CatalogMutation{Record: r, Create: true}
	command := Command{Kind: "catalog", At: r.UpdatedAt, Catalog: &mutation}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { _, err := s.Submit(ctx, []Command{command}); finished <- err }()
	waitBudgetCondition(t, "private encoded submission", func() bool { return len(s.requests) == 1 })
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCommitUnconfirmed) {
		t.Fatalf("accepted cancellation lost ambiguous outcome: %v", err)
	}
	if budgetUsed(&s.budget) == 0 {
		t.Fatal("caller cancellation released an uncommitted reservation")
	}
	mutation.Record.Payload.Ciphertext[0] ^= 1
	mutation.Record.Key.ID = "changed-after-submission"
	start()
	waitBudgetCondition(t, "accepted commit", func() bool { return budgetUsed(&s.budget) == 0 })
	recovered := requireCatalog(t, s, r.Key)
	plaintext, err := catalogSealer(t).Open(context.Background(), recovered.Binding(s.nodeID), recovered.Payload)
	if err != nil || !bytes.Equal(plaintext, []byte("original encrypted configuration")) {
		t.Fatalf("caller mutation changed the encoded submission: %v", err)
	}
}

func TestSubmitCancellationBeforeAdmissionLeavesNoWork(t *testing.T) {
	s, _ := dormantCatalogStore(t)
	for range maxPendingCommitBytes / maxCommitBytes {
		if err := s.budget.acquire(context.Background(), s.stop, maxCommitBytes); err != nil {
			t.Fatal(err)
		}
	}
	defer s.budget.release(maxPendingCommitBytes)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Submit(ctx, []Command{{Kind: "recover", At: time.Now()}}); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCommitUnconfirmed) {
		t.Fatalf("pre-admission cancellation misreported: %v", err)
	}
	if len(s.requests) != 0 || budgetUsed(&s.budget) != maxPendingCommitBytes {
		t.Fatal("canceled admission changed the queue or reserved bytes")
	}
}

func TestCloseReleasesCommittingPendingAndQueuedBudgets(t *testing.T) {
	s, start := dormantCatalogStore(t)
	results := make(chan error, 4)
	for i := range 4 {
		r := catalogRecord(t, s, "Monitor", fmt.Sprintf("monitor-%d", i), fmt.Sprintf("uid-%d", i), "v1", strings.Repeat("x", 1<<20))
		mutation := CatalogMutation{Record: r, Create: true}
		go func() {
			_, err := s.Submit(context.Background(), []Command{{Kind: "catalog", At: r.UpdatedAt, Catalog: &mutation}})
			results <- err
		}()
	}
	waitBudgetCondition(t, "four encoded requests", func() bool { return len(s.requests) == 4 })
	reserved := budgetUsed(&s.budget)
	s.fsm.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.fsm.mu.Unlock()
		}
	}()
	start()
	// Two one-MiB resources fit one commit; the third becomes pending and the
	// fourth remains in the channel while Apply is blocked by this lock.
	waitBudgetCondition(t, "blocked commit with pending overflow", func() bool { return len(s.requests) == 1 })
	if current := budgetUsed(&s.budget); current != reserved {
		t.Fatalf("commit released memory before Apply completed: %d -> %d", reserved, current)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-s.stop:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop admission")
	}
	select {
	case <-closed:
		t.Fatal("Close returned before the accepted commit completed")
	default:
	}
	s.fsm.mu.Unlock()
	locked = false
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close deadlocked with queued submitters")
	}
	for range 4 {
		select {
		case <-results:
		case <-time.After(5 * time.Second):
			t.Fatal("caller remained blocked after Close")
		}
	}
	if budgetUsed(&s.budget) != 0 || len(s.requests) != 0 {
		t.Fatal("shutdown retained committing, pending, or queued reservations")
	}
	view, err := s.CatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	page, _, err := view.Page("Monitor", "", 100)
	if err != nil || len(page) != 2 {
		t.Fatalf("shutdown applied queued work beyond the accepted commit: count=%d, error=%v", len(page), err)
	}
	if _, err := s.Submit(context.Background(), []Command{{Kind: "recover", At: time.Now()}}); err == nil {
		t.Fatal("closed store accepted a new command")
	}
}

func TestSubmitReportsTypedUnconfirmedApplyFailures(t *testing.T) {
	t.Run("history failure after application", func(t *testing.T) {
		s := openCatalogMemory(t)
		r := catalogRecord(t, s, "Credential", "one", "uid", "r1", "secret")
		mutation := CatalogMutation{Record: r, Create: true}
		failedWrite := errors.New("test history write failed")
		s.fsm.history.mu.Lock()
		s.fsm.history.err = failedWrite
		s.fsm.history.mu.Unlock()
		_, err := s.Submit(context.Background(), []Command{{Kind: "catalog", At: r.UpdatedAt, Catalog: &mutation}})
		if !errors.Is(err, ErrCommitUnconfirmed) || !errors.Is(err, failedWrite) {
			t.Fatalf("post-application failure lost ambiguous outcome or cause: %v", err)
		}
		s.fsm.mu.RLock()
		_, applied := s.fsm.image.Catalog[r.Key.indexKey()]
		s.fsm.mu.RUnlock()
		if !applied || s.Status().Ready || budgetUsed(&s.budget) != 0 {
			t.Fatal("failure boundary did not apply, fail readiness, and release admission")
		}
		_, err = s.Submit(context.Background(), []Command{{Kind: "recover", At: time.Now()}})
		if err == nil || errors.Is(err, ErrCommitUnconfirmed) {
			t.Fatalf("previously failed store misreported a command never sent to Apply: %v", err)
		}
	})
	t.Run("Raft future rejection", func(t *testing.T) {
		c := testConfig(t)
		s, err := Open(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.raft.Shutdown().Error(); err != nil {
			t.Fatal(err)
		}
		_, err = s.Submit(context.Background(), []Command{{Kind: "recover", At: time.Now()}})
		if !errors.Is(err, ErrCommitUnconfirmed) || budgetUsed(&s.budget) != 0 {
			t.Fatalf("admitted Raft future error lacks ambiguous outcome or leaked reservation: %v", err)
		}
	})
}
