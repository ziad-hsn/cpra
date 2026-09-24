package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func collectionCancelFixture(head CollectionState, at time.Time) CollectionCommand {
	return CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID,
		Cancel: &CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}}
}

func TestCollectionCancelFencesReadsAndRetiresBoundedCiphertext(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			s, head, view := collectionReadFixture(t, disk, 600)
			before, err := s.fsm.collections.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer before.Close()
			// This test owns each explicit cleanup page. Keep its cancellation
			// observation ahead of the wall-clock maintenance loop, which may
			// otherwise legitimately retire another page during a slow disk run.
			c := collectionCancelFixture(head, head.ActivityAt.Add(time.Hour))
			r := collectionCommand(t, s, c, c.Cancel.At)
			if r.Err != nil || !r.Allowed || r.Collection == nil || r.Collection.Phase != "canceled" || len(r.Events) != 1 {
				t.Fatal("cancel inactive input", r.Err)
			}
			want := head.Clone()
			want.Phase, want.Cancellation, want.TerminalAt = "canceled", c.Cancel, c.Cancel.At
			if !reflect.DeepEqual(*r.Collection, want) {
				t.Fatal("cancel rewrote original inventory or activity")
			}
			if used, err := s.fsm.collections.Bytes(); err != nil || used != head.EncodedBytes {
				t.Fatal("cancel eagerly deleted input", used, err)
			}
			collectionReadAssertions(t, view, c.Cancel.At, ErrOperationExpired)
			if _, _, err := s.CollectionValidationView(context.Background(), head.ID, c.Cancel.At); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("canceled validation view", err)
			}
			if _, err := s.CollectionPage(head.ID, 0, 1); !errors.Is(err, ErrCollectionConflict) {
				t.Fatal("canceled ciphertext page", err)
			}
			item, _, err := before.Item(head.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if got := uploadCollectionFixture(t, s, head, item, c.Cancel.At); !errors.Is(got.Err, ErrCollectionConflict) {
				t.Fatal("canceled upload resumed", got.Err)
			}
			// Neither result metadata nor the caller's command may mutate the FSM.
			r.Collection.Cancellation.ID = uuid.NewString()
			originalID := c.Cancel.ID
			c.Cancel.ID = uuid.NewString()
			stored, _, _ := s.CollectionGet(head.ID)
			if stored.Cancellation.ID != originalID {
				t.Fatal("cancellation metadata aliases durable state")
			}
			c.Cancel.ID = originalID
			if err := s.maintainCollections(c.Cancel.At.Add(-time.Nanosecond)); err != nil {
				t.Fatal(err)
			}
			stored, _, _ = s.CollectionGet(head.ID)
			if stored.RemovedRows != 0 {
				t.Fatal("cleanup predates cancellation")
			}
			for _, removed := range []uint64{256, 512, 600} {
				if err := s.maintainCollections(c.Cancel.At); err != nil {
					t.Fatal(err)
				}
				if removed < 600 {
					stored, _, _ = s.CollectionGet(head.ID)
					if stored.RemovedRows != removed || stored.Phase != "canceled" {
						t.Fatal("cleanup exceeded page or lost outcome")
					}
					retry := collectionCommand(t, s, c, c.Cancel.At)
					if retry.Err != nil || len(retry.Events) != 0 || retry.Collection.RemovedRows != removed {
						t.Fatal("exact cancel retry changed progress", retry.Err)
					}
				}
			}
			if got := collectionCommand(t, s, c, c.Cancel.At); !errors.Is(got.Err, ErrOperationExpired) {
				t.Fatal("retired handle recreated", got.Err)
			}
			if used, err := s.fsm.collections.Bytes(); err != nil || used != 0 {
				t.Fatal("quota not reclaimed", used, err)
			}
			frozen, err := before.Page(head.ID, 0, 256)
			if err != nil || len(frozen) != 256 {
				t.Fatal("cancellation mutated frozen snapshot", err)
			}
			page, err := s.History().Page("collection/"+head.ID, "", 100)
			if err != nil || len(page.Events) != 1 || page.Events[0].Type != "collection_canceled" || page.Events[0].ActionID != originalID || page.Events[0].Actor != head.Actor {
				t.Fatal("missing or duplicated cancel audit", err)
			}
			raw, _ := json.Marshal(page)
			for _, secret := range []string{"never-write-this-collection-plaintext", "private-inventory-key", "ciphertext", "wrapped_key"} {
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatal("secret in audit")
				}
			}
			catalog, _ := s.CatalogSnapshot()
			if catalog.Len() != 0 || len(s.fsm.image.Monitors) != 0 || len(s.fsm.image.Operations) != 0 || len(s.fsm.image.OperationReservations) != 0 {
				t.Fatal("cancellation admitted active effects")
			}
		})
	}
}

