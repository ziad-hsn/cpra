//go:build linux || darwin || windows

package localadmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// This exercises the stopped copy, rather than marking the original directory
// restored. No controller, executor, or provider is constructed by this fixture.
func TestStoppedBackupRestorePreservesCollectionsAfterRetirement(t *testing.T) {
	backupCollectionProcess(t, false)
}

func TestStoppedBackupRestorePendingCollectionRetirement(t *testing.T) {
	backupCollectionProcess(t, true)
}

func backupCollectionProcess(t *testing.T, pending bool) {
	t.Helper()
	if directory := os.Getenv("CPRA_BACKUP_COLLECTION_CHILD"); directory != "" {
		stoppedBackupRestoreCollections(t, directory, pending)
		return
	}
	// Snapshot, file synchronization and Close do not take a context. Contain
	// any regression in a child so blocked cleanup cannot hang the parent suite.
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	directory := backupCollectionRoot(t)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$",
		"-test.count=1", "-test.v", "-test.timeout=2m")
	command.WaitDelay = 5 * time.Second
	command.Env = append(os.Environ(), "CPRA_BACKUP_COLLECTION_CHILD="+directory,
		"GOTMPDIR="+directory, "TMPDIR="+directory, "TMP="+directory, "TEMP="+directory)
	output, err := command.CombinedOutput()
	t.Log(strings.TrimSpace(string(output)))
	if err != nil || ctx.Err() != nil {
		t.Fatalf("backup collection fixture process failed: %v (deadline: %v)", err, ctx.Err())
	}
}

