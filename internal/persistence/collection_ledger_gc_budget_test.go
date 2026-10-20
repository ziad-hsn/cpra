//go:build linux || darwin || windows

package persistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func TestCollectionGCProgressAfterSlowDurabilityPreparation(t *testing.T) {
	for _, test := range []struct {
		name  string
		steps int
		phase string
		gone  string
	}{
		{"database", 1, "removed-ledger.db", "ledger.db"},
		{"marker", 2, "removed-generation.json", "generation.json"},
		{"empty-directory", 3, "removed-directory", ""},
		{"missing-directory", 4, "complete", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCollectionGCFixture(t)
			r := f.open(t)
			for range test.steps {
				gcTestStep(t, r, f.old)
			}
			now := time.Now()
			started := now
			r.clock = func() time.Time { return now }
			nativeCheckpoint := r.checkpoint
			r.checkpoint = func(directory *os.File) error {
				err := nativeCheckpoint(directory)
				now = now.Add(30 * time.Millisecond)
				return err
			}
			step, err := r.Step(context.Background(), f.old)
			if err != nil {
				t.Fatalf("successful durability preparation prevented retirement progress: %v", err)
			}
			if step.Phase != test.phase || step.Generation != f.old || now.Sub(started) <= 25*time.Millisecond {
				t.Fatalf("retirement did not complete its admitted transition: %+v, duration=%v", step, now.Sub(started))
			}
			if _, err := os.Lstat(filepath.Join(f.dir, f.old, test.gone)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("retired entry remains", err)
			}
			if test.gone != "" {
				if _, err := os.Lstat(filepath.Join(f.dir, f.old)); err != nil {
					t.Fatal("same turn also removed the generation directory", err)
				}
			}
			if test.gone == "ledger.db" {
				if _, err := os.Lstat(filepath.Join(f.dir, f.old, "generation.json")); err != nil {
					t.Fatal("same turn also removed the marker", err)
				}
			}
			_, intentErr := os.Lstat(filepath.Join(f.dir, collectionGCIntentName))
			if test.phase == "complete" && !errors.Is(intentErr, os.ErrNotExist) || test.phase != "complete" && intentErr != nil {
				t.Fatal("more than one retirement transition or incomplete finalization", intentErr)
			}
			f.assertProtected(t)
		})
	}
}

func TestCollectionGCBudgetExpiresBeforeUnitAdmission(t *testing.T) {
	for _, steps := range []int{3, 4} {
		t.Run(fmt.Sprint(steps), func(t *testing.T) {
			f := newCollectionGCFixture(t)
			r := f.open(t)
			for range steps {
				gcTestStep(t, r, f.old)
			}
			now := time.Now()
			r.clock = func() time.Time { return now }
			r.checkpoint = func(*os.File) error {
				t.Fatal("expired turn started durability preparation")
				return nil
			}
			step, err := r.step(context.Background(), f.old, now.Add(-time.Nanosecond))
			if !errors.Is(err, errCollectionGCBudget) || step.Phase != "" || step.UnlinkedBytes != 0 {
				t.Fatal("expired owner deadline was renewed", step, err)
			}
			if _, err := os.Lstat(filepath.Join(f.dir, collectionGCIntentName)); err != nil {
				t.Fatal("expired turn removed the intent", err)
			}
			f.assertProtected(t)
		})
	}
}

func TestCollectionGCPreparationPreservesCallerCancellation(t *testing.T) {
	for _, steps := range []int{3, 4} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("steps=%d/deadline=%t", steps, deadline), func(t *testing.T) {
				f := newCollectionGCFixture(t)
				r := f.open(t)
				for range steps {
					gcTestStep(t, r, f.old)
				}
				ctx, cancel := context.WithCancel(context.Background())
				want := context.Canceled
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), time.Second)
					want = context.DeadlineExceeded
				}
				defer cancel()
				nativeCheckpoint := r.checkpoint
				prepared := false
				r.checkpoint = func(directory *os.File) error {
					if err := nativeCheckpoint(directory); err != nil {
						return err
					}
					prepared = true
					if deadline {
						<-ctx.Done()
					} else {
						cancel()
					}
					return nil
				}
				step, err := r.Step(ctx, f.old)
				if !prepared || !errors.Is(err, want) || step.Phase != "" || step.UnlinkedBytes != 0 {
					t.Fatal("caller cancellation did not veto unlink", prepared, step, err)
				}
				if steps == 3 {
					if _, err := os.Lstat(filepath.Join(f.dir, f.old)); err != nil {
						t.Fatal("canceled turn removed the directory", err)
					}
				}
				if _, err := os.Lstat(filepath.Join(f.dir, collectionGCIntentName)); err != nil {
					t.Fatal("canceled turn removed the intent", err)
				}
				f.assertProtected(t)
			})
		}
	}
}

