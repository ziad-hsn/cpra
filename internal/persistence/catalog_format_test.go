package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

// Explicit old envelopes bypass today's writer policy. They exercise the same
// decoder/FSM online and offline recovery uses for retained historical logs.
func applyCatalogFormat(t *testing.T, s *Store, format int, commands ...Command) []Result {
	t.Helper()
	raw, err := json.Marshal(envelope{Version: format, Commands: commands})
	if err != nil {
		t.Fatal(err)
	}
	var response any
	if s.raft != nil {
		future := s.raft.Apply(raw, 5*time.Second)
		if err := future.Error(); err != nil {
			t.Fatal(err)
		}
		response = future.Response()
	} else {
		response = s.fsm.Apply(&raft.Log{Index: s.fsm.image.Index + 1, Data: raw})
	}
	results, ok := response.([]Result)
	if !ok || len(results) != len(commands) {
		t.Fatalf("format %d replay failed: %v", format, response)
	}
	return results
}

func formatCatalogCommand(m CatalogMutation) Command {
	return Command{Kind: "catalog", At: m.Record.UpdatedAt, Catalog: &m}
}

func createCatalogFormat(t *testing.T, s *Store, format int, r CatalogRecord) CatalogRecord {
	t.Helper()
	result := applyCatalogFormat(t, s, format, formatCatalogCommand(CatalogMutation{Record: r, Create: true, Conditions: catalogConditions(t, s, r.References)}))[0]
	if result.Err != nil || !result.Allowed || result.Catalog == nil {
		t.Fatal("explicit-format create", result.Err)
	}
	return *result.Catalog
}

func TestCatalogFormatLegacyReplayAndUniqueSameEntryTokens(t *testing.T) {
	for _, legacy := range []int{CatalogFormatVersion, CollectionFormatVersion} {
		t.Run(string(rune('0'+legacy)), func(t *testing.T) {
			s := openCatalogMemory(t)
			shared := createCatalogFormat(t, s, legacy, catalogRecord(t, s, "Credential", "shared", "shared-uid", "s1", "private"))
			a := createCatalogFormat(t, s, legacy, catalogRecord(t, s, "Endpoint", "a", "a-uid", "a1", "private", shared.Key))
			b := catalogRecord(t, s, "Endpoint", "b", "b-uid", "b1", "private", shared.Key)
			old := applyCatalogFormat(t, s, legacy,
				formatCatalogCommand(updateCatalogMutation(t, s, a, "a2", "private", shared.Key)),
				formatCatalogCommand(CatalogMutation{Record: b, Create: true, Conditions: catalogConditions(t, s, b.References)}))
			for _, result := range old {
				if !result.Allowed || result.Err != nil || result.CatalogMutationSequence != 0 {
					t.Fatal("historical mutation behavior changed", result.Err)
				}
			}
			if got := requireCatalog(t, s, shared.Key).DependentsVersion; got != s.fsm.image.Index || s.fsm.image.CatalogMutationSequence != 0 {
				t.Fatal("legacy replay no longer stamps the original Raft index")
			}
			before := s.fsm.image.Index
			currentA, currentB := requireCatalog(t, s, a.Key), requireCatalog(t, s, b.Key)
			// These independent consumers may both mutate the same dependency in
			// one entry. The later outside mutation must not reuse A's own token.
			results := applyCatalogFormat(t, s, CatalogMutationFormatVersion,
				formatCatalogCommand(updateCatalogMutation(t, s, currentA, "a3", "private", shared.Key)),
				formatCatalogCommand(updateCatalogMutation(t, s, currentB, "b2", "private", shared.Key)))
			for i, result := range results {
				if !result.Allowed || result.Err != nil || result.CatalogMutationSequence != before+uint64(i)+1 || result.Catalog.CommittedIndex != before+1 {
					t.Fatal("new mutation lost its unique token or original commit index", result.Err)
				}
			}
			actual := requireCatalog(t, s, shared.Key)
			if actual.DependentsVersion != results[1].CatalogMutationSequence || actual.DependentsVersion == results[0].CatalogMutationSequence || actual.DependentsVersion <= s.fsm.image.Index {
				t.Fatal("same-entry outside write was indistinguishable from the own write")
			}
			stale := updateCatalogMutation(t, s, actual, "s2", "private")
			stale.ExpectedDependentsVersion = results[0].CatalogMutationSequence
			if got := applyCatalogFormat(t, s, CatalogMutationFormatVersion, formatCatalogCommand(stale))[0]; !errors.Is(got.Err, ErrCatalogConflict) {
				t.Fatal("own-write substitution accepted a later outside mutation", got.Err)
			}
			if s.fsm.image.CatalogMutationSequence != results[1].CatalogMutationSequence {
				t.Fatal("rejected command consumed a token")
			}
		})
	}
}