func stoppedBackupRestoreCollections(t *testing.T, root string, pending bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	key := backupTestKey(t, root)
	source, backup, restored := filepath.Join(root, "state"), filepath.Join(root, "backup"), filepath.Join(root, "restored")
	config := runtimeconfig.Default()
	open := func(directory string, administrative bool) *persistence.Store {
		t.Helper()
		config.Storage.Directory = directory
		var store *persistence.Store
		var err error
		if administrative {
			store, err = persistence.OpenAdministrative(ctx, config)
		} else {
			store, err = persistence.Open(ctx, config)
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Error("close store:", err)
			}
		})
		return store
	}
	closeStore := func(store *persistence.Store) {
		t.Helper()
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	store := open(source, false)
	nodeID := store.Status().NodeID
	verifier := sha256.Sum256([]byte("original-operator-token-for-backup-fixture"))
	authority, err := store.CommitAuthentication(ctx, persistence.AuthenticationCommand{
		Mode: "bootstrap", Epoch: uuid.NewString(), Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(),
		Principals: []persistence.AuthenticationPrincipal{{ID: "original-operator", Role: "operator", TokenSHA256: hex.EncodeToString(verifier[:])}},
	})
	if err != nil {
		t.Fatal(err)
	}
	head, item := backupCollectionInput(t, ctx, store)
	for cycle := 0; cycle < 3; cycle++ {
		if cycle != 0 {
			store = open(source, false)
		}
		got, exists, err := store.CollectionGet(head.ID)
		if err != nil || !exists || !reflect.DeepEqual(got, head) {
			t.Fatalf("cycle %d changed committed collection metadata: %v", cycle, err)
		}
		items, err := store.CollectionPage(head.ID, 0, 100)
		if err != nil || !reflect.DeepEqual(items, []persistence.CollectionItem{item}) {
			t.Fatalf("cycle %d lost encrypted collection input: %v", cycle, err)
		}
		if !pending || cycle < 2 {
			waitBackupCollectionCleanup(t, ctx, store, source, min(cycle, 1), uint64(max(cycle-1, 0)), 30*time.Second)
		}
		if err := store.Snapshot(); err != nil {
			t.Fatal(err)
		}
		if cycle < 2 {
			closeStore(store)
		}
	}
	retired := store.Status().Collections.Retired

	at := time.Now().UTC()
	monitor := persistence.Monitor{ID: "stable-monitor", Revision: "revision-1", Name: "backup fixture", Policy: persistence.Policy{
		Interval: time.Minute, Unhealthy: 1, Healthy: 2, Enabled: true, RecoveryBypass: true, Endpoints: map[string]int{"red": 1},
	}}
	backupCollectionSubmit(t, ctx, store, persistence.Command{Kind: "configure", MonitorID: monitor.ID, Revision: monitor.Revision, Config: &monitor, At: at})
	backupCollectionSubmit(t, ctx, store, persistence.Command{Kind: "pulse", MonitorID: monitor.ID, Revision: monitor.Revision, Generation: 1, Outcome: "failure", Scheduled: at, At: at})
	monitor, _ = monitorWithActionHistory(t, store, monitor.ID)
	if len(monitor.Actions) != 1 {
		t.Fatal("fixture did not create exactly one incident action")
	}
	var actionID string
	for actionID = range monitor.Actions {
	}
	backupCollectionSubmit(t, ctx, store, persistence.Command{Kind: "start", MonitorID: monitor.ID, Revision: monitor.Revision, ActionID: actionID, At: at})
	monitor, _ = monitorWithActionHistory(t, store, monitor.ID)
	if monitor.Actions[actionID].State != persistence.Started {
		t.Fatal("fixture requires an interrupted started action")
	}
	history, err := store.History().Page(monitor.ID, "", 100)
	if err != nil || len(history.Events) == 0 || history.NextCursor != "" {
		t.Fatal("fixture incident history missing or unexpectedly paged", err)
	}
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if pending {
		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(filepath.Join(source, "collections", "gc-retirement.json")); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			select {
			case <-deadline.C:
				t.Fatal("fixture did not reach pending retirement")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-ticker.C:
			}
		}
	}
	closeStore(store)
	before, err := treeInventory(source)
	if err != nil {
		t.Fatal(err)
	}
	if hasIntent := slices.ContainsFunc(before, func(file BackupFile) bool { return file.Path == "collections/gc-retirement.json" }); hasIntent != pending {
		t.Fatal("stopped retirement state differs from the requested fixture")
	}
	if slices.ContainsFunc(before, func(file BackupFile) bool { return file.Path == "collections/gc-retirement.tmp" }) {
		t.Fatal("fixture captured an incomplete intent publication")
	}
	if err := Backup(source, backup, "", key); err != nil {
		t.Fatal(err)
	}
	manifest, err := readBackupManifest(filepath.Join(backup, "BACKUP.json"))
	if err != nil || manifest.NodeID != nodeID || !sameInventory(before, manifest.Files) {
		t.Fatal("backup did not preserve the complete stopped inventory", err)
	}
	var generations struct {
		NodeID    string   `json:"nodeID"`
		Current   string   `json:"current"`
		Previous  string   `json:"previous"`
		Completed []string `json:"completed"`
		Retired   uint64   `json:"retired"`
	}
	catalogPath := filepath.Join(backup, "data", "collections", "generations.json")
	catalog, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal("backup lacks collection generation catalog", err)
	}
	wantCompleted := 2
	if pending {
		wantCompleted = 3
	}
	if err := json.Unmarshal(catalog, &generations); err != nil || generations.NodeID != nodeID || !pending && generations.Retired < 1 ||
		len(generations.Completed) != wantCompleted || generations.Current == generations.Previous ||
		!slices.Contains(generations.Completed, generations.Current) || !slices.Contains(generations.Completed, generations.Previous) {
		t.Fatal("backup lacks a retired generation and completed retention pair", err)
	}
	for _, required := range []string{"collections/generations.json", "collections/current.json", "history/catalog.json",
		"collections/" + generations.Current + "/ledger.db", "collections/" + generations.Previous + "/ledger.db"} {
		if !slices.ContainsFunc(manifest.Files, func(file BackupFile) bool { return file.Path == required && file.Size > 0 }) {
			t.Fatalf("backup inventory omitted %s", required)
		}
	}
	var pendingGeneration string
	if pending {
		raw, err := os.ReadFile(filepath.Join(backup, "data", "collections", "gc-retirement.json"))
		var intent struct {
			Generation string `json:"generation"`
		}
		if err != nil || json.Unmarshal(raw, &intent) != nil || intent.Generation == "" ||
			intent.Generation == generations.Current || intent.Generation == generations.Previous || !slices.Contains(generations.Completed, intent.Generation) {
			t.Fatal("fixture did not capture an eligible retirement intent", err)
		}
		pendingGeneration = intent.Generation
	}
	if err := Restore(backup, restored, key); err != nil {
		t.Fatal(err)
	}
	// Restore deliberately replaces identity.json and adds its reset marker.
	// All other archived bytes, including both ledgers, must survive the copy.
	for _, file := range manifest.Files {
		if file.Path == "identity.json" || pending && (file.Path == "collections/gc-retirement.json" ||
			file.Path == "collections/generations.json" || strings.HasPrefix(file.Path, "collections/"+pendingGeneration+"/")) {
			continue
		}
		got, err := hashFile(filepath.Join(restored, filepath.FromSlash(file.Path)))
		got.Path = file.Path
		if err != nil || got != file {
			t.Fatalf("restoration changed archived file %s: %v", file.Path, err)
		}
	}
	if pending {
		for _, relative := range []string{"collections/gc-retirement.json", "collections/" + pendingGeneration} {
			if _, err := os.Lstat(filepath.Join(restored, filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("restoration did not finalize the copied retirement", err)
			}
		}
		archived, err := treeInventory(filepath.Join(backup, "data"))
		if err != nil || !sameInventory(archived, manifest.Files) {
			t.Fatal("restore changed the authenticated backup", err)
		}
	}
	config.Storage.Directory = restored
	rejected, err := persistence.Open(ctx, config)
	if rejected != nil {
		closeStore(rejected)
	}
	if !errors.Is(err, persistence.ErrAuthenticationResetRequired) {
		t.Fatal("restored authentication was usable without provisioning", err)
	}
	admin := open(restored, true)
	reset, err := admin.Authentication()
	if err != nil || !reset.ResetRequired || reset.Epoch == authority.Epoch || len(reset.Principals) != 0 {
		t.Fatal("restoration failed to fence old authentication", err)
	}
	verifier = sha256.Sum256([]byte("replacement-operator-token-for-backup-fixture"))
	if _, err := admin.CommitAuthentication(ctx, persistence.AuthenticationCommand{
		Mode: "provision", Epoch: reset.Epoch, ExpectedEpoch: reset.Epoch, ExpectedRevision: reset.Revision,
		Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(),
		Principals: []persistence.AuthenticationPrincipal{{ID: "replacement-operator", Role: "operator", TokenSHA256: hex.EncodeToString(verifier[:])}},
	}); err != nil {
		t.Fatal(err)
	}
	closeStore(admin)
	store = open(restored, false)
	if store.Status().NodeID != nodeID {
		t.Fatal("restore broke the encryption-bound node identity")
	}
	if _, _, err := store.CollectionGet(head.ID); !errors.Is(err, persistence.ErrOperationExpired) {
		t.Fatal("restored upload retained its old operation authority", err)
	}
	results, err := store.Submit(ctx, []persistence.Command{{Kind: "collection", At: time.Now().UTC(), Collection: &persistence.CollectionCommand{
		Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item,
	}}})
	if err != nil || len(results) != 1 || !errors.Is(results[0].Err, persistence.ErrOperationExpired) || results[0].Allowed {
		t.Fatal("old upload could continue after explicit restore", err, results)
	}
	receipts, err := store.History().Page("collection/"+head.ID, "", 100)
	if err != nil || len(receipts.Events) != 1 {
		t.Fatal("restored upload lost its invalidation evidence", err)
	}
	receipt := receipts.Events[0]
	if receipt.Type != "collection_invalidated" || receipt.Collection == nil || receipt.Collection.ID != head.ID ||
		receipt.Collection.UploadID != head.UploadID || receipt.Collection.ContentDigest != head.ContentDigest ||
		receipt.Collection.Uploaded != 1 || receipt.Collection.InvalidatedByRestore == "" {
		t.Fatal("restored upload receipt lost its original identity")
	}
	after, exists := monitorWithActionHistory(t, store, monitor.ID)
	if !exists || !after.Incident || after.ID != monitor.ID || after.Revision != monitor.Revision || after.Actions[actionID].State != persistence.Unknown {
		t.Fatal("restore lost incident state or made an interrupted action executable")
	}
	retained, err := store.History().Page(monitor.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range history.Events {
		if !slices.ContainsFunc(retained.Events, func(got persistence.Event) bool { return reflect.DeepEqual(got, event) }) {
			t.Fatalf("restoration lost or changed incident event %s", event.ID)
		}
	}
	newHead, newItem := backupCollectionInput(t, ctx, store)
	oldEpoch, _, err := persistence.ParseOperationHandle(head.ID)
	if err != nil {
		t.Fatal(err)
	}
	newEpoch, _, err := persistence.ParseOperationHandle(newHead.ID)
	if err != nil || oldEpoch == newEpoch {
		t.Fatal("new collection reused restored operation authority", err)
	}
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	closeStore(store)
	store = open(restored, false)
	items, err := store.CollectionPage(newHead.ID, 0, 100)
	if err != nil || !reflect.DeepEqual(items, []persistence.CollectionItem{newItem}) {
		t.Fatal("fresh collection failed after restored store restart", err)
	}
	// The rejected normal open, administrative reset, normal start and restart
	// each rebuild a generation. Retirement advances in bounded one-second turns.
	waitBackupCollectionCleanup(t, ctx, store, restored, 1, retired+1, time.Minute)
	closeStore(store)
	finalCatalog, err := os.ReadFile(filepath.Join(restored, "collections", "generations.json"))
	var completed struct {
		NodeID    string   `json:"nodeID"`
		Current   string   `json:"current"`
		Previous  string   `json:"previous"`
		Completed []string `json:"completed"`
		Retired   uint64   `json:"retired"`
	}
	if err != nil || json.Unmarshal(finalCatalog, &completed) != nil || len(completed.Completed) != 2 ||
		completed.NodeID != nodeID || completed.Retired < retired+1 || completed.Current == completed.Previous ||
		!slices.Contains(completed.Completed, completed.Current) || !slices.Contains(completed.Completed, completed.Previous) {
		t.Fatal("completed cleanup did not retain exactly current and previous generations", err)
	}

	if err := os.WriteFile(catalogPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, "tampered-restore")
	if err := Restore(backup, bad, key); err == nil || !strings.Contains(err.Error(), "inventory mismatch") {
		t.Fatal("altered generation catalog passed authenticated restore", err)
	}
	if _, err := os.Stat(bad); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed restore published a destination", err)
	}
}

