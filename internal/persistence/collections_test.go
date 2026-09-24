package persistence

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	raftbolt "github.com/hashicorp/raft-boltdb/v2"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func collectionCreateFixture(t *testing.T, s *Store, count uint64) CollectionCommand {
	t.Helper()
	at := time.Now().UTC()
	state := CollectionState{UploadID: uuid.NewString(), Actor: "team/oncall", IdentityFormat: commitment.Format,
		ContentDigest: strings.Repeat("a", 64), ItemCount: count, MaxEncodedBytes: 16 << 20,
		ProgressDigest: collectionInitialDigest(), Phase: "uploading", CreatedAt: at, ActivityAt: at,
		ExpiresAt: at.Add(CollectionInactivityLifetime)}
	var err error
	state.Secret, err = catalogSealer(t).Seal(context.Background(), state.Binding(s.nodeID), []byte("private-inventory-key-and-source-fingerprint"))
	if err != nil {
		t.Fatal(err)
	}
	return CollectionCommand{Action: "create", Epoch: uuid.NewString(), Create: &state}
}

func collectionCommand(t *testing.T, s *Store, c CollectionCommand, at time.Time) Result {
	t.Helper()
	r := submit(t, s, Command{Kind: "collection", At: at, Collection: &c})
	if len(r) != 1 {
		t.Fatal("missing collection result")
	}
	return r[0]
}

func createCollectionFixture(t *testing.T, s *Store, count uint64) CollectionState {
	t.Helper()
	c := collectionCreateFixture(t, s, count)
	r := collectionCommand(t, s, c, c.Create.CreatedAt)
	if r.Err != nil || !r.Allowed || r.Collection == nil {
		t.Fatal("collection not created", r.Err)
	}
	return r.Collection.Clone()
}

