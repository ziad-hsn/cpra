package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func reselectionManagerFixture(t *testing.T) (*reselectionProofFixture, *CollectionReselectionManager, *atomic.Int64) {
	t.Helper()
	f := newReselectionProofFixture(t, 1, nil, nil)
	clock := &atomic.Int64{}
	clock.Store(f.at.UnixNano())
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := protectNewStartupDirectory(parent); err != nil {
		t.Fatal(err)
	}
	m, err := NewCollectionReselectionManager(t.Context(), f.catalog, parent, func() time.Time { return time.Unix(0, clock.Load()).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.BeginStop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Wait(ctx); err != nil {
			t.Error(err)
		}
	})
	return f, m, clock
}

func managerUploadSources(t *testing.T, m *CollectionReselectionManager, f *reselectionProofFixture, raw [][]byte) CollectionReselectionStatus {
	t.Helper()
	s, err := m.Create(t.Context(), f.head.ID, "team/operator", collection.FileNormalizationProfile, len(raw))
	if err != nil {
		t.Fatal(err)
	}
	for n, data := range raw {
		s, err = m.Upload(t.Context(), f.head.ID, s.ID, "team/operator", uint64(n+1), 0, true, data)
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.NextSource != 0 || s.NextOffset != 0 || s.SourcesCompleted != len(raw) {
		t.Fatal("complete source position wrong", s)
	}
	return s
}

func managerWaitPhase(t *testing.T, m *CollectionReselectionManager, id, attempt, phase string) CollectionReselectionStatus {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		s, err := m.Get(t.Context(), id, attempt, "team/operator")
		if err != nil {
			t.Fatal(err)
		}
		if s.Phase == phase {
			return s
		}
		if s.Phase == "failed" {
			t.Fatal("attempt failed", s.ErrorCode)
		}
		select {
		case <-deadline:
			t.Fatal("attempt did not reach phase", phase, s.Phase)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestCollectionReselectionManagerContinuesOriginalUpload(t *testing.T) {
	f, m, _ := reselectionManagerFixture(t)
	before, err := f.store.CollectionPage(f.head.ID, 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	index := f.store.Status().CommittedIndex
	s := managerUploadSources(t, m, f, f.raw)
	if _, err := m.Verify(t.Context(), f.head.ID, s.ID, "team/operator"); err != nil {
		t.Fatal(err)
	}
	managerWaitPhase(t, m, f.head.ID, s.ID, "verified")
	if f.store.Status().CommittedIndex != index {
		t.Fatal("proof committed a mutation")
	}
	if _, err := m.Resume(t.Context(), f.head.ID, s.ID, "team/operator"); err != nil {
		t.Fatal(err)
	}
	s = managerWaitPhase(t, m, f.head.ID, s.ID, "completed")
	if s.OperationID != f.head.ID || s.OperationUploaded != 2 {
		t.Fatal("progress lost original identity", s)
	}
	after, err := f.store.CollectionPage(f.head.ID, 0, 256)
	if err != nil || len(after) != 2 || !reflect.DeepEqual(before[0], after[0]) {
		t.Fatal("original ciphertext changed", err)
	}
	head, _, err := f.store.CollectionGet(f.head.ID)
	if err != nil || head.Phase != "uploading" || head.Uploaded != 2 || head.UploadID != f.head.UploadID {
		t.Fatal("transfer activated or changed original upload", err)
	}
	active, err := f.store.CatalogSnapshot()
	if err != nil || active.Len() != 0 {
		t.Fatal("transfer activated resources", err)
	}
	index = f.store.Status().CommittedIndex
	if _, err := m.Resume(t.Context(), f.head.ID, s.ID, "team/operator"); err != nil {
		t.Fatal(err)
	}
	if err := m.Discard(t.Context(), f.head.ID, s.ID, "team/operator"); err != nil {
		t.Fatal(err)
	}
	if f.store.Status().CommittedIndex != index {
		t.Fatal("completed retry or discard mutated operation")
	}
	if _, err := m.Get(t.Context(), f.head.ID, s.ID, "team/operator"); !errors.Is(err, ErrReselectionNotFound) {
		t.Fatal("discarded attempt found", err)
	}
}

func TestCollectionReselectionManagerMismatchExpiryAndOwner(t *testing.T) {
	f, m, clock := reselectionManagerFixture(t)
	raw := append([][]byte(nil), f.raw...)
	raw[0] = append([]byte("# changed source\n"), raw[0]...)
	s := managerUploadSources(t, m, f, raw)
	if _, err := m.Get(t.Context(), f.head.ID, s.ID, "other/operator"); !errors.Is(err, ErrReselectionNotFound) {
		t.Fatal("foreign actor observed attempt", err)
	}
	if _, err := m.Verify(t.Context(), f.head.ID, s.ID, "team/operator"); err != nil {
		t.Fatal(err)
	}
	failed := managerWaitPhase(t, m, f.head.ID, s.ID, "failed")
	if failed.ErrorCode != "input_mismatch" {
		t.Fatal("private parser/commitment failure leaked", failed.ErrorCode)
	}
	if _, err := m.Resume(t.Context(), f.head.ID, s.ID, "team/operator"); !errors.Is(err, ErrReselectionConflict) {
		t.Fatal("failed proof permitted transfer", err)
	}
	if err := m.Discard(t.Context(), f.head.ID, s.ID, "team/operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(t.Context(), f.head.ID, "team/operator", collection.FileNormalizationProfile, 3); !errors.Is(err, ErrReselectionBusy) {
		t.Fatal("attempt creation throttling bypassed", err)
	}
	clock.Add(int64(6 * time.Second))
	s = managerUploadSources(t, m, f, f.raw)
	clock.Store(s.ExpiresAt.UnixNano())
	if _, err := m.Get(t.Context(), f.head.ID, s.ID, "team/operator"); !errors.Is(err, persistence.ErrOperationExpired) {
		t.Fatal("expired attempt readable", err)
	}
	m.expire()
	if _, err := m.Get(t.Context(), f.head.ID, s.ID, "team/operator"); !errors.Is(err, ErrReselectionNotFound) {
		t.Fatal("expired attempt retained", err)
	}
	head, _, err := f.store.CollectionGet(f.head.ID)
	if err != nil || !reflect.DeepEqual(head, f.head) {
		t.Fatal("failed/expired attempt changed original state", err)
	}
}

func TestCollectionReselectionManagerConcurrentCreateAndReaders(t *testing.T) {
	f, m, _ := reselectionManagerFixture(t)
	var wg sync.WaitGroup
	var winners atomic.Int32
	var winner CollectionReselectionStatus
	var mu sync.Mutex
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := m.Create(t.Context(), f.head.ID, "team/operator", collection.FileNormalizationProfile, 3)
			if err == nil {
				winners.Add(1)
				mu.Lock()
				winner = s
				mu.Unlock()
			} else if !errors.Is(err, ErrReselectionBusy) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("duplicate attempt owners", winners.Load())
	}
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				if _, err := m.Get(t.Context(), f.head.ID, winner.ID, "team/operator"); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}

type reselectionManagerUnwrapGate struct {
	secureconfig.KeyWrapper
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *reselectionManagerUnwrapGate) Unwrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	g.once.Do(func() { close(g.entered) })
	// Deliberately uncooperative key service; manager must retain ownership
	// after the caller's shutdown deadline rather than closing beneath it.
	<-g.release
	return g.KeyWrapper.Unwrap(ctx, key, aad)
}

func TestCollectionReselectionManagerShutdownRetainsOwnedStorage(t *testing.T) {
	f, m, _ := reselectionManagerFixture(t)
	s := managerUploadSources(t, m, f, f.raw)
	base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	gate := &reselectionManagerUnwrapGate{KeyWrapper: base, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate.release) }) })
	f.catalog.sealer, _ = secureconfig.NewSealer(gate)
	if _, err := m.Verify(t.Context(), f.head.ID, s.ID, "team/operator"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("verification did not unwrap")
	}
	m.BeginStop()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := m.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unjoined key operation reported stopped", err)
	}
	if m.Ready() {
		t.Fatal("stopping manager stayed ready")
	}
	select {
	case <-m.Done():
		t.Fatal("Done released ownership while key service was unjoined")
	default:
	}
	if _, err := newReselectionSpoolRoot(t.Context(), filepath.Dir(m.root.directory), reselectionRootOptions()); !errors.Is(err, errReselectionSpoolLocked) {
		t.Fatal("shutdown deadline released OS lock", err)
	}
	release.Do(func() { close(gate.release) })
	if err := m.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.Done():
	default:
		t.Fatal("successful Wait did not publish Done")
	}
	if err := m.Err(); err != nil {
		t.Fatal("joined owner reported an ordinary canceled attempt as fatal", err)
	}
	head, _, err := f.store.CollectionGet(f.head.ID)
	if err != nil || !reflect.DeepEqual(head, f.head) {
		t.Fatal("shutdown verification modified operation", err)
	}
}