func backupCollectionSubmit(t *testing.T, ctx context.Context, store *persistence.Store, command persistence.Command) persistence.Result {
	t.Helper()
	results, err := store.Submit(ctx, []persistence.Command{command})
	if err != nil || len(results) != 1 {
		t.Fatal("fixture command submission failed", err)
	}
	if results[0].Err != nil {
		t.Fatal("fixture command was not committed", results[0].Err)
	}
	if (command.Kind == "collection" || command.Kind == "start") && !results[0].Allowed {
		t.Fatal("fixture operation was not admitted")
	}
	return results[0]
}

func backupCollectionInput(t *testing.T, ctx context.Context, store *persistence.Store) (persistence.CollectionState, persistence.CollectionItem) {
	t.Helper()
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	head := persistence.CollectionState{UploadID: uuid.NewString(), Actor: "team/oncall", IdentityFormat: commitment.Format,
		ContentDigest: strings.Repeat("a", 64), ItemCount: 2, MaxEncodedBytes: 1 << 20,
		ProgressDigest: persistence.CollectionInitialDigest(), Phase: "uploading", CreatedAt: at, ActivityAt: at,
		ExpiresAt: at.Add(persistence.CollectionInactivityLifetime)}
	head.Secret, err = sealer.Seal(ctx, head.Binding(store.Status().NodeID), []byte("private-source-fingerprint"))
	if err != nil {
		t.Fatal(err)
	}
	result := backupCollectionSubmit(t, ctx, store, persistence.Command{Kind: "collection", At: at,
		Collection: &persistence.CollectionCommand{Action: "create", Epoch: uuid.NewString(), Create: &head}})
	if result.Collection == nil {
		t.Fatal("create did not return collection identity")
	}
	head = result.Collection.Clone()
	item := persistence.CollectionItem{Ordinal: 1, Key: persistence.CatalogKey{Kind: "Credential", ID: "fixture"},
		Source: "source.00000000000000000001", SourceDocument: 1, SourceItem: 1, ContentDigest: strings.Repeat("b", 64)}
	item.Payload, err = sealer.Seal(ctx, item.Binding(store.Status().NodeID, head.UploadID),
		[]byte(`{"kind":"Credential","metadata":{"id":"fixture"},"spec":{"value":"private-test-value"}}`))
	if err != nil {
		t.Fatal(err)
	}
	result = backupCollectionSubmit(t, ctx, store, persistence.Command{Kind: "collection", At: at,
		Collection: &persistence.CollectionCommand{Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item}})
	if result.Collection == nil || result.Collection.Uploaded != 1 || result.Collection.Phase != "uploading" {
		t.Fatal("fixture must retain a partially uploaded collection")
	}
	return result.Collection.Clone(), item
}

