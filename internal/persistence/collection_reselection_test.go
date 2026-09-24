package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func reselectionCreate(t *testing.T, s *Store, profile string, count uint64) CollectionState {
	t.Helper()
	if s.fsm.image.Authentication == nil {
		if _, err := s.CommitAuthentication(t.Context(), authenticationBootstrap()); err != nil {
			t.Fatal(err)
		}
	}
	c := ticketCreateFixture(t, s)
	c.Create.Actor, c.Create.ItemCount, c.Create.NormalizationProfile = "oncall", count, profile
	owner, err := s.ObserveCollectionOwner(t.Context(), c.Create.Actor, c.Create.CreatedAt)
	if err != nil || owner == nil {
		t.Fatal("owner", err)
	}
	c.Create.Owner = owner
	c.Create.Secret, err = catalogSealer(t).Seal(t.Context(), c.Create.Binding(s.nodeID), []byte("original-private-inventory"))
	if err != nil {
		t.Fatal(err)
	}
	return validationApplyAllowed(t, collectionCommand(t, s, c, c.Create.CreatedAt))
}

func reselectionUpload(t *testing.T, s *Store, head CollectionState, id string) CollectionCommand {
	t.Helper()
	item := collectionItemFixture(t, s, head, head.Uploaded+1, id)
	authority, err := s.ObserveOperatorAuthority(t.Context(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	return CollectionCommand{Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item,
		UploadFence: &CollectionUploadFence{Uploaded: head.Uploaded, EncodedBytes: head.EncodedBytes,
			ProgressDigest: head.ProgressDigest, Authority: authority}}
}

func TestCollectionReselectionProfileAdmissionBindingAndLegacyIdentity(t *testing.T) {
	s := openCatalogMemory(t)
	c := ticketCreateFixture(t, s)
	legacy := c.Create.Clone()
	if legacy.Binding(s.nodeID).Revision != legacy.ContentDigest {
		t.Fatal("legacy envelope binding changed")
	}
	raw, err := json.Marshal(c)
	if err != nil || bytes.Contains(raw, []byte("normalization_profile")) || bytes.Contains(raw, []byte("upload_fence")) {
		t.Fatal("omitted legacy command fields changed serialization", err)
	}
	c.Create.NormalizationProfile = "cpra.file.base.v1"
	c.Create.Secret, err = catalogSealer(t).Seal(t.Context(), c.Create.Binding(s.nodeID), []byte("original-private-identity"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogSealer(t).Open(t.Context(), legacy.Binding(s.nodeID), c.Create.Secret); err == nil {
		t.Fatal("profile-bound ciphertext opened under legacy identity")
	}
	head := validationApplyAllowed(t, collectionCommand(t, s, c, c.Create.CreatedAt))
	if s.fsm.image.Version != CollectionReselectionFormatVersion || collectionAdmissionFor(head).NormalizationProfile != head.NormalizationProfile || collectionReceiptFor(head).NormalizationProfile != head.NormalizationProfile {
		t.Fatal("profile not retained consistently")
	}
	retry := ticketRetry(t, s, c, head.ActivityAt.Add(time.Second))
	if r := collectionCommand(t, s, retry, retry.Create.CreatedAt); r.Err != nil || r.CollectionID != head.ID {
		t.Fatal("identical profile retry lost handle", r.Err)
	}
	retry.Create.NormalizationProfile = ""
	if r := collectionCommand(t, s, retry, retry.Create.CreatedAt); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("consumed ticket accepted substituted profile", r.Err)
	}
	if s.fsm.image.OperationHighWater != 1 {
		t.Fatal("retry allocated an operation")
	}
}

func TestCollectionReselectionHistoricalFormatAndStrictFenceFields(t *testing.T) {
	s := openCatalogMemory(t)
	head := reselectionCreate(t, s, "cpra.file.base.v1", 2)
	upload := reselectionUpload(t, s, head, "first")
	create := collectionCreateFixture(t, s, 1)
	create.Create.NormalizationProfile = "cpra.file.base.v1"
	for _, c := range []CollectionCommand{create, upload} {
		at := head.ActivityAt.Add(time.Second)
		if c.Create != nil {
			at = c.Create.CreatedAt
		}
		command := Command{Kind: "collection", At: at, Collection: &c}
		if commandWriteFormat(command) != CollectionReselectionFormatVersion {
			t.Fatal("new contract selected an older writer")
		}
		for version := 1; version <= LatestFormatVersion+1; version++ {
			raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
			_, err := decodeEnvelope(raw)
			if (err == nil) != (version == CollectionReselectionFormatVersion || externalStorageFormat(version)) {
				t.Fatal("new fields interpreted under historical/future format", version, err)
			}
		}
	}
	for _, action := range []string{"epoch", "create", "cancel", "cleanup", "activation_admit", "plan_begin", "validation_request"} {
		bad := upload
		bad.Action = action
		if bad.validate(head.ActivityAt) == nil {
			t.Fatal("upload fence accepted by unrelated action", action)
		}
	}
	for _, change := range []func(*CollectionUploadFence){
		func(p *CollectionUploadFence) { p.EncodedBytes = -1 },
		func(p *CollectionUploadFence) { p.EncodedBytes = 1 },
		func(p *CollectionUploadFence) { p.ProgressDigest = strings.Repeat("a", 64) },
		func(p *CollectionUploadFence) { p.Uploaded = maxCollectionItems },
		func(p *CollectionUploadFence) { p.Authority.Actor = "" },
	} {
		bad := upload
		fence := *upload.UploadFence
		bad.UploadFence = &fence
		change(&fence)
		if bad.validate(head.ActivityAt) == nil {
			t.Fatal("invalid prefix accepted")
		}
	}
	create.Create.NormalizationProfile = "unsupported"
	if create.validate(create.Create.CreatedAt) == nil {
		t.Fatal("unsupported persisted normalization profile accepted")
	}
}

func TestCollectionReselectionUploadFencesAndReadReconciliation(t *testing.T) {
	s := openCatalogMemory(t)
	head := reselectionCreate(t, s, "cpra.file.base.v1", 2)
	view, _, err := s.CollectionUploadView(t.Context(), head.ID, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	first := reselectionUpload(t, s, head, "first")
	at := head.ActivityAt.Add(time.Second)
	advanced := validationApplyAllowed(t, collectionCommand(t, s, first, at))
	if err := view.Check(t.Context(), at); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("concurrent advancement did not invalidate capture", err)
	}
	if r := collectionCommand(t, s, first, at.Add(time.Second)); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("old expected prefix replay renewed upload", r.Err)
	}
	stored, _, err := s.CollectionGet(head.ID)
	if err != nil || !reflect.DeepEqual(stored, advanced) {
		t.Fatal("rejected attempt mutated original header", err)
	}
	nextView, _, err := s.CollectionUploadView(t.Context(), head.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	row, err := nextView.Item(t.Context(), 1, at)
	if err != nil || !reflect.DeepEqual(row, *first.Item) {
		t.Fatal("read reconciliation lost exact ciphertext", err)
	}
	second := reselectionUpload(t, s, advanced, "second")
	changed := *second.UploadFence
	changed.ProgressDigest = strings.Repeat("f", 64)
	second.UploadFence = &changed
	if r := collectionCommand(t, s, second, at.Add(time.Second)); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("same count but changed prefix digest accepted", r.Err)
	}
	second = reselectionUpload(t, s, advanced, "second")
	final := validationApplyAllowed(t, collectionCommand(t, s, second, at.Add(time.Second)))
	if final.Uploaded != 2 || len(s.fsm.image.Catalog) != 0 || len(s.fsm.image.Monitors) != 0 {
		t.Fatal("conditional inactive append changed active state")
	}
}

func TestCollectionReselectionUploadCurrentAuthority(t *testing.T) {
	s := openCatalogMemory(t)
	head := reselectionCreate(t, s, "", 1)
	upload := reselectionUpload(t, s, head, "first")
	policy, _ := s.Authentication()
	replace := lifecycleReplacement(policy)
	replace.At = head.ActivityAt.Add(time.Second)
	if r := lifecycleCommand(t, s, CollectionReselectionFormatVersion, replace); r.Err != nil {
		t.Fatal(r.Err)
	}
	if r := collectionCommand(t, s, upload, replace.At); !errors.Is(r.Err, ErrAuthenticationConflict) {
		t.Fatal("stale permission revision accepted", r.Err)
	}
	current, err := s.ObserveOperatorAuthority(t.Context(), head.Actor, replace.At)
	if err != nil {
		t.Fatal(err)
	}
	upload.UploadFence.Authority = current
	policy, _ = s.Authentication()
	revoke := lifecycleReplacement(policy)
	for n := range revoke.Principals {
		if revoke.Principals[n].ID == head.Actor {
			revoke.Principals[n].Revoked = true
		}
	}
	if r := lifecycleCommand(t, s, CollectionReselectionFormatVersion, revoke); r.Err != nil {
		t.Fatal(r.Err)
	}
	upload.UploadFence.Authority.Revision = revoke.Revision
	if r := collectionCommand(t, s, upload, revoke.At); !errors.Is(r.Err, ErrOperatorAuthorityDenied) {
		t.Fatal("revoked actor appended", r.Err)
	}
	stored, _, _ := s.CollectionGet(head.ID)
	if stored.Uploaded != 0 || !stored.ActivityAt.Equal(head.ActivityAt) {
		t.Fatal("failed authority check changed prefix or expiry")
	}
}

func TestCollectionReselectionFencesOriginalOwnerAndDeadline(t *testing.T) {
	for _, condition := range []string{"foreign-owner", "foreign-epoch", "expired", "canceled"} {
		t.Run(condition, func(t *testing.T) {
			s := openCatalogMemory(t)
			head := reselectionCreate(t, s, "cpra.file.base.v1", 1)
			upload := reselectionUpload(t, s, head, "first")
			at, want := head.ActivityAt.Add(time.Second), ErrOperatorAuthorityDenied
			switch condition {
			case "foreign-owner":
				upload.UploadFence.Authority.Actor = "other-operator"
			case "foreign-epoch":
				upload.UploadFence.Authority.Epoch = uuid.NewString()
			case "expired":
				at, want = head.ExpiresAt, ErrOperationExpired
			case "canceled":
				cancel := collectionCancelFixture(head, at)
				head = validationApplyAllowed(t, collectionCommand(t, s, cancel, at))
				want = ErrCollectionConflict
			}
			if r := collectionCommand(t, s, upload, at); !errors.Is(r.Err, want) {
				t.Fatal("invalid authority/lifetime accepted", r.Err)
			}
			stored, _, err := s.CollectionGet(head.ID)
			if err != nil || !reflect.DeepEqual(stored, head) {
				t.Fatal("failed fenced append changed original header", err)
			}
		})
	}
}

func TestCollectionReselectionProfileSnapshotAndRetainedReceipt(t *testing.T) {
	s := openCatalogMemory(t)
	head := reselectionCreate(t, s, "cpra.file.base.v1", 1)
	upload := reselectionUpload(t, s, head, "first")
	head = validationApplyAllowed(t, collectionCommand(t, s, upload, head.ActivityAt.Add(time.Second)))
	blob := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(blob, []byte(collectionReselectionSnapshotMagic)) {
		t.Fatal("profile snapshot has old framing")
	}
	for version := CollectionFormatVersion; version <= LatestFormatVersion; version++ {
		image := s.fsm.image
		image.Version = version
		if err := validateCollectionHeaders(image); (err == nil) != (version == CollectionReselectionFormatVersion || externalStorageFormat(version)) {
			t.Fatal("profile snapshot version gate", version, err)
		}
	}
	f := &machine{history: s.fsm.history}
	if err := f.Restore(io.NopCloser(bytes.NewReader(blob))); err != nil {
		t.Fatal("restore", err)
	}
	defer f.collections.Close()
	if !reflect.DeepEqual(f.image.Collections[head.ID], head) {
		t.Fatal("snapshot changed original profile or prefix")
	}
	at := head.ActivityAt.Add(time.Hour)
	cancel := collectionCancelFixture(head, at)
	head = validationApplyAllowed(t, collectionCommand(t, s, cancel, at))
	for n := 0; n < 3; n++ {
		r := collectionCommand(t, s, cleanupCommand(head), at)
		if r.Err != nil {
			t.Fatal("cleanup", r.Err)
		}
		if r.Collection == nil {
			break
		}
		head = r.Collection.Clone()
	}
	receipt, err := s.CollectionOperationAs(context.Background(), head.ID, head.Actor, at)
	if err != nil || receipt.NormalizationProfile != "cpra.file.base.v1" {
		t.Fatal("retained receipt lost original profile", fmt.Sprint(err))
	}
	if receipt.CancellationID != cancel.Cancel.ID || receipt.UploadID != head.UploadID {
		t.Fatal("cleanup changed original identity")
	}
}

func TestCollectionReselectionNativeReplayAndSnapshot(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot=%t", snapshot), func(t *testing.T) {
			config := testConfig(t)
			admin := openAuthenticationAdmin(t, config)
			if _, err := admin.CommitAuthentication(t.Context(), authenticationBootstrap()); err != nil {
				t.Fatal(err)
			}
			if err := admin.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			head := reselectionCreate(t, s, "cpra.file.base.v1", 2)
			command := reselectionUpload(t, s, head, "first")
			head = validationApplyAllowed(t, collectionCommand(t, s, command, head.ActivityAt.Add(time.Second)))
			if snapshot {
				if err := s.raft.Snapshot().Error(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			got, found, err := s.CollectionGet(head.ID)
			if err != nil || !found || !reflect.DeepEqual(got, head) {
				t.Fatal("restart changed captured profile/prefix", err)
			}
			if r := collectionCommand(t, s, command, head.ActivityAt.Add(time.Second)); !errors.Is(r.Err, ErrCollectionConflict) {
				t.Fatal("restart accepted stale prefix", r.Err)
			}
			next := reselectionUpload(t, s, got, "second")
			got = validationApplyAllowed(t, collectionCommand(t, s, next, head.ActivityAt.Add(time.Second)))
			if got.Uploaded != 2 || got.NormalizationProfile != head.NormalizationProfile {
				t.Fatal("fresh captured suffix failed after restart")
			}
		})
	}
}

func TestCollectionReselectionProfileRetainedAfterExecutionSourceCleanup(t *testing.T) {
	s := openCatalogMemory(t)
	head := reselectionCreate(t, s, "cpra.file.base.v1", 1)
	upload := reselectionUpload(t, s, head, "first")
	head = validationApplyAllowed(t, collectionCommand(t, s, upload, head.ActivityAt.Add(time.Second)))
	authority, err := s.ObserveOperatorAuthority(t.Context(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	head, begin, items := validationApplyIntent(t, s, head, authority, true)
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
	for !head.Validation.HistorySealed {
		head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
	}
	activation := activationCommand(head, authority, head.ActivityAt.Add(time.Second))
	head = validationApplyAllowed(t, collectionCommand(t, s, activation, activation.Activation.At))
	cancel := activationCancel(head, authority, head.Activation.At.Add(time.Second))
	head = validationApplyAllowed(t, collectionCommand(t, s, cancel, cancel.Cancel.At))
	head = executionItemFinalize(t, s, head)
	if head.ExecutionResult.Summary.NormalizationProfile != head.NormalizationProfile {
		t.Fatal("finalization dropped original normalization identity")
	}
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
	for !head.ExecutionResult.HistorySealed {
		head = validationApplyAllowed(t, executionPublishStep(t, s, head, at))
	}
	retire := executionRetireCommand(head)
	if commandWriteFormat(Command{Kind: "collection_execute", At: at, CollectionExecute: &retire}) != CollectionReselectionFormatVersion {
		t.Fatal("profile-bearing retirement selected an older format")
	}
	head = validationApplyAllowed(t, executeStoreCommand(t, s, retire, at))
	for n := 0; n < 6 && sourceHeaderPresent(t, s, head.ID); n++ {
		command := sourceRetirementCommand(head)
		c := Command{Kind: "collection_execute", At: at, CollectionExecute: &command}
		if commandWriteFormat(c) != CollectionReselectionFormatVersion {
			t.Fatal("profile-bearing source retirement selected an older format")
		}
		for version := CollectionExecutionSourceRetirementFormatVersion; version <= CollectionReselectionFormatVersion; version++ {
			raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{c}})
			_, err := decodeEnvelope(raw)
			if (err == nil) != (version == CollectionReselectionFormatVersion) {
				t.Fatal("profile-bearing retained summary crossed format gate", version, err)
			}
		}
		head = validationApplyAllowed(t, executeStoreCommand(t, s, command, at))
		at = at.Add(time.Second)
	}
	if sourceHeaderPresent(t, s, head.ID) {
		t.Fatal("source header not retired")
	}
	r, err := s.CollectionOperationAs(t.Context(), head.ID, head.Actor, at)
	if err != nil || r.NormalizationProfile != "cpra.file.base.v1" || r.ExecutionObservation == nil || r.ExecutionObservation.Summary.NormalizationProfile != r.NormalizationProfile {
		t.Fatal("retained anchor lost profile after header retirement", err)
	}
}
