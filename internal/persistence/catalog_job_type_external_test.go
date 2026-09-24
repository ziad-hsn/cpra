//go:build externaljobs

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
	"github.com/hashicorp/raft"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func catalogJobTypeRef(v JobTypeVersion) JobTypeReference {
	return JobTypeReference{JobTypeID: v.Record.Key.ID, JobTypeUID: v.Record.UID, Version: v.Version, Revision: v.Record.Revision, Category: v.Category}
}
func catalogJobTypeDelete(t *testing.T, s *Store, state JobTypeState) JobTypeCommand {
	t.Helper()
	c := jobTypeChange(t, s, state, state.Current.Version)
	c.Action = "delete"
	c.Value.Record.Removed = true
	c.Value.Record.Payload = secureconfig.Envelope{}
	return c
}
func catalogJobTypeRecord(t *testing.T, s *Store, id string, v JobTypeVersion) CatalogRecord {
	t.Helper()
	r := catalogRecord(t, s, "Monitor", id, uuid.NewString(), uuid.NewString(), "external-parameters-private-canary")
	r.CreatedAt, r.UpdatedAt = v.Record.UpdatedAt, v.Record.UpdatedAt
	r.JobTypeReferences = []JobTypeReference{catalogJobTypeRef(v)}
	return r
}
func TestCatalogJobTypeCurrentReferencesAndRetainedVersions(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) {
			config := testConfig(t)
			if !native {
				config.Storage.Mode = "memory"
			}
			s, err := Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if s != nil {
					_ = s.Close()
				}
			}()
			original := commitJobType(t, s, jobTypeFixture(t, s, "pinned-check"))
			ref := catalogJobTypeRef(original.Current)
			source := createCatalog(t, s, catalogJobTypeRecord(t, s, "consumer", original.Current))
			if s.fsm.image.Version != CatalogJobTypeFormatVersion {
				t.Fatal("source failed to advance format")
			}
			view, err := s.CatalogSnapshotContext(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.CommitJobType(t.Context(), catalogJobTypeDelete(t, s, original), original.Current.Record.UpdatedAt.Add(time.Second)); !errors.Is(err, ErrCatalogReferenced) {
				t.Fatal("live config did not block type deletion", err)
			}
			metadata := jobTypeChange(t, s, original, "v1")
			metadata.RetainedVersionRevision = original.Versions["v1"].Record.Revision
			latest := commitJobType(t, s, metadata)
			latest = commitJobType(t, s, jobTypeChange(t, s, latest, "v2"))
			selection, ok, err := s.LookupJobTypeVersion(t.Context(), ref.JobTypeID, ref.Version)
			if err != nil || !ok || selection.CurrentRemoved || selection.CurrentUID != ref.JobTypeUID || !reflect.DeepEqual(selection.Version, original.Current) {
				t.Fatal("older immutable version lost after metadata/version update", err)
			}
			selection.Version.Record.Payload.Ciphertext[0] ^= 1
			stored, _, _ := s.LookupJobTypeVersion(t.Context(), ref.JobTypeID, ref.Version)
			if !reflect.DeepEqual(stored.Version, original.Current) {
				t.Fatal("lookup returned owned ciphertext")
			}
			if native {
				if err = s.Snapshot(); err != nil {
					t.Fatal(err)
				}
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(t.Context(), config)
				if err != nil {
					t.Fatal("native reference recovery", err)
				}
				if _, err = s.CommitJobType(t.Context(), catalogJobTypeDelete(t, s, latest), latest.Current.Record.UpdatedAt.Add(time.Second)); !errors.Is(err, ErrCatalogReferenced) {
					t.Fatal("restart lost incoming reference", err)
				}
				source = requireCatalog(t, s, source.Key)
			}
			// Replacing the source removes only its current reference. The old detached
			// catalog generation and immutable encrypted type version remain readable.
			update := updateCatalogMutation(t, s, source, uuid.NewString(), "builtin replacement")
			update.Record.JobTypeReferences = nil
			if result := catalogSubmit(t, s, update); result.Err != nil {
				t.Fatal(result.Err)
			}
			latest = commitJobType(t, s, catalogJobTypeDelete(t, s, latest))
			recreate := jobTypeFixture(t, s, ref.JobTypeID)
			recreate.Value.Version = "v3"
			recreate.Value.Record.CreatedAt = latest.Current.Record.UpdatedAt.Add(time.Second)
			recreate.Value.Record.UpdatedAt = recreate.Value.Record.CreatedAt
			latest = commitJobType(t, s, recreate)
			selected, ok, err := s.LookupJobTypeVersion(t.Context(), ref.JobTypeID, ref.Version)
			if err != nil || !ok || selected.CurrentUID == ref.JobTypeUID || !reflect.DeepEqual(selected.Version, original.Current) {
				t.Fatal("recreate erased retained version", err)
			}
			page, _, err := view.Page("Monitor", "", 100)
			if err != nil || len(page) != 1 || !reflect.DeepEqual(page[0].JobTypeReferences, []JobTypeReference{ref}) {
				t.Fatal("old detached generation lost pin", err)
			}
			stale := catalogJobTypeRecord(t, s, "new-source", original.Current)
			if result := catalogSubmit(t, s, CatalogMutation{Record: stale, Create: true}); !errors.Is(result.Err, ErrCatalogDependency) {
				t.Fatal("new source rebound old incarnation", result.Err)
			}
			if source, ok, _ := s.CatalogGet(stale.Key); ok || len(source.JobTypeReferences) != 0 {
				t.Fatal("rejected source installed")
			}
			if native {
				if err = s.Snapshot(); err != nil {
					t.Fatal(err)
				}
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(t.Context(), config)
				if err != nil {
					t.Fatal("retained old version recovery", err)
				}
			}
		})
	}
}
func TestCatalogJobTypeDeletionOrderAndSourceCAS(t *testing.T) {
	s := openCatalogMemory(t)
	job := commitJobType(t, s, jobTypeFixture(t, s, "race"))
	pending := catalogJobTypeRecord(t, s, "waiting", job.Current)
	job = commitJobType(t, s, catalogJobTypeDelete(t, s, job))
	if r := catalogSubmit(t, s, CatalogMutation{Record: pending, Create: true}); !errors.Is(r.Err, ErrCatalogDependency) {
		t.Fatal("delete-before-create accepted", r.Err)
	}
	c := jobTypeFixture(t, s, "other")
	other := commitJobType(t, s, c)
	source := createCatalog(t, s, catalogJobTypeRecord(t, s, "active", other.Current))
	stale := updateCatalogMutation(t, s, source, uuid.NewString(), "stale")
	stale.Record.JobTypeReferences = nil
	fresh := updateCatalogMutation(t, s, source, uuid.NewString(), "fresh")
	freshResult := catalogSubmit(t, s, fresh)
	if freshResult.Err != nil {
		t.Fatal(freshResult.Err)
	}
	if result := catalogSubmit(t, s, stale); !errors.Is(result.Err, ErrCatalogConflict) {
		t.Fatal("stale replacement succeeded", result.Err)
	}
	if _, err := s.CommitJobType(t.Context(), catalogJobTypeDelete(t, s, other), other.Current.Record.UpdatedAt.Add(time.Second)); !errors.Is(err, ErrCatalogReferenced) {
		t.Fatal("stale replacement removed current index", err)
	}
	deleted := deleteCatalogMutation(*freshResult.Catalog, uuid.NewString())
	deleted.Record.JobTypeReferences = nil
	if result := catalogSubmit(t, s, deleted); result.Err != nil {
		t.Fatal(result.Err)
	}
	commitJobType(t, s, catalogJobTypeDelete(t, s, other))
}
func TestCatalogJobTypeCanonicalShapeAndFormats(t *testing.T) {
	s := openCatalogMemory(t)
	job := commitJobType(t, s, jobTypeFixture(t, s, "shape"))
	r := catalogJobTypeRecord(t, s, "source", job.Current)
	refs, err := CanonicalJobTypeReferences(append([]JobTypeReference{r.JobTypeReferences[0]}, r.JobTypeReferences...))
	if err != nil || len(refs) != 1 {
		t.Fatal("dedup", err)
	}
	if _, err = CanonicalJobTypeReferences(make([]JobTypeReference, 65)); !errors.Is(err, ErrJobTypeInvalid) {
		t.Fatal("malformed references", err)
	}
	many := make([]JobTypeReference, 65)
	for n := range many {
		many[n] = refs[0]
		many[n].JobTypeID = fmt.Sprint(n)
	}
	if _, err = CanonicalJobTypeReferences(many); !errors.Is(err, ErrJobTypeQuota) {
		t.Fatal("reference bound", err)
	}
	for name, change := range map[string]func(*CatalogRecord){
		"wrong-kind":     func(r *CatalogRecord) { r.Key.Kind = "Credential" },
		"endpoint-check": func(r *CatalogRecord) { r.Key.Kind = "NotificationEndpoint" },
		"duplicate":      func(r *CatalogRecord) { r.JobTypeReferences = append(r.JobTypeReferences, r.JobTypeReferences[0]) },
		"empty-version":  func(r *CatalogRecord) { r.JobTypeReferences[0].Version = "" },
		"tombstone":      func(r *CatalogRecord) { r.Removed = true; r.Payload = secureconfig.Envelope{} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := r.Clone()
			change(&bad)
			if bad.validate() == nil {
				t.Fatal("invalid reference shape accepted")
			}
		})
	}
	command := Command{Kind: "catalog", At: r.UpdatedAt, Catalog: &CatalogMutation{Record: r, Create: true}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == CatalogJobTypeFormatVersion || version == WorkerSessionFormatVersion || version == WorkerExecutionFormatVersion || version == WorkerOfferFormatVersion) {
			t.Fatal("format gate", version, err)
		}
	}
	raw, _ := json.Marshal(command)
	bound, err := encodedBound(command)
	if err != nil || bound < len(raw) {
		t.Fatal("tagged nested budget", bound, len(raw), err)
	}
	if bytes.Contains(raw, []byte("external-parameters-private-canary")) {
		t.Fatal("plaintext in command")
	}
	if !bytes.Contains(raw, []byte(`"job_type_references"`)) {
		t.Fatal("reference missing from command")
	}
	committed := createCatalog(t, s, r)
	for _, change := range []func(*image){
		func(i *image) { i.Version = WorkerPolicyFormatVersion },
		func(i *image) {
			record := i.Catalog[r.Key.indexKey()]
			record.JobTypeReferences[0].Revision = "foreign"
			i.Catalog[r.Key.indexKey()] = record
		},
		func(i *image) {
			state := i.JobTypes.Records[job.Current.Record.Key.ID]
			delete(state.Versions, "v1")
			i.JobTypes.Records[job.Current.Record.Key.ID] = state
		},
	} {
		bad := catalogDeltaCloneMachine(t, s.fsm).image
		change(&bad)
		if validateCatalogImage(bad) == nil {
			t.Fatal("corrupt/lower-format catalog accepted")
		}
	}
	clone := committed.Clone()
	clone.JobTypeReferences[0].Version = "changed"
	if requireCatalog(t, s, r.Key).JobTypeReferences[0].Version != "v1" {
		t.Fatal("reference clone aliases")
	}
}
func TestCatalogJobTypeReservationBindsReference(t *testing.T) {
	s := openCatalogMemory(t)
	job := commitJobType(t, s, jobTypeFixture(t, s, "reserved"))
	newer := commitJobType(t, s, jobTypeChange(t, s, job, "v2"))
	r := catalogJobTypeRecord(t, s, "consumer", job.Current)
	c := Command{Kind: "catalog", At: r.UpdatedAt, Catalog: &CatalogMutation{OperationID: r.Revision, Record: r, Create: true, Actor: "oncall"}}
	original, _, err := operationDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := s.ReserveOperation(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	c.Catalog.OperationID = reservation.ID
	changed := *c.Catalog
	changed.Record = r.Clone()
	changed.Record.JobTypeReferences = []JobTypeReference{catalogJobTypeRef(newer.Current)}
	other, _, err := operationDigest(Command{Kind: "catalog", At: c.At, Catalog: &changed})
	if err != nil || original.Digest == other.Digest {
		t.Fatal("reference omitted from reservation digest", err)
	}
	result, err := s.Submit(t.Context(), []Command{{Kind: "catalog", At: c.At, Catalog: &changed}})
	if err != nil || len(result) != 1 || !errors.Is(result[0].Err, ErrOperationReservation) {
		t.Fatal("swapped valid pin consumed reservation", err, result)
	}
	result, err = s.Submit(t.Context(), []Command{c})
	if err != nil || len(result) != 1 || result[0].Err != nil || !result[0].Allowed {
		t.Fatal("original reservation lost", err, result)
	}
}
func TestCatalogJobTypeBootstrapAndLookupCancellation(t *testing.T) {
	s := openCatalogMemory(t)
	job := commitJobType(t, s, jobTypeFixture(t, s, "bootstrap-ref"))
	r := catalogJobTypeRecord(t, s, "seed", job.Current)
	digest, err := BootstrapDigest("", r)
	if err != nil {
		t.Fatal(err)
	}
	manifest := BootstrapManifest{StageID: "external-seed", InventoryDigest: strings.Repeat("a", 64), CatalogDigest: digest, Count: 1}
	for _, b := range []BootstrapCommand{{Action: "begin", StageID: manifest.StageID, Manifest: &manifest}, {Action: "seed", StageID: manifest.StageID, Ordinal: 1, Record: &r}, {Action: "activate", StageID: manifest.StageID, Manifest: &manifest}} {
		res := submit(t, s, Command{Kind: "bootstrap", At: r.UpdatedAt, Bootstrap: &b})
		if len(res) != 1 || res[0].Err != nil {
			t.Fatal("seed ref", res)
		}
	}
	if s.fsm.image.Version != CatalogJobTypeFormatVersion || !s.fsm.jobTypeCatalogReferenced(job.Current.Record.Key.ID, job.Current.Record.UID) {
		t.Fatal("bootstrap missed index or format")
	}
	s.fsm.mu.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	_, _, err = s.LookupJobTypeVersion(ctx, job.Current.Record.Key.ID, "v1")
	cancel()
	s.fsm.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lookup ignored cancellation", err)
	}
}