func waitBackupCollectionCleanup(t *testing.T, ctx context.Context, store *persistence.Store, directory string, retained int, retired uint64, allowance time.Duration) {
	t.Helper()
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, allowance)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	previousProgress := ""
	for {
		status := store.Status()
		if !status.Ready {
			t.Fatal("collection cleanup made storage unavailable")
		}
		accounting := status.Collections
		if accounting != nil && accounting.Available {
			progress := fmt.Sprintf("active=%d retained=%d pinned=%d retired=%d", accounting.Active, accounting.Retained, accounting.Pinned, accounting.Retired)
			if progress != previousProgress {
				t.Logf("cleanup progress after %s: %s condition=%s", time.Since(started), progress, accounting.CleanupCondition)
				previousProgress = progress
			}
			if accounting.CleanupCondition == "blocked" || accounting.CleanupCondition == "unavailable" {
				t.Fatalf("collection cleanup failed: %+v", accounting)
			}
			// A completed filesystem call can exceed the cooperative budget.
			// Preserve that diagnostic without treating it as unfinished cleanup.
			completedStep := accounting.CleanupCondition == "" || accounting.CleanupCondition == "budget_exceeded"
			if accounting.ObservedAt.After(started) && completedStep &&
				accounting.Active == 1 && accounting.Retained == retained && accounting.Pinned == 0 && accounting.Retired >= retired {
				// Catalog counters advance before the identity-bound retirement
				// intent is removed. Only its completed state can be copied here.
				_, err := os.Lstat(filepath.Join(directory, "collections", "gc-retirement.json"))
				if errors.Is(err, os.ErrNotExist) {
					if accounting.CleanupCondition != "" {
						t.Logf("cleanup completed with %s: %.3f ms", accounting.CleanupCondition, accounting.StepDurationMS)
					}
					return
				}
				if err != nil {
					t.Fatal("inspect retirement finalization:", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("collection cleanup did not finish: %+v (%v)", accounting, ctx.Err())
		case <-ticker.C:
		}
	}
}