func TestCatalogFormatWriterMaximumAndHistoricalDecoder(t *testing.T) {
	s := openCatalogMemory(t)
	r := catalogRecord(t, s, "Credential", "ordinary", "uid", "revision", "private")
	create := collectionCreateFixture(t, s, 1)
	commands := []Command{formatCatalogCommand(CatalogMutation{Record: r, Create: true}), {Kind: "collection", At: create.Create.CreatedAt, Collection: &create}}
	results := submit(t, s, commands...)
	if results[0].CatalogMutationSequence == 0 || results[1].Err != nil || s.fsm.image.Version != CatalogMutationFormatVersion {
		t.Fatal("later collection command downgraded the selected envelope/image")
	}
	for _, format := range []int{CatalogFormatVersion, CollectionFormatVersion, CatalogMutationFormatVersion} {
		raw, _ := json.Marshal(envelope{Version: format, Commands: commands[:1]})
		if _, err := decodeEnvelope(raw); err != nil {
			t.Fatal("historical reader minimum was replaced by new writer policy", format, err)
		}
	}
	if commandWriteFormat(commands[0]) != CatalogMutationFormatVersion || commandWriteFormat(commands[1]) != CollectionFormatVersion || commandWriteFormat(Command{Kind: "recover"}) != FormatVersion {
		t.Fatal("writer policy changed unrelated legacy formats")
	}
	seed := Command{Kind: "bootstrap", Bootstrap: &BootstrapCommand{Action: "seed"}}
	if commandWriteFormat(seed) != CatalogMutationFormatVersion {
		t.Fatal("bootstrap's direct catalog path missed the new format")
	}
	seed.Bootstrap.Action = "begin"
	if commandWriteFormat(seed) != CatalogFormatVersion {
		t.Fatal("bootstrap begin unexpectedly requires mutation semantics")
	}
	sequence := s.fsm.image.CatalogMutationSequence
	if retry := submit(t, s, commands[0])[0]; !errors.Is(retry.Err, ErrCatalogConflict) || s.fsm.image.CatalogMutationSequence != sequence || retry.CatalogMutationSequence != 0 {
		t.Fatal("repeated accepted mutation consumed another token", retry.Err)
	}
}

func TestCatalogFormatBootstrapTokensOnlyForAcceptedSeeds(t *testing.T) {
	for _, format := range []int{CatalogFormatVersion, CollectionFormatVersion, CatalogMutationFormatVersion} {
		t.Run(string(rune('0'+format)), func(t *testing.T) {
			s := openCatalogMemory(t)
			manifest, records := bootstrapFixture(t, s)
			apply := func(command BootstrapCommand) Result {
				if format == CatalogMutationFormatVersion {
					return bootstrapSubmit(t, s, command) // Actual current writer.
				}
				return applyCatalogFormat(t, s, format, Command{Kind: "bootstrap", At: time.Now().UTC(), Bootstrap: &command})[0]
			}
			begin := BootstrapCommand{Action: "begin", StageID: manifest.StageID, Manifest: &manifest}
			for range 2 {
				if result := apply(begin); !result.Allowed || result.Err != nil || result.CatalogMutationSequence != 0 || s.fsm.image.CatalogMutationSequence != 0 {
					t.Fatal("bootstrap begin/retry consumed a catalog token", result.Err)
				}
			}
			var previous uint64
			for i := range records {
				before := s.fsm.image.Index
				seed := BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: uint64(i + 1), Record: &records[i]}
				result := apply(seed)
				if !result.Allowed || result.Err != nil {
					t.Fatal("bootstrap seed failed", result.Err)
				}
				if format == CatalogMutationFormatVersion {
					if previous == 0 {
						previous = before
					}
					previous++
					if result.CatalogMutationSequence != previous || s.fsm.image.CatalogMutationSequence != previous {
						t.Fatal("successful seed missed its unique token")
					}
				} else if result.CatalogMutationSequence != 0 || s.fsm.image.CatalogMutationSequence != 0 {
					t.Fatal("legacy seed gained new mutation semantics")
				}
				if duplicate := apply(seed); !errors.Is(duplicate.Err, ErrBootstrapConflict) || duplicate.CatalogMutationSequence != 0 || s.fsm.image.CatalogMutationSequence != previous {
					t.Fatal("rejected duplicate seed changed mutation sequence", duplicate.Err)
				}
			}
			activate := BootstrapCommand{Action: "activate", StageID: manifest.StageID, Manifest: &manifest}
			for range 2 {
				if result := apply(activate); !result.Allowed || result.Err != nil || result.CatalogMutationSequence != 0 || s.fsm.image.CatalogMutationSequence != previous {
					t.Fatal("activation/retry changed mutation sequence", result.Err)
				}
			}
			if err := validateBootstrapImage(s.fsm.image); err != nil {
				t.Fatal("server-owned tokens changed bootstrap input digest", err)
			}
		})
	}
}