func TestCollectionCancelIdentityExpiryAndCommandValidation(t *testing.T) {
	s := openCatalogMemory(t)
	head := createCollectionFixture(t, s, 1)
	base := collectionCancelFixture(head, head.ActivityAt.Add(time.Second))
	for _, tc := range []struct {
		name   string
		change func(*CollectionCommand)
		want   error
	}{
		{"wrong-owner", func(c *CollectionCommand) { c.Cancel.Actor = "another/team" }, ErrCollectionConflict},
		{"wrong-upload", func(c *CollectionCommand) { c.UploadID = uuid.NewString() }, ErrCollectionConflict},
		{"old-epoch", func(c *CollectionCommand) { c.OperationID = operationHandle(uuid.NewString(), 1) }, ErrOperationExpired},
		{"unissued", func(c *CollectionCommand) { c.OperationID = operationHandle(s.fsm.image.OperationEpoch, 999) }, ErrOperationNotFound},
		{"old-time", func(c *CollectionCommand) { c.Cancel.At = head.ActivityAt.Add(-time.Second) }, ErrCollectionConflict},
		{"expired", func(c *CollectionCommand) { c.Cancel.At = head.ExpiresAt }, ErrOperationExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			copy := *base.Cancel
			c.Cancel = &copy
			tc.change(&c)
			if got := collectionCommand(t, s, c, c.Cancel.At); !errors.Is(got.Err, tc.want) {
				t.Fatal(got.Err)
			}
			stored, _, _ := s.CollectionGet(head.ID)
			if !reflect.DeepEqual(stored, head) {
				t.Fatal("rejected cancel changed input")
			}
		})
	}
	for _, change := range []func(*CollectionCommand){
		func(c *CollectionCommand) { c.Cancel = nil },
		func(c *CollectionCommand) { c.Cancel.ID = "invalid" },
		func(c *CollectionCommand) { c.Cancel.Actor = "" },
		func(c *CollectionCommand) { c.Cancel.At = time.Time{} },
		func(c *CollectionCommand) { c.Epoch = uuid.NewString() },
		func(c *CollectionCommand) { c.Item = &CollectionItem{} },
		func(c *CollectionCommand) { c.Cleanup = &CollectionCleanup{} },
		func(c *CollectionCommand) { c.Create = &head },
	} {
		c := base
		copy := *base.Cancel
		c.Cancel = &copy
		change(&c)
		if !errors.Is(c.validate(base.Cancel.At), ErrCollectionInvalid) {
			t.Fatal("invalid cancel wire accepted")
		}
	}
	if !errors.Is(base.validate(base.Cancel.At.Add(time.Second)), ErrCollectionInvalid) {
		t.Fatal("changed observation accepted")
	}
	r := collectionCommand(t, s, base, base.Cancel.At)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	for _, change := range []func(*CollectionCancellation){
		func(c *CollectionCancellation) { c.ID = uuid.NewString() },
		func(c *CollectionCancellation) { c.Actor = "another/team" },
		func(c *CollectionCancellation) { c.At = c.At.Add(time.Second) },
	} {
		c := base
		copy := *base.Cancel
		c.Cancel = &copy
		change(c.Cancel)
		if got := collectionCommand(t, s, c, c.Cancel.At); !errors.Is(got.Err, ErrCollectionConflict) {
			t.Fatal("different cancellation replay accepted", got.Err)
		}
	}
	// Persisted equal instants compare correctly across offset representations.
	retry := base
	copy := *base.Cancel
	retry.Cancel = &copy
	retry.Cancel.At = retry.Cancel.At.In(time.FixedZone("fixture", 19800))
	if got := collectionCommand(t, s, retry, retry.Cancel.At); got.Err != nil || len(got.Events) != 0 {
		t.Fatal("identical cancel instant rejected", got.Err)
	}
}

