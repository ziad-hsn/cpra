package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func collectionFill(t *testing.T, s *Store, count int) CollectionState {
	t.Helper()
	head := createCollectionFixture(t, s, uint64(count))
	commands := make([]Command, count)
	for n := range count {
		item := collectionItemFixture(t, s, head, uint64(n+1), fmt.Sprintf("item-%04d", n))
		commands[n] = Command{Kind: "collection", At: head.ActivityAt.Add(time.Second), Collection: &CollectionCommand{
			Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item}}
	}
	results, err := s.Submit(context.Background(), commands)
	if err != nil || len(results) != count {
		t.Fatal("fill collection", err)
	}
	for _, r := range results {
		if r.Err != nil || !r.Allowed {
			t.Fatal("fill item", r.Err)
		}
	}
	current, ok, err := s.CollectionGet(head.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	return current
}

func cleanupCommand(head CollectionState) CollectionCommand {
	progress := collectionCleanupFor(head)
	return CollectionCommand{Action: "cleanup", OperationID: head.ID, UploadID: head.UploadID, Cleanup: &progress}
}

func TestCollectionExpirationBoundedFencedAndRetired(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 600)
	cleanup := cleanupCommand(head)
	if r := collectionCommand(t, s, cleanup, head.ExpiresAt.Add(-time.Nanosecond)); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("live collection was eligible for cleanup", r.Err)
	}
	// A retry renews the deadline without changing the item inventory. The old
	// observed inactivity time must still fail its fence at the old deadline.
	page, err := s.CollectionPage(head.ID, 0, 1)
	if err != nil || len(page) != 1 {
		t.Fatal(err)
	}
	renewed := uploadCollectionFixture(t, s, head, page[0], head.ActivityAt.Add(time.Second))
	if renewed.Err != nil {
		t.Fatal(renewed.Err)
	}
	if r := collectionCommand(t, s, cleanup, head.ExpiresAt); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("stale cleanup ignored renewed inactivity", r.Err)
	}
	head = *renewed.Collection
	cleanup = cleanupCommand(head)
	first := collectionCommand(t, s, cleanup, head.ExpiresAt)
	if first.Err != nil || first.Collection == nil || first.Collection.RemovedRows != 256 || first.Collection.Phase != "expired" || len(first.Events) != 1 {
		t.Fatal("first bounded cleanup", first.Err)
	}
	if first.Collection.Uploaded != 600 || first.Collection.ProgressDigest != head.ProgressDigest {
		t.Fatal("cleanup rewrote original inventory identity")
	}
	if r := collectionCommand(t, s, cleanup, head.ExpiresAt); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("stale cleanup removed another page", r.Err)
	}
	if _, err := s.CollectionPage(head.ID, 0, 100); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("expired ciphertext remains available for activation", err)
	}
	if r := uploadCollectionFixture(t, s, head, page[0], head.ExpiresAt); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("expired input resumed", r.Err)
	}
	for _, expected := range []uint64{512, 600} {
		current, ok, err := s.CollectionGet(head.ID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		r := collectionCommand(t, s, cleanupCommand(current), head.ExpiresAt)
		if r.Err != nil || r.Collection.RemovedRows != expected || len(r.Events) != 0 {
			t.Fatal("cleanup progress or duplicate event", r.Err)
		}
	}
	if _, _, err := s.CollectionGet(head.ID); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("retired handle remains resumable", err)
	}
	if r := collectionCommand(t, s, cleanup, head.ExpiresAt); r.Err != nil || !r.Allowed || len(r.Events) != 0 {
		t.Fatal("retired cleanup recreated state", r.Err)
	}
	if used, err := s.fsm.collections.Bytes(); err != nil || used != 0 || len(s.fsm.image.Collections) != 0 {
		t.Fatal("logical collection quota not reclaimed", used, err)
	}
	audit, err := s.History().Page("collection/"+head.ID, "", 100)
	if err != nil || len(audit.Events) != 1 || audit.Events[0].Type != "collection_expired" || audit.Events[0].Actor != head.Actor {
		t.Fatal("expiration audit missing or duplicated", err)
	}
	view, _ := s.CatalogSnapshot()
	if view.Len() != 0 || len(s.fsm.image.Monitors) != 0 {
		t.Fatal("expiration changed active resources")
	}
}

func TestCollectionPartialExpirationSnapshotAndFrozenOriginal(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 300)
	before, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer before.Release()
	r := collectionCommand(t, s, cleanupCommand(head), head.ExpiresAt)
	if r.Err != nil || r.Collection.RemovedRows != 256 {
		t.Fatal(r.Err)
	}
	after, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer after.Release()
	for _, tc := range []struct {
		name      string
		snapshot  *frozenSnapshot
		remaining uint64
	}{
		{"original", before.(*frozenSnapshot), 300},
		{"partial", after.(*frozenSnapshot), 44},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &collectionTestSink{}
			if err := tc.snapshot.Persist(sink); err != nil {
				t.Fatal(err)
			}
			i, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), filepath.Join(t.TempDir(), "ledger"))
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			header := i.Collections[head.ID]
			if header.Uploaded-header.RemovedRows != tc.remaining {
				t.Fatal("snapshot changed its frozen prefix")
			}
			rows, err := ledger.Page(head.ID, 0, 500)
			if err != nil || uint64(len(rows)) != tc.remaining || rows[len(rows)-1].Ordinal != tc.remaining {
				t.Fatal("snapshot missing prefix", err)
			}
		})
	}
}

