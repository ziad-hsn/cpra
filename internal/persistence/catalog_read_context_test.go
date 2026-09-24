package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

func catalogContextReads(s *Store, key CatalogKey, cursor CatalogCursor) []controllerContextRead {
	return []controllerContextRead{
		{"snapshot", func(ctx context.Context) error { _, err := s.CatalogSnapshotContext(ctx); return err }},
		{"changes", func(ctx context.Context) error { _, err := s.CatalogChangesSinceContext(ctx, cursor, 10); return err }},
		{"dependents", func(ctx context.Context) error { _, _, err := s.CatalogDependentsContext(ctx, key, 100); return err }},
		{"pending operations", func(ctx context.Context) error { _, err := s.PendingOperationsContext(ctx); return err }},
	}
}

func TestCatalogContextReadsHonorLockDeadlines(t *testing.T) {
	s := openCatalogMemory(t)
	m, _ := managedControlMonitor(t, s, "catalog-read-deadlines")
	view, err := s.CatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range catalogContextReads(s, CatalogKey{Kind: "Monitor", ID: m.ID}, view.Cursor) {
		for _, blocked := range []string{"store", "fsm"} {
			t.Run(check.name+"/"+blocked, func(t *testing.T) {
				var release func()
				if blocked == "store" {
					s.mu.Lock()
					release = s.mu.Unlock
				} else {
					s.fsm.mu.Lock()
					release = s.fsm.mu.Unlock
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
				completed := make(chan error, 1)
				go func() { completed <- check.read(ctx) }()
				select {
				case err := <-completed:
					release()
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal("catalog lock wait lost caller deadline", err)
					}
				case <-time.After(time.Second):
					release()
					<-completed
					t.Fatal("catalog read blocked shutdown cancellation")
				}
				if err := check.read(context.Background()); err != nil {
					t.Fatal("lock cancellation prevented a later read", err)
				}
			})
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := check.read(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s ignored canceled context: %v", check.name, err)
		}
	}
}

func TestCatalogContextReadsPreserveNativeInspectionAndPermanentHealth(t *testing.T) {
	s := openCatalogMemory(t)
	m, _ := managedControlMonitor(t, s, "native-catalog-reads")
	s.fsm.mu.RLock()
	baseline := s.fsm.image
	s.fsm.mu.RUnlock()
	for _, scenario := range []struct {
		name string
		set  func(*Store)
		want error
	}{
		{"administrative", func(s *Store) { s.administrative = true }, nil},
		{"opening", func(s *Store) { s.opening = true }, nil},
		{"restore", func(s *Store) { s.fsm.image.Restore = &RestoreState{Phase: "pending"} }, nil},
		{"authentication reset", func(s *Store) { s.fsm.image.Authentication = &AuthenticationState{ResetRequired: true} }, nil},
		{"bootstrap", func(s *Store) { s.fsm.image.Bootstrap = &BootstrapState{Phase: "seeding"} }, ErrBootstrapPending},
		{"store fault before bootstrap", func(s *Store) {
			s.err = errors.Join(errors.New("storage fault"), raft.ErrNotLeader)
			s.fsm.image.Bootstrap = &BootstrapState{Phase: "seeding"}
		}, errCatalogUnavailable},
		{"fsm fault before bootstrap", func(s *Store) {
			s.fsm.err = errors.Join(errors.New("history fault"), raft.ErrNotLeader)
			s.fsm.image.Bootstrap = &BootstrapState{Phase: "seeding"}
		}, errCatalogUnavailable},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// These isolated owners have no background worker. Their retained image
			// is only read; readiness flags never modify the live fixture store.
			native := &Store{stop: make(chan struct{}), fsm: &machine{image: baseline}}
			view, err := native.CatalogSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			scenario.set(native)
			checks := catalogContextReads(native, CatalogKey{Kind: "Monitor", ID: m.ID}, view.Cursor)
			checks = append(checks,
				controllerContextRead{"legacy snapshot", func(context.Context) error { _, err := native.CatalogSnapshot(); return err }},
				controllerContextRead{"legacy changes", func(context.Context) error { _, err := native.CatalogChangesSince(view.Cursor, 10); return err }},
				controllerContextRead{"legacy dependents", func(context.Context) error {
					_, _, err := native.CatalogDependents(CatalogKey{Kind: "Monitor", ID: m.ID}, 100)
					return err
				}},
				controllerContextRead{"legacy operations", func(context.Context) error { _, err := native.PendingOperations(); return err }},
			)
			for _, check := range checks {
				if err := check.read(context.Background()); !errors.Is(err, scenario.want) || IsLeadershipUnavailable(err) {
					t.Fatalf("%s: got %v, want %v", check.name, err, scenario.want)
				}
			}
		})
	}
}

func TestCatalogContextSnapshotCancellationDoesNotPublishPartialIndex(t *testing.T) {
	s := openCatalogMemory(t)
	key := CatalogKey{Kind: "Credential", ID: "shared"}
	createCatalog(t, s, catalogRecord(t, s, key.Kind, key.ID, "shared-uid", "shared-v1", "ciphertext"))
	for i := range 12 {
		id := fmt.Sprintf("dependent-%02d", i)
		createCatalog(t, s, catalogRecord(t, s, "Monitor", id, "uid-"+id, "version-"+id, "{}", key))
	}
	before, err := s.CatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	want, version, err := s.CatalogDependents(key, 100)
	if err != nil || len(want) != 12 {
		t.Fatal("invalid dependency fixture", len(want), err)
	}
	s.fsm.mu.Lock()
	s.fsm.catalog, s.fsm.catalogIncoming = nil, nil
	s.fsm.mu.Unlock()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &controllerReadCancelContext{Context: parent, cancel: cancel, remaining: 8}
	if _, err := s.CatalogSnapshotContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("catalog index initialization ignored cancellation", err)
	}
	s.fsm.mu.RLock()
	partial := s.fsm.catalog != nil || s.fsm.catalogIncoming != nil
	s.fsm.mu.RUnlock()
	if partial {
		t.Fatal("canceled initialization published a partial dependency graph")
	}
	after, err := s.CatalogSnapshotContext(context.Background())
	if err != nil || after.Len() != before.Len() || after.Cursor != before.Cursor {
		t.Fatal("retry lost catalog snapshot identity", err)
	}
	got, gotVersion, err := s.CatalogDependentsContext(context.Background(), key, 100)
	if err != nil || gotVersion != version || !reflect.DeepEqual(got, want) {
		t.Fatal("retry lost reverse dependencies", got, err)
	}
}
