package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

func TestCatalogChangeCursorIncludesSameCommitWritesAndRemoval(t *testing.T) {
	s := openCatalogMemory(t)
	view, err := s.CatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]Command, 0, 3)
	for i := range 3 {
		record := catalogRecord(t, s, "Credential", fmt.Sprintf("key-%d", i), fmt.Sprintf("uid-%d", i), fmt.Sprintf("rv-%d", i), "secret")
		commands = append(commands, Command{Kind: "catalog", At: record.UpdatedAt, Catalog: &CatalogMutation{Create: true, Record: record}})
	}
	for _, result := range submit(t, s, commands...) {
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	page, err := s.CatalogChangesSince(view.Cursor, 2)
	if err != nil || len(page.Changes) != 2 || !page.More || page.Changes[0].CommittedIndex != page.Changes[1].CommittedIndex {
		t.Fatal("same-commit writes lost", err)
	}
	// Result mutation cannot modify the retained ring or committed catalog.
	page.Changes[0].Key.ID = "changed"
	copy, _ := s.CatalogChangesSince(view.Cursor, 2)
	if copy.Changes[0].Key.ID != "key-0" {
		t.Fatal("change page aliases store state")
	}
	last, err := s.CatalogChangesSince(page.Next, 2)
	if err != nil || len(last.Changes) != 1 || last.More || last.Changes[0].Key.ID != "key-2" {
		t.Fatal("bounded continuation lost change", err)
	}
	record := requireCatalog(t, s, CatalogKey{Kind: "Credential", ID: "key-0"})
	if result := catalogSubmit(t, s, deleteCatalogMutation(record, "removed-version")); result.Err != nil {
		t.Fatal(result.Err)
	}
	deleted, err := s.CatalogChangesSince(last.Next, 500)
	if err != nil || len(deleted.Changes) != 1 || !deleted.Changes[0].Removed || deleted.Changes[0].UID != record.UID {
		t.Fatal("deletion lost exact tombstone incarnation", err)
	}
	if view.Len() != 0 {
		t.Fatal("capturing change cursor broke immutable snapshot")
	}
}

func TestCatalogChangesBoundLagAndRejectForeignOrRestoredCursor(t *testing.T) {
	s := openCatalogMemory(t)
	view, _ := s.CatalogSnapshot()
	// Exercise the bounded ring directly under the same lock Apply holds. This
	// is a retention test, not thousands of redundant crypto/database writes.
	s.fsm.mu.Lock()
	for i := range catalogChangeCapacity + 1 {
		s.fsm.noteCatalogChange(CatalogRecord{Key: CatalogKey{Kind: "Monitor", ID: fmt.Sprintf("m-%d", i)}, CommittedIndex: uint64(i + 1)})
	}
	s.fsm.mu.Unlock()
	if _, err := s.CatalogChangesSince(view.Cursor, 100); !errors.Is(err, ErrCatalogChangeGap) {
		t.Fatal("slow reader silently skipped changes", err)
	}
	other := openCatalogMemory(t)
	fresh, _ := s.CatalogSnapshot()
	if _, err := other.CatalogChangesSince(fresh.Cursor, 100); !errors.Is(err, ErrCatalogChangeGap) {
		t.Fatal("cursor accepted by a different store")
	}
	if _, err := json.Marshal(fresh.Cursor); err == nil {
		t.Fatal("process-local cursor could be persisted as a resume token")
	}
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CatalogChangesSince(fresh.Cursor, 100); !errors.Is(err, ErrCatalogChangeGap) {
		t.Fatal("restore failed to invalidate pre-restore cursor")
	}
	postRestore, _ := s.CatalogSnapshot()
	page, err := s.CatalogChangesSince(postRestore.Cursor, 100)
	if err != nil || len(page.Changes) != 0 || page.More {
		t.Fatal("fresh restored view could not follow changes", err)
	}
}

func TestCheckTrafficDoesNotCreateConfigurationChangeWork(t *testing.T) {
	s := openCatalogMemory(t)
	configure(t, s)
	view, _ := s.CatalogSnapshot()
	submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: time.Now().UTC(), Outcome: "success"})
	page, err := s.CatalogChangesSince(view.Cursor, 100)
	if err != nil || len(page.Changes) != 0 {
		t.Fatal("health traffic triggered configuration rebuild", err)
	}
}