func TestCollectionCancelDoesNotRewriteTerminalOrUnavailableState(t *testing.T) {
	for _, phase := range []string{"expired", "invalidated"} {
		t.Run(phase, func(t *testing.T) {
			s := openCatalogMemory(t)
			head := createCollectionFixture(t, s, 1)
			s.fsm.mu.Lock()
			state := s.fsm.image.Collections[head.ID]
			state.Phase = phase
			s.fsm.image.Collections[head.ID] = state
			s.fsm.mu.Unlock()
			c := collectionCancelFixture(head, head.ActivityAt.Add(time.Second))
			if got := collectionCommand(t, s, c, c.Cancel.At); !errors.Is(got.Err, ErrCollectionConflict) {
				t.Fatal(got.Err)
			}
			stored, _, _ := s.CollectionGet(head.ID)
			if !reflect.DeepEqual(stored, state) {
				t.Fatal("terminal outcome changed")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		fence func(*machine)
		want  error
	}{
		{"failed", func(f *machine) { f.err = ErrCollectionUnavailable }, ErrCollectionUnavailable},
		{"bootstrap", func(f *machine) { f.image.Bootstrap = &BootstrapState{Phase: "loading"} }, ErrBootstrapPending},
		{"restore", func(f *machine) { f.image.Restore = &RestoreState{Phase: "receipts"} }, ErrAuthenticationResetRequired},
		{"authentication", func(f *machine) { f.image.Authentication = &AuthenticationState{ResetRequired: true} }, ErrAuthenticationResetRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openCatalogMemory(t)
			head := createCollectionFixture(t, s, 1)
			c := collectionCancelFixture(head, head.ActivityAt.Add(time.Second))
			s.fsm.mu.Lock()
			tc.fence(s.fsm)
			s.fsm.mu.Unlock()
			results, err := s.Submit(context.Background(), []Command{{Kind: "collection", At: c.Cancel.At, Collection: &c}})
			if err == nil && len(results) == 1 {
				err = results[0].Err
			}
			if !errors.Is(err, tc.want) {
				t.Fatal("cancel bypassed admission fence", err)
			}
			s.fsm.mu.RLock()
			got := s.fsm.image.Collections[head.ID].Clone()
			s.fsm.mu.RUnlock()
			if !reflect.DeepEqual(got, head) {
				t.Fatal("unavailable cancel changed original input")
			}
		})
	}
}

func TestCollectionCancelEmptyUploadsReleaseHeaderCapacity(t *testing.T) {
	s := openCatalogMemory(t)
	var first string
	for n := 0; n < maxCollectionOperations+1; n++ {
		head := createCollectionFixture(t, s, 1)
		if n == 0 {
			first = head.ID
		}
		if n > 0 && head.ID == first {
			t.Fatal("retired operation identity reused")
		}
		c := collectionCancelFixture(head, head.ActivityAt.Add(time.Second))
		r := collectionCommand(t, s, c, c.Cancel.At)
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		if err := s.maintainCollections(c.Cancel.At); err != nil {
			t.Fatal(err)
		}
		if len(s.fsm.image.Collections) != 0 {
			t.Fatal("terminal audit retained header quota")
		}
	}
	if s.fsm.image.OperationHighWater != uint64(maxCollectionOperations+1) {
		t.Fatal("issued-handle high water changed")
	}
	page, err := s.History().Page("collection/"+first, "", 100)
	if err != nil || len(page.Events) != 1 {
		t.Fatal("header retirement erased audit", err)
	}
}

func TestCollectionCancelHeaderRejectsContradictoryMetadata(t *testing.T) {
	s := openCatalogMemory(t)
	head := createCollectionFixture(t, s, 1)
	c := collectionCancelFixture(head, head.ActivityAt.Add(time.Second))
	head.Phase, head.Cancellation = "canceled", c.Cancel
	if err := head.validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*CollectionState){
		func(h *CollectionState) { h.Cancellation = nil },
		func(h *CollectionState) { h.Phase = "uploading" },
		func(h *CollectionState) { h.Phase = "expired" },
		func(h *CollectionState) { h.Cancellation.Actor = "other" },
		func(h *CollectionState) { h.Cancellation.ID = "not-a-uuid" },
		func(h *CollectionState) { h.Cancellation.At = h.ActivityAt.Add(-time.Nanosecond) },
		func(h *CollectionState) { h.Cancellation.At = h.ExpiresAt },
	} {
		copy := head.Clone()
		change(&copy)
		if !errors.Is(copy.validate(), ErrCollectionInvalid) {
			t.Fatal("contradictory cancellation metadata accepted")
		}
	}
}

func TestCollectionCancelOrderedWithUploadAndIdenticalRetry(t *testing.T) {
	s := openCatalogMemory(t)
	head := createCollectionFixture(t, s, 2)
	at := head.ActivityAt.Add(time.Second)
	item := collectionItemFixture(t, s, head, 1, "original")
	cancel := collectionCancelFixture(head, at)
	upload := Command{Kind: "collection", At: at, Collection: &CollectionCommand{
		Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item}}
	stop := Command{Kind: "collection", At: at, Collection: &cancel}
	results, err := s.Submit(context.Background(), []Command{upload, stop, stop, upload})
	if err != nil || len(results) != 4 {
		t.Fatal(err)
	}
	if results[0].Err != nil || results[1].Err != nil || results[2].Err != nil || !errors.Is(results[3].Err, ErrCollectionConflict) {
		t.Fatal("ordered cancellation boundary", results)
	}
	if len(results[1].Events) != 1 || len(results[2].Events) != 0 {
		t.Fatal("same-log retry repeated cancellation")
	}
	stored, _, _ := s.CollectionGet(head.ID)
	if stored.Uploaded != 1 || stored.Phase != "canceled" {
		t.Fatal("cancel rolled back accepted prefix")
	}
	page, err := s.History().Page("collection/"+head.ID, "", 100)
	if err != nil || len(page.Events) != 1 {
		t.Fatal("same-log audit", err)
	}
}

func TestCollectionCancelSnapshotAndLogRestart(t *testing.T) {
	for _, position := range []string{"log-only", "before-cancel", "after-cancel"} {
		t.Run(position, func(t *testing.T) {
			config := testConfig(t)
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			head := collectionFill(t, s, 300)
			// Future deterministic times prevent the background maintenance loop
			// from retiring this input before the explicit replay boundary checks.
			c := collectionCancelFixture(head, head.ActivityAt.Add(time.Hour))
			if position == "before-cancel" {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if got := collectionCommand(t, s, c, c.Cancel.At); got.Err != nil {
				t.Fatal(got.Err)
			}
			if position == "after-cancel" {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			current, _, _ := s.CollectionGet(head.ID)
			partial := collectionCommand(t, s, cleanupCommand(current), c.Cancel.At)
			if partial.Err != nil || partial.Collection.RemovedRows != 256 {
				t.Fatal(partial.Err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			lock, err := LockOffline(config.Storage.Directory)
			if err != nil {
				t.Fatal("stopped validation", err)
			}
			if err = lock.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(context.Background(), config)
			if err != nil {
				t.Fatal("replay", err)
			}
			recovered, ok, err := s.CollectionGet(head.ID)
			if err != nil || !ok || recovered.Phase != "canceled" || recovered.RemovedRows != 256 || !reflect.DeepEqual(recovered.Cancellation, c.Cancel) {
				t.Fatal("cancel replay changed inventory", err)
			}
			if got := collectionCommand(t, s, c, c.Cancel.At); got.Err != nil || len(got.Events) != 0 {
				t.Fatal("replayed cancel duplicated audit", got.Err)
			}
			page, err := s.History().Page("collection/"+head.ID, "", 100)
			if err != nil || len(page.Events) != 1 {
				t.Fatal("history replay", err)
			}
			if got := collectionCommand(t, s, cleanupCommand(recovered), c.Cancel.At); got.Err != nil {
				t.Fatal(got.Err)
			}
			if err := s.History().Expire(c.Cancel.At.AddDate(0, 0, 29)); err != nil {
				t.Fatal(err)
			}
			page, err = s.History().Page("collection/"+head.ID, "", 100)
			if err != nil || len(page.Events) != 1 {
				t.Fatal("audit lost before retention", err)
			}
			if err := s.History().Expire(c.Cancel.At.AddDate(0, 0, 31)); err != nil {
				t.Fatal(err)
			}
			page, err = s.History().Page("collection/"+head.ID, "", 100)
			if err != nil || len(page.Events) != 0 {
				t.Fatal("audit outlived retention", err)
			}
		})
	}
}

func TestCollectionCancelRestorePreservesOutcomeAndInvalidatesHandle(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 2)
	c := collectionCancelFixture(head, head.ActivityAt.Add(time.Second))
	if got := collectionCommand(t, s, c, c.Cancel.At); got.Err != nil {
		t.Fatal(got.Err)
	}
	// The actual restore receipt phase must preserve terminal evidence while
	// every old operation handle becomes unusable under the replacement epoch.
	s.fsm.mu.Lock()
	s.fsm.image.OperationEpoch = uuid.NewString()
	s.fsm.image.OperationHighWater = 0
	state := RestoreState{Phase: "receipts"}
	r := s.fsm.restoreReceipts(&state)
	preserved := s.fsm.image.Collections[head.ID].Clone()
	s.fsm.mu.Unlock()
	if r.Err != nil || preserved.Phase != "canceled" || !reflect.DeepEqual(preserved.Cancellation, c.Cancel) {
		t.Fatal("restore overwrote cancellation", r.Err)
	}
	if got := collectionCommand(t, s, c, c.Cancel.At); !errors.Is(got.Err, ErrOperationExpired) {
		t.Fatal("restore resumed canceled handle", got.Err)
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	sink := &collectionTestSink{}
	if err := snapshot.Persist(sink); err != nil {
		t.Fatal(err)
	}
	image, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal("terminal old epoch snapshot", err)
	}
	defer ledger.Close()
	if image.Collections[head.ID].Phase != "canceled" {
		t.Fatal("snapshot lost cancellation")
	}
	if got := collectionCommand(t, s, cleanupCommand(preserved), c.Cancel.At); got.Err != nil {
		t.Fatal("restored canceled input not reclaimable", got.Err)
	}
}

func TestCollectionCancelExplicitRestoreKeepsTerminalOutcome(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	head := collectionFill(t, s, 2)
	c := collectionCancelFixture(head, head.ActivityAt.Add(time.Hour))
	if got := collectionCommand(t, s, c, c.Cancel.At); got.Err != nil {
		t.Fatal(got.Err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MarkRestored(config.Storage.Directory, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	s, err = OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	s.fsm.mu.RLock()
	stored := s.fsm.image.Collections[head.ID].Clone()
	epoch := s.fsm.image.OperationEpoch
	s.fsm.mu.RUnlock()
	oldEpoch, _, _ := ParseOperationHandle(head.ID)
	if epoch == oldEpoch || stored.Phase != "canceled" || !reflect.DeepEqual(stored.Cancellation, c.Cancel) {
		t.Fatal("explicit restore changed cancellation outcome")
	}
	if _, err := s.Submit(context.Background(), []Command{{Kind: "collection", At: c.Cancel.At, Collection: &c}}); !errors.Is(err, ErrAuthenticationAdminRequired) {
		t.Fatal("administrative restore admitted cancellation", err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal("terminal restored snapshot", err)
	}
	page, err := s.History().Page("collection/"+head.ID, "", 100)
	if err != nil || len(page.Events) != 1 || page.Events[0].Type != "collection_canceled" {
		t.Fatal("restore duplicated or replaced cancel evidence", err)
	}
}