func TestCatalogJobTypeCollectionPreparedAndDecisionRecovery(t *testing.T) {
	for _, deleteBeforeDecision := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete-before-decision=%t", deleteBeforeDecision), func(t *testing.T) {
			s := openCatalogMemory(t)
			job := commitJobType(t, s, jobTypeFixture(t, s, "collection-type"))
			c := collectionCreateFixture(t, s, 1)
			c.Create.Actor = "oncall"
			var err error
			c.Create.Owner, err = s.ObserveCollectionOwner(t.Context(), "oncall", c.Create.ActivityAt)
			if err != nil {
				t.Fatal(err)
			}
			head := validationApplyAllowed(t, collectionCommand(t, s, c, c.Create.ActivityAt))
			item := CollectionItem{Ordinal: 1, Key: CatalogKey{Kind: "Monitor", ID: "collection-consumer"}, Source: "source.00000000000000000001", SourceDocument: 1, SourceItem: 1, ContentDigest: strings.Repeat("b", 64)}
			item.Payload, err = catalogSealer(t).Seal(t.Context(), item.Binding(s.nodeID, head.UploadID), []byte("encrypted-monitor-structural-fixture"))
			if err != nil {
				t.Fatal(err)
			}
			head = validationApplyAllowed(t, uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Millisecond)))
			authority, err := s.ObserveOperatorAuthority(t.Context(), head.Actor, head.ActivityAt)
			if err != nil {
				t.Fatal(err)
			}
			head, validation, items := validationApplyIntent(t, s, head, authority, true)
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &validation, nil))
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items))
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
			for !head.Validation.HistorySealed {
				head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
			}
			head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, authority, head.ActivityAt.Add(time.Second)), head.ActivityAt.Add(time.Second)))
			begin := executeBoundaryBegin(t, head, authority)
			head = validationApplyAllowed(t, executeStoreCommand(t, s, begin, head.Activation.At.Add(time.Second)))
			prepared := executeCandidate(t, begin, s.fsm.collectionExecutionIndex, 1, head.Execution.LastAt.Add(time.Second))
			prepared.Prepared.Record.JobTypeReferences = []JobTypeReference{catalogJobTypeRef(job.Current)}
			head = validationApplyAllowed(t, executeStoreCommand(t, s, prepared, prepared.Prepared.At))
			if s.fsm.image.Version != CatalogJobTypeFormatVersion {
				t.Fatal("preparation failed to gate format")
			}
			if deleteBeforeDecision {
				job = commitJobType(t, s, catalogJobTypeDelete(t, s, job))
			}
			// The prepared immutable version survives a deleted current descriptor;
			// recovery must retain the original candidate but grants no execution.
			blob := captureSnapshotBytes(t, s.fsm)
			restored := &machine{history: s.fsm.history}
			if err = restored.Restore(io.NopCloser(bytes.NewReader(blob))); err != nil {
				t.Fatal("prepared snapshot recovery", err)
			}
			defer restored.collections.Close()
			if validateCatalogImage(restored.image) != nil {
				t.Fatal("restored catalog invalid")
			}
			view, err := restored.collections.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			bad := catalogDeltaCloneMachine(t, restored).image
			bad.Version = WorkerPolicyFormatVersion
			_, err = validateCollectionExecutionInventory(t.Context(), bad, view)
			_ = view.Close()
			if err == nil {
				t.Fatal("lower-format prepared references accepted")
			}
			result := executeStoreCommand(t, s, executeDecision(prepared), prepared.Prepared.At.Add(time.Second))
			if result.Err != nil || !result.Allowed {
				t.Fatal("decision", result.Err)
			}
			if deleteBeforeDecision {
				if result.Operation != nil || result.Collection.Execution.Conflicts != 1 {
					t.Fatal("deleted type was rebound")
				}
			} else {
				if result.Operation == nil || result.Collection.Execution.Accepted != 1 {
					t.Fatal("pinned candidate was not committed")
				}
				if _, err = s.CommitJobType(t.Context(), catalogJobTypeDelete(t, s, job), job.Current.Record.UpdatedAt.Add(time.Second)); !errors.Is(err, ErrCatalogReferenced) {
					t.Fatal("child commit missed type reference", err)
				}
			}
		})
	}
}