func TestCatalogFormatRejectedTypedOperationPreservesTruthfulReceipt(t *testing.T) {
	s := openCatalogMemory(t)
	createCatalog(t, s, catalogRecord(t, s, "Credential", "existing", "uid", "s1", "private"))
	reservation, command := allocatedCommand(t, s, reservationCommand(t, s, "reserved", time.Now().UTC()))
	if result := applyCatalogFormat(t, s, CatalogFormatVersion, command)[0]; !errors.Is(result.Err, ErrCatalogFormatDowngrade) {
		t.Fatal("typed old-format mutation was accepted", result.Err)
	}
	if _, exists := s.fsm.image.OperationReservations[reservation.ID]; !exists {
		t.Fatal("format downgrade consumed an original reservation")
	}
	s.fsm.image.CatalogMutationSequence = math.MaxUint64
	before, _ := json.Marshal(s.fsm.image.Catalog)
	result := submit(t, s, command)[0]
	after, _ := json.Marshal(s.fsm.image.Catalog)
	if !errors.Is(result.Err, ErrCatalogSequenceExhausted) || !bytes.Equal(before, after) || result.CatalogMutationSequence != 0 || s.fsm.image.CatalogMutationSequence != math.MaxUint64 {
		t.Fatal("overflow changed catalog/index/token", result.Err)
	}
	if _, exists := s.fsm.image.OperationReservations[reservation.ID]; exists || result.Operation == nil || result.Operation.ID != reservation.ID || result.Operation.Outcome != "activation_rejected" || result.Operation.CommittedIndex != 0 {
		t.Fatal("rejected target lost established terminal receipt behavior")
	}
	observed, err := s.Operation(reservation.ID)
	if err != nil || observed.Outcome != "activation_rejected" || observed.State != "failed" {
		t.Fatal("overflow terminal receipt unavailable", err)
	}
}