func TestCollectionGCFailedPreparationCannotUnlink(t *testing.T) {
	for _, steps := range []int{3, 4} {
		t.Run(fmt.Sprint(steps), func(t *testing.T) {
			f := newCollectionGCFixture(t)
			r := f.open(t)
			for range steps {
				gcTestStep(t, r, f.old)
			}
			r.checkpoint = func(*os.File) error { return errors.New("injected directory flush failure") }
			step, err := r.Step(context.Background(), f.old)
			if !errors.Is(err, errCollectionGCBlocked) || step.Phase != "" {
				t.Fatal("failed durability preparation authorized removal", step, err)
			}
			if steps == 3 {
				if _, err := os.Lstat(filepath.Join(f.dir, f.old)); err != nil {
					t.Fatal("failed preparation removed the directory", err)
				}
			}
			if _, err := os.Lstat(filepath.Join(f.dir, collectionGCIntentName)); err != nil {
				t.Fatal("failed preparation removed the intent", err)
			}
			f.assertProtected(t)
		})
	}
}

func TestCollectionGCCompletesCheckpointAfterUnlinkCancellation(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	for range 3 {
		gcTestStep(t, r, f.old)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nativeCheckpoint := r.checkpoint
	finalCheckpoint := false
	r.checkpoint = func(directory *os.File) error {
		if _, err := os.Lstat(filepath.Join(f.dir, f.old)); errors.Is(err, os.ErrNotExist) {
			cancel()
			finalCheckpoint = true
		}
		return nativeCheckpoint(directory)
	}
	step, err := r.Step(ctx, f.old)
	if err != nil || step.Phase != "removed-directory" || !finalCheckpoint || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("unlink did not finish its mandatory checkpoint", step, finalCheckpoint, err)
	}
	f.assertProtected(t)
}

func TestCollectionGenerationOwnerPreservesOriginalBudget(t *testing.T) {
	owner := generationOwnerFixture(t, filepath.Join(t.TempDir(), "collections"), uuid.NewString())
	for range 3 {
		if err := publishGenerationFixture(t, owner).Close(); err != nil {
			t.Fatal(err)
		}
	}
	owner.mu.Lock()
	if err := owner.openRetirement(); err != nil {
		owner.mu.Unlock()
		t.Fatal(err)
	}
	old := owner.catalog.Completed[0]
	step := gcTestStep(t, owner.retirement, old)
	if step.Phase != "intent" {
		owner.mu.Unlock()
		t.Fatal("fixture did not admit its intent", step)
	}
	owner.pending = old
	// Earlier owner work has used the original budget. Step would incorrectly
	// grant a fresh allowance from this later clock; step must use the deadline
	// captured by maintain before publication, opening and eligibility checks.
	now := time.Now().Add(time.Hour)
	owner.retirement.clock = func() time.Time { return now }
	owner.retirement.checkpoint = func(*os.File) error {
		t.Error("owner renewed the budget before durability preparation")
		return errors.New("unexpected preparation")
	}
	owner.mu.Unlock()
	if err := owner.maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	status := owner.accounting()
	if status.CleanupCondition != "budget_exhausted" || status.UnlinkedBytes != 0 || status.Retired != 0 {
		t.Fatal("owner did not preserve its original deadline", status)
	}
	for _, name := range []string{filepath.Join(old, "ledger.db"), collectionGCIntentName} {
		if _, err := os.Stat(filepath.Join(owner.directory, name)); err != nil {
			t.Fatal("expired owner removed an entry", err)
		}
	}
}