func TestCollectionReselectionManagerZeroLifecycleAndFormatting(t *testing.T) {
	for _, m := range []*CollectionReselectionManager{nil, {}} {
		if m.Ready() || m.Err() != nil {
			t.Fatal("uninitialized manager has live ownership")
		}
		m.BeginStop()
		if err := m.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-m.Done():
		default:
			t.Fatal("uninitialized owner did not report stopped")
		}
		if err := m.CheckCreate(t.Context(), "original", "actor"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("uninitialized owner admitted a read", err)
		}
	}
	m := CollectionReselectionManager{collectionReselectionManagerState: &collectionReselectionManagerState{lastCreate: map[string]time.Time{"private-original-id": time.Now()}}}
	a := collectionReselectionAttempt{collectionReselectionAttemptState: &collectionReselectionAttemptState{head: persistence.CollectionState{UploadID: "private-upload-id"}}}
	for _, value := range []any{m, &m, a, &a} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			got := fmt.Sprintf(format, value)
			if got != "collection reselection manager" && got != "private collection reselection attempt" {
				t.Fatal("private state formatting escaped redaction", got)
			}
		}
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("private manager state serialized")
		}
	}
}

func TestCollectionReselectionManagerConcurrentFormatting(t *testing.T) {
	f, m, clock := reselectionManagerFixture(t)
	var current atomic.Pointer[collectionReselectionAttempt]
	stop, finished := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(stop) }); <-finished })
	go func() {
		defer close(finished)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if got := fmt.Sprintf("%v/%+v/%#v", m, *m, m); got != "collection reselection manager/collection reselection manager/collection reselection manager" {
				t.Error("manager escaped redaction", got)
				return
			}
			if a := current.Load(); a != nil {
				if got := fmt.Sprintf("%v/%+v/%#v", a, *a, a); got != "private collection reselection attempt/private collection reselection attempt/private collection reselection attempt" {
					t.Error("attempt escaped redaction", got)
					return
				}
			}
		}
	}()
	for range 6 {
		status, err := m.Create(t.Context(), f.head.ID, "team/operator", collection.FileNormalizationProfile, len(f.raw))
		if err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		current.Store(m.attempts[status.ID])
		m.mu.Unlock()
		if _, err := m.Upload(t.Context(), f.head.ID, status.ID, "team/operator", 1, 0, true, f.raw[0]); err != nil {
			t.Fatal(err)
		}
		if err := m.Discard(t.Context(), f.head.ID, status.ID, "team/operator"); err != nil {
			t.Fatal(err)
		}
		clock.Add(int64(6 * time.Second))
	}
	m.BeginStop()
	if err := m.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionReselectionManagerCreatePreflightReadOnly(t *testing.T) {
	f, m, clock := reselectionManagerFixture(t)
	index := f.store.Status().CommittedIndex
	for range 2 {
		if err := m.CheckCreate(t.Context(), f.head.ID, "team/operator"); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.CheckCreate(t.Context(), f.head.ID, "other/operator"); err == nil {
		t.Fatal("foreign actor passed pre-body check")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.CheckCreate(ctx, f.head.ID, "team/operator"); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-body check ignored cancellation", err)
	}
	clock.Store(f.head.ExpiresAt.UnixNano())
	if err := m.CheckCreate(t.Context(), f.head.ID, "team/operator"); !errors.Is(err, persistence.ErrOperatorAuthorityDenied) {
		// This fixture's operator expires before its original upload. The
		// pre-body check must preserve authority-before-protected-read order.
		t.Fatal("expired actor passed pre-body check", err)
	}
	m.mu.Lock()
	allocated := len(m.attempts) != 0 || len(m.lastCreate) != 0 || m.reserved != 0
	m.mu.Unlock()
	files, err := os.ReadDir(m.root.directory)
	if err != nil || len(files) != 1 || files[0].Name() != reselectionOwnershipFile || allocated || f.store.Status().CommittedIndex != index {
		t.Fatal("pre-body checks allocated staging or changed original operation", err)
	}
	m.BeginStop()
	if err := m.CheckCreate(t.Context(), f.head.ID, "team/operator"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("stopped owner accepted pre-body check", err)
	}
}

func TestCollectionReselectionManagerOriginalExpiryKeepsMonotonicClock(t *testing.T) {
	at := time.Now()
	if at == at.Round(0) {
		t.Fatal("fixture lacks a monotonic clock")
	}
	for _, remaining := range []time.Duration{-time.Nanosecond, 0, time.Minute, reselectionAttemptLifetime, time.Hour} {
		original := at.Add(remaining).Round(0) // A persisted timestamp has no monotonic part.
		expiry := reselectionAttemptExpiry(at, original)
		if expiry.Sub(at) != min(remaining, reselectionAttemptLifetime) || expiry == expiry.Round(0) {
			t.Fatal("original expiry cap lost its fixed local clock", remaining)
		}
	}
}

type reselectionManagerDeadlineGate struct {
	secureconfig.KeyWrapper
	deadline chan time.Time
	stopped  chan error
}

func (g *reselectionManagerDeadlineGate) Unwrap(ctx context.Context, _, _ []byte) ([]byte, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		g.deadline <- time.Time{}
	} else {
		g.deadline <- deadline
	}
	<-ctx.Done()
	g.stopped <- ctx.Err()
	return nil, ctx.Err()
}