func TestCatalogFormatDowngradeOverflowAndSnapshotValidation(t *testing.T) {
	s := openCatalogMemory(t)
	shared := createCatalog(t, s, catalogRecord(t, s, "Credential", "shared", "uid", "s1", "private"))
	consumer := createCatalog(t, s, catalogRecord(t, s, "Endpoint", "consumer", "c-uid", "c1", "private", shared.Key))
	sequence := s.fsm.image.CatalogMutationSequence
	before, _ := json.Marshal(s.fsm.image.Catalog)
	for _, format := range []int{CatalogFormatVersion, CollectionFormatVersion} {
		mutation := updateCatalogMutation(t, s, consumer, "c2", "private", shared.Key)
		got := applyCatalogFormat(t, s, format, formatCatalogCommand(mutation))[0]
		if !errors.Is(got.Err, ErrCatalogFormatDowngrade) || got.Allowed || got.CatalogMutationSequence != 0 {
			t.Fatal("old mutation semantics were accepted after activation", got.Err)
		}
	}
	after, _ := json.Marshal(s.fsm.image.Catalog)
	if !bytes.Equal(before, after) || sequence != s.fsm.image.CatalogMutationSequence {
		t.Fatal("downgrade rejection changed catalog/index state")
	}
	s.fsm.image.CatalogMutationSequence = math.MaxUint64
	mutation := updateCatalogMutation(t, s, consumer, "c2", "private", shared.Key)
	got := applyCatalogFormat(t, s, CatalogMutationFormatVersion, formatCatalogCommand(mutation))[0]
	after, _ = json.Marshal(s.fsm.image.Catalog)
	if !errors.Is(got.Err, ErrCatalogSequenceExhausted) || !bytes.Equal(before, after) || s.fsm.image.CatalogMutationSequence != math.MaxUint64 || got.CatalogMutationSequence != 0 {
		t.Fatal("overflow wrapped or mutated catalog state", got.Err)
	}
	for _, change := range []func(*image){
		func(i *image) { i.CatalogMutationSequence = 0 },
		func(i *image) { i.Version = CollectionFormatVersion },
		func(i *image) { i.CatalogMutationSequence = 1 },
	} {
		bad := s.fsm.image
		change(&bad)
		data, _ := json.Marshal(bad)
		if _, err := decodeImage(bytes.NewReader(data)); err == nil {
			t.Fatal("invalid sequence/version snapshot accepted")
		}
	}
	// Legacy non-catalog commands remain legal after the switch.
	if result := applyCatalogFormat(t, s, FormatVersion, Command{Kind: "barrier", At: time.Now().UTC()})[0]; !result.Allowed {
		t.Fatal("new catalog mode rejected legacy-shaped non-catalog work")
	}
}

func TestCatalogFormatV3StreamIsFrozenAndV4FramingIsExplicit(t *testing.T) {
	s := openCatalogMemory(t)
	head := createCollectionFixture(t, s, 1)
	item := collectionItemFixture(t, s, head, 1, "staged")
	if result := uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Second)); result.Err != nil {
		t.Fatal(result.Err)
	}
	legacy, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Release()
	first := &collectionTestSink{}
	if err := legacy.Persist(first); err != nil || !bytes.HasPrefix(first.Bytes(), []byte("CPRA-COLLECTION-SNAPSHOT-3\n")) {
		t.Fatal("legacy snapshot framing changed", err)
	}
	createCatalog(t, s, catalogRecord(t, s, "Credential", "new", "uid", "revision", "private"))
	second := &collectionTestSink{}
	if err := legacy.Persist(second); err != nil || !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("later format upgrade changed frozen v3 bytes", err)
	}
	oldImage, oldLedger, err := decodeSnapshot(bytes.NewReader(first.Bytes()), "")
	if err != nil || oldImage.Version != CollectionFormatVersion || oldImage.CatalogMutationSequence != 0 {
		t.Fatal("new reader lost legacy v3 support", err)
	}
	defer oldLedger.Close()
	if got, exists, err := oldLedger.Item(head.ID, 1); err != nil || !exists || !reflect.DeepEqual(got, item) {
		t.Fatal("v3 ciphertext row changed", err)
	}
	current, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer current.Release()
	sink := &collectionTestSink{}
	if err := current.Persist(sink); err != nil || !bytes.HasPrefix(sink.Bytes(), []byte("CPRA-COLLECTION-SNAPSHOT-4\n")) {
		t.Fatal("new image lacks explicit v4 framing", err)
	}
	restored, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil || restored.Version != CatalogMutationFormatVersion || restored.CatalogMutationSequence != s.fsm.image.CatalogMutationSequence {
		t.Fatal("v4 sequence was not retained", err)
	}
	defer ledger.Close()
	if got, exists, err := ledger.Item(head.ID, 1); err != nil || !exists || !reflect.DeepEqual(got, item) {
		t.Fatal("v4 changed the existing input ledger framing", err)
	}
	for _, bad := range [][]byte{
		bytes.Replace(sink.Bytes(), []byte(catalogMutationSnapshotMagic), []byte(collectionSnapshotMagic), 1),
		bytes.Replace(first.Bytes(), []byte(collectionSnapshotMagic), []byte(catalogMutationSnapshotMagic), 1),
		sink.Bytes()[:sink.Len()-1],
		append(bytes.Clone(sink.Bytes()), 'x'),
	} {
		_, rejected, err := decodeSnapshot(bytes.NewReader(bad), "")
		if rejected != nil {
			_ = rejected.Close()
		}
		if err == nil {
			t.Fatal("mismatched version or incomplete stream accepted")
		}
	}
}