func TestCollectionMaintenanceIdleAndEmptyExpiry(t *testing.T) {
	s := openCatalogMemory(t)
	head := createCollectionFixture(t, s, 1)
	index := s.fsm.image.Index
	if err := s.maintainCollections(head.ExpiresAt.Add(-time.Second)); err != nil || s.fsm.image.Index != index {
		t.Fatal("idle maintenance submitted a write", err)
	}
	if err := s.maintainCollections(head.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CollectionGet(head.ID); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("empty expired upload was not retired", err)
	}
	index = s.fsm.image.Index
	if err := s.maintainCollections(head.ExpiresAt.Add(time.Second)); err != nil || s.fsm.image.Index != index {
		t.Fatal("completed cleanup kept submitting", err)
	}
}

func TestCollectionCleanupComparesInstantsAcrossEncodedTimeZones(t *testing.T) {
	s := openCatalogMemory(t)
	create := collectionCreateFixture(t, s, 1)
	zone := time.FixedZone("fixture", 5*3600+30*60)
	create.Create.CreatedAt = create.Create.CreatedAt.In(zone)
	create.Create.ActivityAt = create.Create.CreatedAt
	create.Create.ExpiresAt = create.Create.ActivityAt.Add(CollectionInactivityLifetime)
	r := collectionCommand(t, s, create, create.Create.CreatedAt)
	if r.Err != nil || r.Collection == nil {
		t.Fatal(r.Err)
	}
	cleanup := cleanupCommand(*r.Collection)
	cleanup.Cleanup.ActivityAt = cleanup.Cleanup.ActivityAt.UTC()
	result := collectionCommand(t, s, cleanup, r.Collection.ExpiresAt.UTC())
	if result.Err != nil || !result.Allowed {
		t.Fatal("equal persisted instants could not retire expired input", result.Err)
	}
	if _, _, err := s.CollectionGet(r.Collection.ID); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("non-UTC input remained live", err)
	}
}

func TestCollectionPartialExpirationRaftRestartAndOfflineValidation(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	head := collectionFill(t, s, 600)
	page, err := s.CollectionPage(head.ID, 0, 1)
	if err != nil || len(page) != 1 {
		t.Fatal(err)
	}
	// Keep background wall-clock maintenance from racing this explicit
	// snapshot/log boundary exercise; commands carry their own observation time.
	renewed := uploadCollectionFixture(t, s, head, page[0], head.ActivityAt.Add(time.Hour))
	if renewed.Err != nil {
		t.Fatal(renewed.Err)
	}
	head = *renewed.Collection
	first := collectionCommand(t, s, cleanupCommand(head), head.ExpiresAt)
	if first.Err != nil || first.Collection.RemovedRows != 256 {
		t.Fatal(first.Err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	second := collectionCommand(t, s, cleanupCommand(*first.Collection), head.ExpiresAt)
	if second.Err != nil || second.Collection.RemovedRows != 512 {
		t.Fatal(second.Err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := LockOffline(config.Storage.Directory)
	if err != nil {
		t.Fatal("partial expiration stopped validation", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal("partial expiration replay", err)
	}
	recovered, exists, err := s.CollectionGet(head.ID)
	if err != nil || !exists || recovered.Phase != "expired" || recovered.RemovedRows != 512 || recovered.ProgressDigest != head.ProgressDigest {
		t.Fatal("partial cleanup lost or changed its inventory", err)
	}
	remaining, err := s.fsm.collections.Page(head.ID, 0, 100)
	if err != nil || len(remaining) != 88 || remaining[87].Ordinal != 88 {
		t.Fatal("incorrect reconstructed encrypted prefix", err)
	}
	last := collectionCommand(t, s, cleanupCommand(recovered), head.ExpiresAt)
	if last.Err != nil || last.Collection.RemovedRows != 600 {
		t.Fatal(last.Err)
	}
	if _, _, err := s.CollectionGet(head.ID); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("cleaned operation survived as executable input", err)
	}
	audit, err := s.History().Page("collection/"+head.ID, "", 100)
	if err != nil || len(audit.Events) != 1 {
		t.Fatal("expiration audit changed during replay", err)
	}
}

func TestCollectionBackgroundMaintenanceExpiresAbandonedInput(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	create := collectionCreateFixture(t, s, 1)
	create.Create.CreatedAt = time.Now().UTC().Add(-25 * time.Hour)
	create.Create.ActivityAt = create.Create.CreatedAt
	create.Create.ExpiresAt = create.Create.ActivityAt.Add(CollectionInactivityLifetime)
	r := collectionCommand(t, s, create, create.Create.CreatedAt)
	if r.Err != nil || r.Collection == nil {
		t.Fatal(r.Err)
	}
	id := r.Collection.ID
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("background maintenance did not retire abandoned input")
		case <-tick.C:
			_, _, err := s.CollectionGet(id)
			if errors.Is(err, ErrOperationExpired) {
				if !s.Status().Ready {
					t.Fatal("successful cleanup made durable storage unavailable")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}