// Use committed input and real restarts to produce a reconstructable old ledger.
// The callback installs only per-owner checkpoint instrumentation, with the same
// owner lock used by the real maintenance loop. All native syncs still execute.
func openStoreForCollectionRetirement(t *testing.T, prepare func(*Store, *collectionRetirement)) (*Store, string, string) {
	t.Helper()
	config := testConfig(t)
	collectionGCTestPrivate(t, config.Storage.Directory)
	var operation string
	for cycle := 0; cycle < 3; cycle++ {
		store, err := Open(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		if cycle == 0 {
			operation = collectionFill(t, store, 5).ID
		}
		owner := store.collectionGenerations
		if err := owner.publish(owner.active.Load()); err != nil {
			t.Fatal(err)
		}
		if cycle != 2 {
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		owner.mu.Lock()
		defer owner.mu.Unlock()
		if len(owner.catalog.Completed) != 3 {
			t.Fatal("fixture did not retain three real generations", owner.catalog)
		}
		if err := owner.openRetirement(); err != nil {
			t.Fatal(err)
		}
		old := owner.catalog.Completed[0]
		retirement := owner.retirement
		raw, _, _, err := collectionGCRead(retirement.root, collectionGCIntentName, retirement.identity)
		if errors.Is(err, os.ErrNotExist) {
			step := gcTestStep(t, retirement, old)
			if step.Phase != "intent" {
				t.Fatal("fixture did not admit its retirement", step)
			}
		} else {
			// The real loop may already have admitted work before the fixture
			// acquired ownership. Preserve its intent without advancing it.
			var intent collectionGCIntent
			if err != nil || collectionGCDecode(raw, &intent) != nil || !intent.valid(retirement.identity, owner.protection()) || intent.Generation != old {
				t.Fatal("fixture found an unexpected existing retirement", err)
			}
			if _, err := os.Stat(filepath.Join(owner.directory, old, "ledger.db")); err != nil {
				t.Fatal("fixture setup was delayed past database retirement", err)
			}
		}
		owner.pending = old
		prepare(store, owner.retirement)
		return store, old, operation
	}
	t.Fatal("fixture did not open a cleanup owner")
	return nil, "", ""
}

func TestStoreCollectionRetirementProgressAfterSlowPreparation(t *testing.T) {
	var expectedBytes int64
	store, old, operation := openStoreForCollectionRetirement(t, func(store *Store, retirement *collectionRetirement) {
		owner := store.collectionGenerations
		for _, leaf := range []string{"ledger.db", "generation.json"} {
			info, err := os.Stat(filepath.Join(owner.directory, owner.pending, leaf))
			if err != nil {
				t.Fatal(err)
			}
			expectedBytes += info.Size()
		}
		nativeCheckpoint := retirement.checkpoint
		retirement.checkpoint = func(directory *os.File) error {
			if err := nativeCheckpoint(directory); err != nil {
				return err
			}
			// A successful native operation that finishes after the cooperative
			// allowance must not cause its caller to cancel every deletion attempt.
			<-time.After(30 * time.Millisecond)
			return nil
		}
	})
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	observedOverrun := false
	var final CollectionStorageAccounting
	for final.Retired == 0 {
		select {
		case <-deadline.C:
			t.Fatal("background cleanup did not progress", store.Status())
		case <-poll.C:
			status := store.Status()
			if !status.Ready || status.Collections == nil {
				t.Fatal("slow cleanup made storage unavailable", status)
			}
			final = *status.Collections
			if final.UnlinkedBytes > 0 && final.CleanupCondition == "budget_exceeded" && final.StepDurationMS >= 30 {
				observedOverrun = true
			}
		}
	}
	if !observedOverrun || final.Retired != 1 || final.Active != 1 || final.Retained != 1 || final.UnlinkedBytes != expectedBytes {
		t.Fatal("background cleanup lost progress or overrun accounting", observedOverrun, expectedBytes, final)
	}
	owner := store.collectionGenerations
	owner.mu.Lock()
	retained := []string{owner.catalog.Current, owner.catalog.Previous}
	owner.mu.Unlock()
	for _, name := range []string{old, collectionGCIntentName} {
		if _, err := os.Stat(filepath.Join(owner.directory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("completed retirement left its generation or intent", err)
		}
	}
	for _, name := range retained {
		if _, err := os.Stat(filepath.Join(owner.directory, name, "ledger.db")); err != nil {
			t.Fatal("background cleanup removed a protected ledger", err)
		}
	}
	items, err := store.CollectionPage(operation, 0, 100)
	if err != nil || len(items) != 5 {
		t.Fatal("background cleanup changed committed input", len(items), err)
	}
	t.Logf("retired=%d unlinked_bytes=%d final_step_ms=%.3f observed_slow_step=%t", final.Retired, final.UnlinkedBytes, final.StepDurationMS, observedOverrun)
}

func TestStoreCollectionRetirementShutdownDuringPreparation(t *testing.T) {
	prepared := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releasePreparation := func() { releaseOnce.Do(func() { close(release) }) }
	defer releasePreparation()
	store, old, _ := openStoreForCollectionRetirement(t, func(_ *Store, retirement *collectionRetirement) {
		nativeCheckpoint := retirement.checkpoint
		var once sync.Once
		retirement.checkpoint = func(directory *os.File) error {
			if err := nativeCheckpoint(directory); err != nil {
				return err
			}
			once.Do(func() {
				close(prepared)
				<-release
			})
			return nil
		}
	})
	select {
	case <-prepared:
	case <-time.After(10 * time.Second):
		t.Fatal("background cleanup did not prepare its transition")
	}
	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	select {
	case <-store.maintenanceContext.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel background maintenance")
	}
	select {
	case err := <-closed:
		t.Fatal("shutdown released ownership before maintenance drained", err)
	default:
	}
	if lock, err := LockOffline(store.config.Storage.Directory); err == nil {
		_ = lock.Close()
		t.Fatal("stopped-store ownership was available during preparation")
	} else if !errors.Is(err, bolt.ErrTimeout) {
		t.Fatal("offline ownership failed for a cause other than the held lock", err)
	}
	if store.Status().Ready {
		t.Fatal("draining store remained ready")
	}
	releasePreparation()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not finish after canceled preparation")
	}
	for _, name := range []string{filepath.Join(old, "ledger.db"), collectionGCIntentName} {
		if _, err := os.Stat(filepath.Join(store.collectionGenerations.directory, name)); err != nil {
			t.Fatal("shutdown cancellation did not preserve pending work", err)
		}
	}
	lock, err := LockOffline(store.config.Storage.Directory)
	if err != nil {
		t.Fatal("drained shutdown did not release ownership", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