func TestCollectionReselectionManagerWorkerDeadlineBound(t *testing.T) {
	for _, remaining := range []time.Duration{500 * time.Millisecond, 3 * time.Minute} {
		t.Run(remaining.String(), func(t *testing.T) {
			f, m, clock := reselectionManagerFixture(t)
			status := managerUploadSources(t, m, f, f.raw)
			clock.Store(status.ExpiresAt.Add(-remaining).UnixNano())
			base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
			gate := &reselectionManagerDeadlineGate{KeyWrapper: base, deadline: make(chan time.Time, 1), stopped: make(chan error, 1)}
			f.catalog.sealer, _ = secureconfig.NewSealer(gate)
			if _, err := m.Verify(t.Context(), f.head.ID, status.ID, "team/operator"); err != nil {
				t.Fatal(err)
			}
			select {
			case deadline := <-gate.deadline:
				if deadline.IsZero() || time.Until(deadline) > min(remaining, reselectionWorkTimeout) {
					t.Fatal("worker exceeds its fixed attempt or work deadline", deadline)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not reach the key boundary")
			}
			want := error(context.DeadlineExceeded)
			if remaining > reselectionWorkTimeout {
				m.BeginStop()
				want = context.Canceled
			}
			select {
			case err := <-gate.stopped:
				if !errors.Is(err, want) {
					t.Fatal("worker stopped for the wrong reason", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("worker waited for expiry polling instead of its deadline")
			}
			// The injected observation clock remains before expiry, so the
			// expiration sweep cannot be the source of the short cancellation.
		})
	}
}

func TestCollectionReselectionManagerCleanupOwnership(t *testing.T) {
	f := newReselectionProofFixture(t, 1, nil, nil)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := protectNewStartupDirectory(parent); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	want := errors.New("fixture cleanup failure")
	cleanup := func(root *reselectionSpoolRoot) error {
		calls.Add(1)
		// Acquiring the same OS ownership proves root.Close completed first.
		reopened, err := newReselectionSpoolRoot(context.Background(), filepath.Dir(root.directory), reselectionRootOptions())
		if err != nil {
			t.Error("cleanup ran before the original root closed", err)
		} else if err := reopened.Close(); err != nil {
			t.Error(err)
		}
		close(entered)
		<-release
		return want
	}
	m, err := newCollectionReselectionManager(t.Context(), f.catalog, parent, func() time.Time { return f.at }, cleanup)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }); m.BeginStop(); _ = m.Wait(context.Background()) })
	var stops sync.WaitGroup
	for range 8 {
		stops.Add(1)
		go func() { defer stops.Done(); m.BeginStop() }()
	}
	stops.Wait()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := m.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("waiting cleanup reported joined", err)
	}
	select {
	case <-m.Done():
		t.Fatal("Done closed before runtime cleanup")
	default:
	}
	once.Do(func() { close(release) })
	if err := m.Wait(t.Context()); !errors.Is(err, want) || !errors.Is(m.Err(), want) || calls.Load() != 1 {
		t.Fatal("cleanup failure or ownership lost", err, m.Err(), calls.Load())
	}
	if _, err := newCollectionReselectionManager(t.Context(), nil, parent, time.Now, cleanup); !errors.Is(err, ErrValidation) || calls.Load() != 1 {
		t.Fatal("failed construction consumed caller cleanup", err)
	}
}