func TestCatalogFormatCatalogOnlySnapshotsAndStoppedValidation(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(map[bool]string{false: "logs-only", true: "snapshot"}[snapshot], func(t *testing.T) {
			cfg := testConfig(t)
			s, err := Open(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			r := createCatalog(t, s, catalogRecord(t, s, "Credential", "ordinary", "uid", "revision", "private"))
			sequence := s.fsm.image.CatalogMutationSequence
			if s.fsm.collections != nil {
				t.Fatal("ordinary catalog write allocated a staging ledger")
			}
			if snapshot {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
				if s.fsm.collections != nil {
					t.Fatal("empty snapshot allocated a mutable staging ledger")
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			lock, err := LockOffline(cfg.Storage.Directory)
			if err != nil {
				t.Fatal("catalog-only stopped validation failed", err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := Open(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if got := requireCatalog(t, restarted, r.Key); !reflect.DeepEqual(got, r) || restarted.fsm.image.CatalogMutationSequence != sequence {
				t.Fatal("catalog-only restart changed record or mutation sequence")
			}
		})
	}
}

func TestCatalogFormatMixedSnapshotAndLogRecovery(t *testing.T) {
	cfg := testConfig(t)
	s, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	shared := createCatalogFormat(t, s, CatalogFormatVersion, catalogRecord(t, s, "Credential", "shared", "uid", "s1", "private"))
	consumer := createCatalogFormat(t, s, CollectionFormatVersion, catalogRecord(t, s, "Endpoint", "consumer", "c-uid", "c1", "private", shared.Key))
	head := createCollectionFixture(t, s, 1)
	item := collectionItemFixture(t, s, head, 1, "staged")
	if r := uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Second)); r.Err != nil {
		t.Fatal(r.Err)
	}
	if s.fsm.image.Version != CollectionFormatVersion || s.fsm.image.CatalogMutationSequence != 0 {
		t.Fatal("legacy input was prematurely migrated")
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	prior := s.fsm.image.Index
	result := catalogSubmit(t, s, updateCatalogMutation(t, s, consumer, "c2", "private", shared.Key))
	if result.Err != nil || result.CatalogMutationSequence != prior+1 {
		t.Fatal("first upgraded log mutation did not seed from preceding image index", result.Err)
	}
	guard := requireCatalog(t, s, shared.Key).DependentsVersion
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := LockOffline(cfg.Storage.Directory)
	if err != nil {
		t.Fatal("mixed snapshot/log offline validation", err)
	}
	_ = lock.Close()
	restarted, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if requireCatalog(t, restarted, shared.Key).DependentsVersion != guard || restarted.fsm.image.CatalogMutationSequence != result.CatalogMutationSequence {
		t.Fatal("replay changed the format transition or reverse token")
	}
	page, err := restarted.CollectionPage(head.ID, 0, 1)
	if err != nil || len(page) != 1 || !reflect.DeepEqual(page[0], item) {
		t.Fatal("mixed recovery changed legacy input bytes", err)
	}
	if err := restarted.Snapshot(); err != nil {
		t.Fatal(err)
	}
	// Freeze/restore the v4 image independently of the selected generation.
	frozen, err := restarted.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	sink := &collectionTestSink{}
	if err := frozen.Persist(sink); err != nil {
		t.Fatal(err)
	}
	history, err := openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	history.catalog.Index = restarted.fsm.image.Index
	restored := &machine{history: history, collectionDirectory: filepath.Join(t.TempDir(), "restored")}
	if err := restored.Restore(io.NopCloser(bytes.NewReader(sink.Bytes()))); err != nil {
		t.Fatal(err)
	}
	defer restored.collections.Close()
	if restored.image.CatalogMutationSequence != result.CatalogMutationSequence || restored.image.Version != CatalogMutationFormatVersion {
		t.Fatal("v4 snapshot restore lost sequence")
	}
}
