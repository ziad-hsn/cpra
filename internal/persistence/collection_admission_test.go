package persistence

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func ticketCreateFixture(t *testing.T, s *Store) CollectionCommand {
	t.Helper()
	c := collectionCreateFixture(t, s, 1)
	r := collectionCommand(t, s, CollectionCommand{Action: "epoch", Epoch: uuid.NewString()}, c.Create.CreatedAt)
	if r.Err != nil || !validOperationEpoch(r.CollectionEpoch) {
		t.Fatal("initialize ticket epoch", r.Err)
	}
	c.Epoch = r.CollectionEpoch
	c.Create.Admission = &CollectionAdmissionProof{Epoch: r.CollectionEpoch, RequestDigest: strings.Repeat("c", 64), ExpiresAt: c.Create.CreatedAt.Add(time.Hour)}
	return c
}

func ticketRetry(t *testing.T, s *Store, c CollectionCommand, at time.Time) CollectionCommand {
	t.Helper()
	copy := c.Create.Clone()
	c.Create = &copy
	c.Create.CreatedAt, c.Create.ActivityAt = at, at
	c.Create.ExpiresAt = at.Add(CollectionInactivityLifetime)
	var err error
	c.Create.Secret, err = catalogSealer(t).Seal(context.Background(), c.Create.Binding(s.nodeID), []byte("private-inventory-key-and-source-fingerprint"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCollectionAdmissionEpochDoesNotAllocate(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	first := collectionCommand(t, s, CollectionCommand{Action: "epoch", Epoch: uuid.NewString()}, at)
	second := collectionCommand(t, s, CollectionCommand{Action: "epoch", Epoch: uuid.NewString()}, at.Add(time.Second))
	if first.Err != nil || second.Err != nil || !first.Allowed || first.CollectionEpoch == "" || first.CollectionEpoch != second.CollectionEpoch ||
		first.CollectionID != "" || first.Collection != nil || s.fsm.image.OperationHighWater != 0 || len(s.fsm.image.Collections) != 0 || s.fsm.collections != nil {
		t.Fatal("epoch allocated collection state or changed identity")
	}
	snap, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Release()
	sink := &collectionTestSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil || ledger != nil || i.OperationEpoch != first.CollectionEpoch || i.OperationHighWater != 0 {
		t.Fatal("epoch-only snapshot requires a collection ledger", err)
	}
	for _, c := range []CollectionCommand{{Action: "epoch"}, {Action: "epoch", Epoch: uuid.NewString(), UploadID: uuid.NewString()}, {Action: "epoch", Epoch: uuid.NewString(), Create: &CollectionState{}}} {
		if !errors.Is(c.validate(at), ErrCollectionInvalid) {
			t.Fatal("invalid epoch command accepted")
		}
	}
}

func TestCollectionAdmissionReconcilesRandomEnvelopesAndCleanedHeaders(t *testing.T) {
	s := openCatalogMemory(t)
	c := ticketCreateFixture(t, s)
	first := collectionCommand(t, s, c, c.Create.CreatedAt)
	if first.Err != nil || first.Collection == nil || first.CollectionID != first.Collection.ID {
		t.Fatal("ticket create", first.Err)
	}
	head := first.Collection.Clone()
	retry := ticketRetry(t, s, c, head.CreatedAt.Add(time.Second))
	retry.Create.MaxEncodedBytes++ // New policy must not overwrite admitted quota.
	second := collectionCommand(t, s, retry, retry.Create.CreatedAt)
	if second.Err != nil || second.CollectionID != first.CollectionID || !reflect.DeepEqual(*second.Collection, head) || s.fsm.image.OperationHighWater != 1 {
		t.Fatal("retry changed original admission", second.Err)
	}
	// Both caller input and returned metadata are detached from committed proof.
	originalDigest := c.Create.Admission.RequestDigest
	c.Create.Admission.RequestDigest = strings.Repeat("d", 64)
	second.Collection.Admission.RequestDigest = strings.Repeat("e", 64)
	current, _, _ := s.CollectionGet(head.ID)
	if current.Admission.RequestDigest != originalDigest {
		t.Fatal("admission proof aliases caller or response")
	}
	c.Create.Admission.RequestDigest = originalDigest
	cancel := collectionCancelFixture(head, head.CreatedAt.Add(2*time.Second))
	if r := collectionCommand(t, s, cancel, cancel.Cancel.At); r.Err != nil {
		t.Fatal(r.Err)
	}
	if r := collectionCommand(t, s, retry, retry.Create.CreatedAt); r.Err != nil || r.Collection.Phase != "canceled" || len(r.Events) != 0 {
		t.Fatal("terminal retry reopened input", r.Err)
	}
	if err := s.maintainCollections(cancel.Cancel.At); err != nil {
		t.Fatal(err)
	}
	result := collectionCommand(t, s, retry, retry.Create.CreatedAt)
	if result.Err != nil || !result.Allowed || result.Collection != nil || result.CollectionID != head.ID || s.fsm.image.OperationHighWater != 1 || len(s.fsm.image.Collections) != 0 {
		t.Fatal("cleaned header lost consumed-ticket identity", result.Err)
	}
	if len(s.fsm.image.CollectionAdmissions) != 1 {
		t.Fatal("cleanup removed live ticket proof")
	}
	legacy := ticketRetry(t, s, c, head.CreatedAt.Add(3*time.Second))
	legacy.Create.Admission = nil
	if r := collectionCommand(t, s, legacy, legacy.Create.CreatedAt); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("omitting proof reused a consumed ticket", r.Err)
	}
}

func TestCollectionAdmissionAtomicSameLogAndMismatch(t *testing.T) {
	s := openCatalogMemory(t)
	c := ticketCreateFixture(t, s)
	retry := ticketRetry(t, s, c, c.Create.CreatedAt.Add(time.Second))
	results := submit(t, s, Command{Kind: "collection", At: c.Create.CreatedAt, Collection: &c}, Command{Kind: "collection", At: retry.Create.CreatedAt, Collection: &retry})
	if len(results) != 2 || results[0].Err != nil || results[1].Err != nil || results[0].CollectionID != results[1].CollectionID || s.fsm.image.OperationHighWater != 1 {
		t.Fatal("same log allocated duplicate tickets")
	}
	for _, tc := range []struct {
		change func(*CollectionState)
		want   error
	}{
		{func(h *CollectionState) { h.Actor = "another/team" }, ErrCollectionConflict},
		{func(h *CollectionState) { h.ContentDigest = strings.Repeat("d", 64) }, ErrCollectionConflict},
		{func(h *CollectionState) { h.ItemCount++ }, ErrCollectionConflict},
		{func(h *CollectionState) { h.Admission.RequestDigest = strings.Repeat("e", 64) }, ErrCollectionConflict},
		{func(h *CollectionState) { h.Admission.ExpiresAt = h.Admission.ExpiresAt.Add(time.Second) }, ErrCollectionConflict},
		{func(h *CollectionState) { h.Admission.Epoch = uuid.NewString() }, ErrOperationExpired},
	} {
		changed := ticketRetry(t, s, c, c.Create.CreatedAt.Add(2*time.Second))
		tc.change(changed.Create)
		if r := collectionCommand(t, s, changed, changed.Create.CreatedAt); !errors.Is(r.Err, tc.want) || r.CollectionID != "" {
			t.Fatal("mismatched proof accepted", r.Err)
		}
	}
	expired := ticketRetry(t, s, c, c.Create.Admission.ExpiresAt)
	if r := collectionCommand(t, s, expired, expired.Create.CreatedAt); !errors.Is(r.Err, ErrOperationExpired) {
		t.Fatal("ticket expiry was not atomic", r.Err)
	}
	if s.fsm.image.OperationHighWater != 1 {
		t.Fatal("rejected proof allocated a handle")
	}
}

func TestCollectionAdmissionBoundedPruningCannotResurrectOldObservation(t *testing.T) {
	s := openCatalogMemory(t)
	c := ticketCreateFixture(t, s)
	head := collectionCommand(t, s, c, c.Create.CreatedAt).Collection.Clone()
	s.fsm.mu.Lock()
	seed := s.fsm.image.CollectionAdmissions[head.UploadID]
	for n := 2; n <= maxCollectionAdmissions; n++ {
		entry := seed
		entry.UploadID, entry.OperationID = uuid.NewString(), operationHandle(seed.Epoch, uint64(n))
		s.fsm.image.CollectionAdmissions[entry.UploadID] = entry
	}
	s.fsm.image.OperationHighWater = maxCollectionAdmissions
	err := validateCollectionHeaders(s.fsm.image)
	s.fsm.mu.Unlock()
	if err != nil {
		t.Fatal("invalid capacity fixture", err)
	}
	newRequest := ticketRetry(t, s, c, head.CreatedAt.Add(time.Second))
	newRequest.Create.UploadID = uuid.NewString()
	if r := collectionCommand(t, s, newRequest, newRequest.Create.CreatedAt); !errors.Is(r.Err, ErrCollectionQuota) {
		t.Fatal("consumed-ticket quota bypassed", r.Err)
	}
	if r := collectionCommand(t, s, c, c.Create.CreatedAt); r.Err != nil || r.CollectionID != head.ID {
		t.Fatal("full ticket map rejected original reconciliation", r.Err)
	}
	at := seed.ExpiresAt
	newRequest = ticketRetry(t, s, newRequest, at)
	newRequest.Create.Admission.ExpiresAt = at.Add(time.Hour)
	if r := collectionCommand(t, s, newRequest, at); r.Err != nil || r.CollectionID == head.ID {
		t.Fatal("expired consumption records did not release capacity", r.Err)
	}
	if len(s.fsm.image.CollectionAdmissions) != 1 || !s.fsm.image.CollectionAdmissionWatermark.Equal(at) {
		t.Fatal("pruning did not advance bounded replay state")
	}
	if r := collectionCommand(t, s, c, c.Create.CreatedAt); !errors.Is(r.Err, ErrOperationExpired) {
		t.Fatal("old observation recreated a pruned ticket", r.Err)
	}
	if err := validateCollectionHeaders(s.fsm.image); err != nil {
		t.Fatal("pruning left invalid snapshot metadata", err)
	}
}

func TestCollectionAdmissionRestartAfterHeaderCleanup(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(map[bool]string{false: "log", true: "snapshot"}[snapshot], func(t *testing.T) {
			config := testConfig(t)
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			c := ticketCreateFixture(t, s)
			head := collectionCommand(t, s, c, c.Create.CreatedAt).Collection.Clone()
			cancel := collectionCancelFixture(head, head.CreatedAt.Add(time.Second))
			if r := collectionCommand(t, s, cancel, cancel.Cancel.At); r.Err != nil {
				t.Fatal(r.Err)
			}
			if err := s.maintainCollections(cancel.Cancel.At); err != nil {
				t.Fatal(err)
			}
			if snapshot {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			retry := ticketRetry(t, s, c, head.CreatedAt.Add(2*time.Second))
			result := collectionCommand(t, s, retry, retry.Create.CreatedAt)
			if result.Err != nil || result.CollectionID != head.ID || result.Collection != nil || s.fsm.image.OperationHighWater != 1 {
				t.Fatal("restart recreated consumed ticket", result.Err)
			}
			if err := s.Snapshot(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCollectionAdmissionSnapshotValidationAndFrozenMetadata(t *testing.T) {
	s := openCatalogMemory(t)
	c := ticketCreateFixture(t, s)
	head := collectionCommand(t, s, c, c.Create.CreatedAt).Collection.Clone()
	snap, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Release()
	frozen := snap.(*frozenSnapshot)
	for _, corrupt := range []func(*image){
		func(i *image) { delete(i.CollectionAdmissions, head.UploadID) },
		func(i *image) {
			a := i.CollectionAdmissions[head.UploadID]
			a.Actor = "other"
			i.CollectionAdmissions[head.UploadID] = a
		},
		func(i *image) { i.CollectionAdmissionWatermark = head.Admission.ExpiresAt },
		func(i *image) {
			a := i.CollectionAdmissions[head.UploadID]
			a.OperationID = operationHandle(a.Epoch, 2)
			i.CollectionAdmissions[head.UploadID] = a
		},
		func(i *image) {
			a := i.CollectionAdmissions[head.UploadID]
			a.Epoch = uuid.NewString()
			i.CollectionAdmissions[head.UploadID] = a
		},
		func(i *image) {
			a := i.CollectionAdmissions[head.UploadID]
			a.ExpiresAt = a.CreatedAt.Add(25 * time.Hour)
			i.CollectionAdmissions[head.UploadID] = a
		},
	} {
		i := frozen.image
		i.CollectionAdmissions = make(map[string]CollectionAdmission)
		for key, value := range frozen.image.CollectionAdmissions {
			i.CollectionAdmissions[key] = value
		}
		corrupt(&i)
		if !errors.Is(validateCollectionHeaders(i), ErrCollectionInvalid) {
			t.Fatal("corrupt ticket state accepted")
		}
	}
	s.fsm.mu.Lock()
	delete(s.fsm.image.CollectionAdmissions, head.UploadID)
	s.fsm.mu.Unlock()
	if err := validateCollectionHeaders(frozen.image); err != nil || len(frozen.image.CollectionAdmissions) != 1 {
		t.Fatal("snapshot aliases admission map", err)
	}
}

func TestCollectionAdmissionExplicitRestoreFencesOriginalTicket(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c := ticketCreateFixture(t, s)
	head := collectionCommand(t, s, c, c.Create.CreatedAt).Collection.Clone()
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
	s.fsm.mu.Lock()
	result, done := s.fsm.admitCollection(*c.Create, c.Create.CreatedAt)
	epoch, records := s.fsm.image.OperationEpoch, len(s.fsm.image.CollectionAdmissions)
	validation := validateCollectionHeaders(s.fsm.image)
	s.fsm.mu.Unlock()
	if !done || !errors.Is(result.Err, ErrOperationExpired) || epoch == c.Create.Admission.Epoch || records != 0 || validation != nil {
		t.Fatal("restore did not fence original creation proof", result.Err, validation)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal("restored ticket snapshot", err)
	}
	if _, _, err := s.CollectionGet(head.ID); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("old handle remained current", err)
	}
}