func TestCollectionReselectionManagerBlockedUploadWaitersCancel(t *testing.T) {
	f, m, _ := reselectionManagerFixture(t)
	status, err := m.Create(t.Context(), f.head.ID, "team/operator", collection.FileNormalizationProfile, len(f.raw))
	if err != nil {
		t.Fatal(err)
	}
	index := f.store.Status().CommittedIndex
	m.mu.Lock()
	a := m.attempts[status.ID]
	m.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	a.spool.mu.Lock()
	write := a.spool.writeAt
	a.spool.writeAt = func(frame []byte, offset int64) (int, error) {
		close(entered)
		<-release // An actual spool write holds the attempt and spool locks.
		return write(frame, offset)
	}
	a.spool.mu.Unlock()
	upload := make(chan error, 1)
	go func() {
		_, err := m.Upload(t.Context(), f.head.ID, status.ID, "team/operator", 1, 0, true, f.raw[0])
		upload <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not reach the file write")
	}
	checks := map[string]func(context.Context) error{
		"get": func(ctx context.Context) error {
			_, err := m.Get(ctx, f.head.ID, status.ID, "team/operator")
			return err
		},
		"upload": func(ctx context.Context) error {
			_, err := m.Upload(ctx, f.head.ID, status.ID, "team/operator", 1, 0, true, f.raw[0])
			return err
		},
		"verify": func(ctx context.Context) error {
			_, err := m.Verify(ctx, f.head.ID, status.ID, "team/operator")
			return err
		},
		"resume": func(ctx context.Context) error {
			_, err := m.Resume(ctx, f.head.ID, status.ID, "team/operator")
			return err
		},
		"discard": func(ctx context.Context) error { return m.Discard(ctx, f.head.ID, status.ID, "team/operator") },
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- check(ctx) }()
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("blocked waiter ignored its deadline", err)
				}
			case <-time.After(time.Second):
				t.Fatal("public method remained behind uncooperative disk I/O")
			}
		})
	}
	m.BeginStop()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := m.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown abandoned the actual writer", err)
	}
	select {
	case <-m.Done():
		t.Fatal("Done closed over live file ownership")
	default:
	}
	once.Do(func() { close(release) })
	if err := <-upload; !errors.Is(err, ErrUnavailable) {
		t.Fatal("upload ignored component shutdown after its file write", err)
	}
	if err := m.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.store.Status().CommittedIndex != index {
		t.Fatal("blocked staging or canceled waiters mutated original operation")
	}
}
