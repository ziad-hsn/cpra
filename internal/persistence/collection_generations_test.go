//go:build linux || darwin || windows

package persistence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func generationOwnerFixture(t *testing.T, directory, node string) *collectionGenerations {
	t.Helper()
	collectionGCTestPrivate(t, filepath.Dir(directory))
	owner, err := newCollectionGenerations(directory, node)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.close() })
	return owner
}

func publishGenerationFixture(t *testing.T, owner *collectionGenerations) *collectionLedger {
	t.Helper()
	ledger, err := openCollectionLedger(owner.directory, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	owner.track(ledger)
	if err := owner.publish(ledger); err != nil {
		t.Fatal(err)
	}
	return ledger
}

func retireGenerationFixtures(t *testing.T, owner *collectionGenerations, want int) {
	t.Helper()
	for step := 0; step < 100 && (len(owner.catalog.Completed) > want || owner.pending != ""); step++ {
		if err := owner.maintain(context.Background()); err != nil {
			t.Fatal(err)
		}
		if condition := owner.accounting().CleanupCondition; condition == "blocked" || condition == "unavailable" {
			t.Fatalf("cleanup %s", condition)
		}
	}
	if len(owner.catalog.Completed) != want {
		t.Fatalf("retained %d generations, want %d", len(owner.catalog.Completed), want)
	}
}

func TestCollectionGenerationOwnerRetainsCompletedPair(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "collections")
	node := uuid.NewString()
	owner := generationOwnerFixture(t, directory, node)
	var ledger *collectionLedger
	for index := 0; index < 6; index++ {
		if ledger != nil {
			if err := ledger.Close(); err != nil {
				t.Fatal(err)
			}
		}
		ledger = publishGenerationFixture(t, owner)
	}
	previous := owner.catalog.Previous
	retireGenerationFixtures(t, owner, 2)
	status := owner.accounting()
	if status.Active != 1 || status.Retained != 1 || status.Retired != 4 || status.UnlinkedBytes <= 0 {
		t.Fatalf("incorrect accounting: %+v", status)
	}
	for _, name := range []string{ledger.generation, previous} {
		if _, err := os.Stat(filepath.Join(directory, name, "ledger.db")); err != nil {
			t.Fatal("deleted retained generation", err)
		}
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	restarted := generationOwnerFixture(t, directory, node)
	_ = publishGenerationFixture(t, restarted)
	retireGenerationFixtures(t, restarted, 2)
	if restarted.accounting().Retired != 5 {
		t.Fatal("retirement accounting was lost on restart")
	}
}

func TestCollectionGenerationRetirementResumesOriginalIntent(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "collections")
	node := uuid.NewString()
	owner := generationOwnerFixture(t, directory, node)
	for index := 0; index < 4; index++ {
		ledger := publishGenerationFixture(t, owner)
		if err := ledger.Close(); err != nil {
			t.Fatal(err)
		}
	}
	intentPath := filepath.Join(directory, collectionGCIntentName)
	for step := 0; step < 100; step++ {
		if err := owner.maintain(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(intentPath); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if owner.pending == "" {
		t.Fatal("retirement intent was not admitted")
	}
	if _, err := os.Stat(intentPath); err != nil {
		t.Fatal("retirement repeatedly exhausted cooperative work budget", err)
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	restarted := generationOwnerFixture(t, directory, node)
	_ = publishGenerationFixture(t, restarted)
	retireGenerationFixtures(t, restarted, 2)
	if _, err := os.Stat(filepath.Join(directory, collectionGCIntentName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted intent was not completed")
	}
}

func TestCollectionGenerationResumesIntentBeforeScanning(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "collections")
	node := uuid.NewString()
	owner := generationOwnerFixture(t, directory, node)
	for range 3 {
		ledger := publishGenerationFixture(t, owner)
		if err := ledger.Close(); err != nil {
			t.Fatal(err)
		}
	}
	intentPath := filepath.Join(directory, collectionGCIntentName)
	for range 30 {
		if err := owner.maintain(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(intentPath); err == nil {
			break
		}
	}
	if _, err := os.Stat(intentPath); err != nil || owner.pending == "" {
		t.Fatal("fixture did not admit a retirement", err)
	}
	admitted := owner.pending
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	owner = generationOwnerFixture(t, directory, node)
	// A failed unrelated recovery can leave an incomplete generation. Its
	// discovery must not precede finishing an already admitted retirement.
	incomplete := filepath.Join(directory, "generation-"+uuid.NewString())
	if err := os.Mkdir(incomplete, 0700); err != nil {
		t.Fatal(err)
	}
	for range 30 {
		if err := owner.maintain(t.Context()); err != nil {
			t.Fatal(err)
		}
		if condition := owner.accounting().CleanupCondition; condition == "blocked" || condition == "unavailable" {
			t.Fatal("unrelated scan prevented admitted retirement", condition)
		}
		if _, err := os.Stat(intentPath); errors.Is(err, os.ErrNotExist) {
			break
		}
	}
	if _, err := os.Stat(intentPath); !errors.Is(err, os.ErrNotExist) || owner.catalog.Retired != 1 {
		t.Fatal("admitted retirement did not finish exactly once", err)
	}
	if _, err := os.Stat(filepath.Join(directory, admitted)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("admitted candidate remained", err)
	}
	for _, retained := range []string{owner.catalog.Current, owner.catalog.Previous} {
		if _, err := os.Stat(filepath.Join(directory, retained, "ledger.db")); err != nil {
			t.Fatal("retained ledger changed", err)
		}
	}
	for range 10 {
		if err := owner.maintain(t.Context()); err != nil {
			t.Fatal(err)
		}
		if owner.accounting().CleanupCondition == "blocked" {
			break
		}
	}
	if owner.accounting().CleanupCondition != "blocked" {
		t.Fatal("subsequent scan did not report the incomplete generation")
	}
	if entries, err := os.ReadDir(incomplete); err != nil || len(entries) != 0 {
		t.Fatal("incomplete generation was removed or modified", err)
	}
}

func TestCollectionRetirementPublishesCatalogBeforeRemovingIntent(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "collections")
	node := uuid.NewString()
	owner := generationOwnerFixture(t, directory, node)
	for range 3 {
		ledger := publishGenerationFixture(t, owner)
		if err := ledger.Close(); err != nil {
			t.Fatal(err)
		}
	}
	old := owner.catalog.Completed[0]
	for step := 0; step < 20; step++ {
		if err := owner.maintain(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(directory, old)); errors.Is(err, os.ErrNotExist) {
			break
		}
	}
	if _, err := os.Lstat(filepath.Join(directory, old)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("candidate was not retired to its final metadata checkpoint")
	}
	path := filepath.Join(directory, collectionGenerationCatalogName)
	saved := filepath.Join(directory, "saved-catalog.json")
	if err := os.Rename(path, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := owner.maintain(context.Background()); err == nil {
		t.Fatal("catalog replacement failure was ignored")
	}
	if _, err := os.Stat(filepath.Join(directory, collectionGCIntentName)); err != nil {
		t.Fatal("lost retirement proof before catalog publication", err)
	}
	if owner.catalog.Retired != 0 {
		t.Fatal("failed catalog publication changed retirement accounting")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, path); err != nil {
		t.Fatal(err)
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	restarted := generationOwnerFixture(t, directory, node)
	_ = publishGenerationFixture(t, restarted)
	retireGenerationFixtures(t, restarted, 2)
	if restarted.accounting().Retired != 2 {
		t.Fatal("recovery duplicated or lost retirement checkpoint")
	}
}

func TestCollectionGenerationPinsProtectConcurrentClosure(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "collections")
	owner := generationOwnerFixture(t, directory, uuid.NewString())
	old := publishGenerationFixture(t, owner)
	view, err := old.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if old.viewPins.Load() != 1 {
		t.Fatal("frozen read did not register a pin")
	}
	closing := make(chan error, 1)
	go func() { closing <- old.Close() }()
	second := publishGenerationFixture(t, owner)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	_ = publishGenerationFixture(t, owner)
	for range 4 {
		if err := owner.maintain(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, old.generation, "ledger.db")); err != nil {
		t.Fatal("deleted a pinned generation", err)
	}
	if owner.accounting().Pinned != 1 {
		t.Fatal("pin observation is missing")
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closing:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ledger closure did not finish after pin release")
	}
	retireGenerationFixtures(t, owner, 2)
	if owner.accounting().Pinned != 0 {
		t.Fatal("released pin remains active")
	}
}

func TestCollectionGenerationOwnerStaysLazyAndRejectsForeignCatalog(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "collections")
	owner := generationOwnerFixture(t, directory, uuid.NewString())
	if err := owner.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty store allocated a collection directory")
	}
	ledger := publishGenerationFixture(t, owner)
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := newCollectionGenerations(directory, uuid.NewString()); !errors.Is(err, errCollectionLedgerCorrupt) {
		t.Fatal("foreign node adopted collection metadata", err)
	}
}

func TestCollectionInitialWindowsMappingIsBounded(t *testing.T) {
	if got := collectionInitialMapping("windows", maxCollectionLedgerBytes); got != 64<<20 {
		t.Fatalf("Windows initial mapping = %d", got)
	}
	if got := collectionInitialMapping("linux", 64<<20); got != 144<<20 {
		t.Fatalf("Linux mapping changed: %d", got)
	}
}

func TestStoreCollectionGenerationsStabilizeAcrossRestarts(t *testing.T) {
	config := testConfig(t)
	collectionGCTestPrivate(t, config.Storage.Directory)
	var operation string
	for cycle := 0; cycle < 5; cycle++ {
		store, err := Open(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		if cycle == 0 {
			operation = collectionFill(t, store, 50).ID
		} else {
			items, err := store.CollectionPage(operation, 0, 100)
			if err != nil || len(items) != 50 {
				t.Fatalf("committed input did not recover: %d %v", len(items), err)
			}
		}
		// Initial lazy registration also needs its first completed publication.
		if err := store.collectionGenerations.maintain(t.Context()); err != nil {
			t.Fatal(err)
		}
		retireGenerationFixtures(t, store.collectionGenerations, min(cycle+1, 2))
		if err := store.Snapshot(); err != nil {
			t.Fatal(err)
		}
		if !store.Status().Ready {
			t.Fatal("cleanup made healthy Raft storage unavailable")
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