func TestCatalogJobTypeRawPresenceAndNestedStrictness(t *testing.T) {
	s := openCatalogMemory(t)
	r := catalogRecord(t, s, "Monitor", "raw", "uid", "revision", "protected")
	row, _ := json.Marshal(r)
	for _, extra := range []string{`"job_type_references":null`, `"job_type_references":[]`} {
		inserted := append([]byte("{"+extra+","), row[1:]...)
		var decoded CatalogRecord
		if err := json.Unmarshal(inserted, &decoded); err != nil {
			t.Fatal(err)
		}
		if catalogRecordMinimumFormat(decoded) != CatalogJobTypeFormatVersion {
			t.Fatal("explicit empty field lost its format")
		}
		tombstone := decoded.Clone()
		tombstone.Removed = true
		tombstone.Payload = secureconfig.Envelope{}
		tombstone.JobTypeReferences = nil
		if tombstone.validate() != nil || catalogRecordMinimumFormat(tombstone) != CatalogJobTypeFormatVersion {
			t.Fatal("empty-field tombstone lost format or was rejected")
		}

		for _, command := range []Command{
			{Kind: "catalog", At: r.UpdatedAt, Catalog: &CatalogMutation{Record: decoded, Create: true}},
			{Kind: "bootstrap", At: r.UpdatedAt, Bootstrap: &BootstrapCommand{Action: "seed", StageID: "stage", Ordinal: 1, Record: &decoded}},
		} {
			// Marshal intentionally omits the empty extension, so restore exact raw
			// caller bytes before passing the envelope through the strict decoder.
			for _, version := range []int{CatalogMutationFormatVersion, WorkerPolicyFormatVersion, CatalogJobTypeFormatVersion} {
				raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
				raw = bytes.Replace(raw, row, inserted, 1)
				_, err := decodeEnvelope(raw)
				if (err == nil) != (version == CatalogJobTypeFormatVersion || version == WorkerSessionFormatVersion || version == WorkerExecutionFormatVersion || version == WorkerOfferFormatVersion) {
					t.Fatal("raw command format bypass", command.Kind, version, extra, err)
				}
			}
		}
		prepared, _ := executionRecordFixture(t)
		prepared.Record = decoded
		command := CollectionExecuteCommand{Action: "prepare", Binding: prepared.Binding, Authority: OperatorAuthority{Actor: "operator", Epoch: "epoch", Revision: "policy"}, CapabilitiesDigest: strings.Repeat("c", 64), Ordinal: prepared.Ordinal, Prepared: &prepared}
		prepared.At = r.UpdatedAt.Add(time.Second)
		command.Prepared = &prepared
		raw, _ := json.Marshal(envelope{Version: WorkerPolicyFormatVersion, Commands: []Command{{Kind: "collection_execute", At: prepared.At, CollectionExecute: &command}}})
		raw = bytes.Replace(raw, row, inserted, 1)
		if _, err := decodeEnvelope(raw); err == nil {
			t.Fatal("raw prepared command accepted below18")
		}
		raw = bytes.Replace(raw, []byte(`"version":17`), []byte(`"version":18`), 1)
		if _, err := decodeEnvelope(raw); err != nil {
			t.Fatal("raw prepared extension rejected at18", err)
		}
		frame, err := collectionExecutionEncoding(collectionExecutionRecord{Version: 1, Prepared: &prepared})
		if err != nil {
			t.Fatal(err)
		}
		frame = bytes.Replace(frame, row, inserted, 1)
		if _, err = decodeCollectionExecutionRecord(frame); err == nil {
			t.Fatal("noncanonical null/empty prepared ledger accepted")
		}

		decoded.CommittedIndex = 1
		i := image{Version: WorkerPolicyFormatVersion, Index: 1, Monitors: map[string]Monitor{}, Catalog: map[string]CatalogRecord{decoded.Key.indexKey(): decoded}}
		if validateCatalogImage(i) == nil {
			t.Fatal("empty snapshot field accepted below18")
		}
		canonicalRecord, _ := json.Marshal(decoded)
		explicitRecord := append([]byte("{"+extra+","), canonicalRecord[1:]...)
		snapshot, _ := json.Marshal(i)
		snapshot = bytes.Replace(snapshot, canonicalRecord, explicitRecord, 1)
		if _, err := decodeImage(bytes.NewReader(snapshot)); err == nil {
			t.Fatal("raw empty snapshot field accepted below18")
		}

	}
	// Tagged custom decoding must keep rejecting unknown fields anywhere inside
	// the encrypted record, rather than weakening DisallowUnknownFields.
	for _, raw := range [][]byte{append([]byte(`{"surprise":null,`), row[1:]...), bytes.Replace(row, []byte(`"kind":"Monitor"`), []byte(`"kind":"Monitor","surprise":null`), 1)} {
		var decoded CatalogRecord
		if json.Unmarshal(raw, &decoded) == nil {
			t.Fatal("unknown nested field accepted")
		}
	}
}