func collectionItemFixture(t *testing.T, s *Store, head CollectionState, ordinal uint64, id string) CollectionItem {
	t.Helper()
	item := CollectionItem{Ordinal: ordinal, Key: CatalogKey{Kind: "Credential", ID: id}, Source: "source.00000000000000000001",
		SourceDocument: 1, SourceItem: ordinal, ContentDigest: strings.Repeat("b", 64)}
	var err error
	item.Payload, err = catalogSealer(t).Seal(context.Background(), item.Binding(s.nodeID, head.UploadID),
		[]byte(`{"kind":"Credential","metadata":{"id":"`+id+`"},"spec":{"value":"never-write-this-collection-plaintext"}}`))
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func uploadCollectionFixture(t *testing.T, s *Store, head CollectionState, item CollectionItem, at time.Time) Result {
	t.Helper()
	return collectionCommand(t, s, CollectionCommand{Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item}, at)
}

func TestCollectionCommandsInactiveIdempotentAndConditional(t *testing.T) {
	s := openCatalogMemory(t)
	create := collectionCreateFixture(t, s, 3)
	head := *collectionCommand(t, s, create, create.Create.CreatedAt).Collection
	item := collectionItemFixture(t, s, head, 1, "first")
	at := head.CreatedAt.Add(time.Second)
	uploaded := uploadCollectionFixture(t, s, head, item, at)
	if uploaded.Err != nil || uploaded.Collection.Uploaded != 1 || uploaded.Collection.EncodedBytes == 0 {
		t.Fatal(uploaded.Err)
	}
	retry := uploadCollectionFixture(t, s, head, item, at.Add(time.Second))
	if retry.Err != nil || retry.Collection.Uploaded != 1 || retry.Collection.EncodedBytes != uploaded.Collection.EncodedBytes ||
		!retry.Collection.ExpiresAt.Equal(at.Add(time.Second).Add(CollectionInactivityLifetime)) {
		t.Fatal("retry changed inventory", retry.Err)
	}
	recreated := collectionCommand(t, s, create, create.Create.CreatedAt)
	if recreated.Err != nil || recreated.Collection.ID != head.ID || recreated.Collection.Uploaded != 1 {
		t.Fatal("create retry allocated another identity", recreated.Err)
	}
	changed := item.Clone()
	changed.Payload.Ciphertext[0] ^= 1
	if result := uploadCollectionFixture(t, s, head, changed, at.Add(2*time.Second)); !errors.Is(result.Err, ErrCollectionConflict) {
		t.Fatal("changed ciphertext accepted", result.Err)
	}
	duplicateKey := collectionItemFixture(t, s, head, 2, "first")
	if result := uploadCollectionFixture(t, s, head, duplicateKey, at.Add(2*time.Second)); !errors.Is(result.Err, ErrCollectionConflict) {
		t.Fatal("duplicate identity accepted", result.Err)
	}
	skip := collectionItemFixture(t, s, head, 3, "third")
	if result := uploadCollectionFixture(t, s, head, skip, at.Add(2*time.Second)); !errors.Is(result.Err, ErrCollectionConflict) {
		t.Fatal("upload gap accepted", result.Err)
	}
	if result := uploadCollectionFixture(t, s, head, item, retry.Collection.ExpiresAt); !errors.Is(result.Err, ErrOperationExpired) {
		t.Fatal("expired upload renewed", result.Err)
	}
	page, err := s.CollectionPage(head.ID, 0, 100)
	if err != nil || len(page) != 1 || !reflect.DeepEqual(page[0], item) {
		t.Fatal("incorrect retained input", err)
	}
	page[0].Payload.Ciphertext[0] ^= 1
	fresh, _ := s.CollectionPage(head.ID, 0, 100)
	if !reflect.DeepEqual(fresh[0], item) {
		t.Fatal("page leaked mutable ledger storage")
	}
	view, _ := s.CatalogSnapshot()
	if view.Len() != 0 || len(s.fsm.image.Monitors) != 0 || len(s.fsm.image.Operations) != 0 {
		t.Fatal("inactive upload changed active configuration or admitted work")
	}
	// Later catalog-format commands must not downgrade the collection format.
	createCatalog(t, s, catalogRecord(t, s, "Credential", "ordinary", "uid", "revision", "encrypted ordinary config"))
	if s.fsm.image.Version != CatalogMutationFormatVersion {
		t.Fatal("catalog mutation downgraded storage")
	}
}

type collectionTestSink struct {
	bytes.Buffer
	cancelled bool
}

func (s *collectionTestSink) ID() string    { return "collection-test" }
func (s *collectionTestSink) Close() error  { return nil }
func (s *collectionTestSink) Cancel() error { s.cancelled = true; return nil }

// captureSnapshotBytes exercises the complete current snapshot format and
// releases its read view before callers restore or close the original owner.
func captureSnapshotBytes(t *testing.T, f *machine) []byte {
	t.Helper()
	snapshot, err := f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	sink := &collectionTestSink{}
	if err := snapshot.Persist(sink); err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(sink.Bytes())
}

func TestCollectionSnapshotFreezesPrefixAndRequiresAllRows(t *testing.T) {
	s := openCatalogMemory(t)
	head := createCollectionFixture(t, s, 2)
	first := collectionItemFixture(t, s, head, 1, "first")
	if r := uploadCollectionFixture(t, s, head, first, head.ActivityAt.Add(time.Second)); r.Err != nil {
		t.Fatal(r.Err)
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	second := collectionItemFixture(t, s, head, 2, "second")
	if r := uploadCollectionFixture(t, s, head, second, head.ActivityAt.Add(2*time.Second)); r.Err != nil {
		t.Fatal(r.Err)
	}
	sink := &collectionTestSink{}
	if err := snapshot.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sink.Bytes(), []byte("never-write-this-collection-plaintext")) || bytes.Contains(sink.Bytes(), []byte("private-inventory-key")) {
		t.Fatal("plaintext in snapshot")
	}
	image, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if image.Collections[head.ID].Uploaded != 1 {
		t.Fatal("snapshot saw a later upload")
	}
	row, exists, err := ledger.Item(head.ID, 1)
	if err != nil || !exists || !reflect.DeepEqual(row, first) {
		t.Fatal("snapshot lost original ciphertext", err)
	}
	if _, exists, _ := ledger.Item(head.ID, 2); exists {
		t.Fatal("snapshot included future row")
	}
	for _, raw := range [][]byte{sink.Bytes()[:sink.Len()-1], append(bytes.Clone(sink.Bytes()), 'x')} {
		_, _, err := decodeSnapshot(bytes.NewReader(raw), filepath.Join(t.TempDir(), "ledger"))
		if err == nil {
			t.Fatal("accepted truncated or extra collection snapshot data")
		}
	}
	raw, _ := json.Marshal(image)
	if _, _, err := decodeSnapshot(bytes.NewReader(raw), ""); err == nil {
		t.Fatal("accepted header without streamed rows")
	}
}

func TestCollectionSnapshotAndLaterLogRecoverFromOriginalInputs(t *testing.T) {
	for _, withSnapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(withSnapshot), func(t *testing.T) {
			config := testConfig(t)
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			head := createCollectionFixture(t, s, 2)
			first := collectionItemFixture(t, s, head, 1, "first")
			if r := uploadCollectionFixture(t, s, head, first, head.ActivityAt.Add(time.Second)); r.Err != nil {
				t.Fatal(r.Err)
			}
			if withSnapshot {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			second := collectionItemFixture(t, s, head, 2, "second")
			if r := uploadCollectionFixture(t, s, head, second, head.ActivityAt.Add(2*time.Second)); r.Err != nil {
				t.Fatal(r.Err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			lock, err := LockOffline(config.Storage.Directory)
			if err != nil {
				t.Fatal("complete stopped backup validation", err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			recovered, exists, err := s.CollectionGet(head.ID)
			if err != nil || !exists || recovered.Uploaded != 2 {
				t.Fatal("restart lost upload", err)
			}
			page, err := s.CollectionPage(head.ID, 0, 100)
			if err != nil || !reflect.DeepEqual(page, []CollectionItem{first, second}) {
				t.Fatal("restart regenerated original ciphertext", err)
			}
			plain, err := catalogSealer(t).Open(context.Background(), page[1].Binding(s.nodeID, recovered.UploadID), page[1].Payload)
			if err != nil || !bytes.Contains(plain, []byte("never-write-this-collection-plaintext")) {
				t.Fatal("restart changed encrypted binding", err)
			}
			clear(plain)
		})
	}
}

func TestCollectionProcessCrashHelper(t *testing.T) {
	directory := os.Getenv("CPRA_COLLECTION_CRASH_DIRECTORY")
	if directory == "" {
		return
	}
	config := testConfig(t)
	config.Storage.Directory = directory
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	head := createCollectionFixture(t, s, 2)
	first := collectionItemFixture(t, s, head, 1, "first")
	if r := uploadCollectionFixture(t, s, head, first, head.ActivityAt.Add(time.Second)); r.Err != nil {
		t.Fatal(r.Err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	second := collectionItemFixture(t, s, head, 2, "second")
	if r := uploadCollectionFixture(t, s, head, second, head.ActivityAt.Add(2*time.Second)); r.Err != nil {
		t.Fatal(r.Err)
	}
	fmt.Println("COLLECTION_COMMITTED " + head.ID)
	select {}
}

func TestCollectionCommittedUploadSurvivesProcessKill(t *testing.T) {
	config := testConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCollectionProcessCrashHelper$")
	cmd.Env = append(os.Environ(), "CPRA_COLLECTION_CRASH_DIRECTORY="+config.Storage.Directory)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() {
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			if strings.HasPrefix(scan.Text(), "COLLECTION_COMMITTED ") {
				ready <- strings.TrimPrefix(scan.Text(), "COLLECTION_COMMITTED ")
				return
			}
		}
		ready <- ""
	}()
	var id string
	select {
	case id = <-ready:
	case <-ctx.Done():
		t.Fatal("process did not commit within startup budget")
	}
	if _, _, err := ParseOperationHandle(id); err != nil {
		t.Fatal("process exited before committed upload")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waited = true
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	head, exists, err := s.CollectionGet(id)
	if err != nil || !exists || head.Uploaded != 2 {
		t.Fatal("forced termination lost committed prefix", err)
	}
	page, err := s.CollectionPage(id, 0, 100)
	if err != nil || len(page) != 2 || page[0].Key.ID != "first" || page[1].Key.ID != "second" {
		t.Fatal("incorrect crash recovery", err)
	}
	view, _ := s.CatalogSnapshot()
	if view.Len() != 0 {
		t.Fatal("replay activated collection input")
	}
}

func TestCollectionStorageFailureStopsAdmission(t *testing.T) {
	s := openCatalogMemory(t)
	head := createCollectionFixture(t, s, 1)
	item := collectionItemFixture(t, s, head, 1, "first")
	if err := s.fsm.collections.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := s.Submit(context.Background(), []Command{{Kind: "collection", At: head.ActivityAt.Add(time.Second),
		Collection: &CollectionCommand{Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item}}})
	if !errors.Is(err, ErrCommitUnconfirmed) || s.Status().Ready {
		t.Fatal("failed materialization did not stop admission", err)
	}
	if _, _, err := s.CollectionGet(head.ID); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("failed ledger remained readable", err)
	}
	if _, err := s.Submit(context.Background(), []Command{{Kind: "barrier", At: time.Now().UTC()}}); err == nil {
		t.Fatal("continued writes after storage failure")
	}
}

func TestCollectionExplicitRestoreInvalidatesUploadAndKeepsSnapshotComplete(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	head := createCollectionFixture(t, s, 1)
	item := collectionItemFixture(t, s, head, 1, "first")
	if r := uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Second)); r.Err != nil {
		t.Fatal(r.Err)
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
	defer s.Close()
	if _, _, err := s.CollectionGet(head.ID); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("restored operation still resumable", err)
	}
	if s.fsm.image.Collections[head.ID].Phase != "invalidated" {
		t.Fatal("restore lost upload fence")
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal("invalidated ciphertext cannot snapshot", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := LockOffline(config.Storage.Directory)
	if err != nil {
		t.Fatal("invalidated collection broke stopped backup validation", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionRecoveryRejectsMissingCommittedLogDespiteRetainedMaterialization(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	head := createCollectionFixture(t, s, 2)
	first := collectionItemFixture(t, s, head, 1, "first")
	if r := uploadCollectionFixture(t, s, head, first, head.ActivityAt.Add(time.Second)); r.Err != nil {
		t.Fatal(r.Err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	second := collectionItemFixture(t, s, head, 2, "second")
	if r := uploadCollectionFixture(t, s, head, second, head.ActivityAt.Add(2*time.Second)); r.Err != nil {
		t.Fatal(r.Err)
	}
	lostIndex := s.Status().CommittedIndex
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	log, err := raftbolt.NewBoltStore(filepath.Join(config.Storage.Directory, "raft.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.DeleteRange(lostIndex, lostIndex); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	// The separate materialization still has both ciphertext rows. It must not
	// mask missing authoritative log input or supply the replay's newer state.
	if reopened, err := Open(context.Background(), config); err == nil {
		_ = reopened.Close()
		t.Fatal("retained materialization masked missing committed Raft data")
	}
}