func TestCatalogJobTypeExplicitEmptySourceDeletion(t *testing.T) {
	for _, empty := range []string{"null", "[]"} {
		t.Run(empty, func(t *testing.T) {
			s := openCatalogMemory(t)
			r := catalogRecord(t, s, "Monitor", "explicit-empty", uuid.NewString(), uuid.NewString(), "builtin-config")
			row, _ := json.Marshal(r)
			explicit := append([]byte(`{"job_type_references":`+empty+`,`), row[1:]...)
			raw, _ := json.Marshal(envelope{Version: CatalogJobTypeFormatVersion, Commands: []Command{{Kind: "catalog", At: r.UpdatedAt, Catalog: &CatalogMutation{Record: r, Create: true}}}})
			raw = bytes.Replace(raw, row, explicit, 1)
			applied, ok := s.fsm.Apply(&raft.Log{Index: s.fsm.image.Index + 1, Data: raw}).([]Result)
			if !ok || len(applied) != 1 || applied[0].Err != nil || !applied[0].Allowed {
				t.Fatal("explicit-empty admission", applied)
			}
			active := requireCatalog(t, s, r.Key)
			if !active.jobTypeReferencesPresent {
				t.Fatal("committed observation lost explicit field presence")
			}
			removed := catalogSubmit(t, s, deleteCatalogMutation(active, uuid.NewString()))
			if removed.Err != nil || !removed.Allowed || !removed.Catalog.Removed || len(removed.Catalog.JobTypeReferences) != 0 {
				t.Fatal("explicit-empty source cannot be deleted", removed.Err)
			}
			if s.fsm.image.Version != CatalogJobTypeFormatVersion || validateCatalogImage(s.fsm.image) != nil {
				t.Fatal("empty tombstone snapshot invalid")
			}
			if _, ledger, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), ""); err != nil {
				t.Fatal("empty tombstone snapshot decode", err)
			} else {
				_ = ledger.Close()
			}
		})
	}
}
