package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/raft"
	raftbolt "github.com/hashicorp/raft-boltdb/v2"
)

// The fixture writes the real Raft log/snapshot/history formats and applies the
// real deterministic FSM. It deliberately omits a running Raft instance so tests
// can distinguish an appended entry from a committed/applied entry precisely.
type offlineBootstrapDisk struct {
	dir       string
	nodeID    string
	log       *raftbolt.BoltStore
	snapshots *raft.FileSnapshotStore
	fsm       *machine
	index     uint64
}

func newOfflineBootstrapDisk(t *testing.T) *offlineBootstrapDisk {
	t.Helper()
	d := &offlineBootstrapDisk{dir: t.TempDir(), nodeID: uuid.NewString()}
	var err error
	d.log, err = raftbolt.New(raftbolt.Options{Path: filepath.Join(d.dir, "raft.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.log.Close() })
	history, err := openHistory(filepath.Join(d.dir, "history"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.Close() })
	d.fsm = &machine{image: image{Version: FormatVersion, Monitors: make(map[string]Monitor)}, history: history}
	d.snapshots, err = raft.NewFileSnapshotStore(d.dir, 3, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(d.dir, "identity.json"), nodeIdentity{Version: FormatVersion, ID: d.nodeID, Initialized: true}); err != nil {
		t.Fatal(err)
	}
	d.appendNoop(t)
	d.command(t, Command{Kind: "recover", At: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)}, true)
	return d
}

func (d *offlineBootstrapDisk) command(t *testing.T, command Command, committed bool) Result {
	t.Helper()
	data, err := json.Marshal(envelope{Version: CatalogFormatVersion, Commands: []Command{command}})
	if err != nil {
		t.Fatal(err)
	}
	d.index++
	entry := &raft.Log{Index: d.index, Term: 1, Type: raft.LogCommand, Data: data}
	if err := d.log.StoreLog(entry); err != nil {
		t.Fatal(err)
	}
	if !committed {
		return Result{}
	}
	response := d.fsm.Apply(entry)
	results, ok := response.([]Result)
	if !ok || len(results) != 1 {
		t.Fatalf("fixture command failed: %v", response)
	}
	return results[0]
}

func (d *offlineBootstrapDisk) bootstrap(t *testing.T, b BootstrapCommand, committed bool) Result {
	t.Helper()
	return d.command(t, Command{Kind: "bootstrap", Bootstrap: &b, At: time.Date(2026, 9, 14, 13, 0, 0, 0, time.UTC)}, committed)
}

func (d *offlineBootstrapDisk) appendNoop(t *testing.T) {
	t.Helper()
	d.index++
	if err := d.log.StoreLog(&raft.Log{Index: d.index, Term: 1, Type: raft.LogNoop}); err != nil {
		t.Fatal(err)
	}
}

func (d *offlineBootstrapDisk) snapshotAndCompact(t *testing.T) {
	t.Helper()
	frozen, err := d.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	sink, err := d.snapshots.Create(1, d.index, 1, raft.Configuration{}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := frozen.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if err := d.log.DeleteRange(1, d.index); err != nil {
		t.Fatal(err)
	}
}

func (d *offlineBootstrapDisk) stop(t *testing.T) {
	t.Helper()
	if err := errors.Join(d.log.Close(), d.fsm.history.Close()); err != nil {
		t.Fatal(err)
	}
}

func offlineFileDigests(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	files := map[string][32]byte{}
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[relative] = sha256.Sum256(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestOfflineBootstrapRequiresCommittedActivation(t *testing.T) {
	for _, scenario := range []string{
		"log only pending", "rejected activation", "uncommitted activation",
		"activated after pending snapshot", "active after API edits",
		"snapshot Raft no-op ahead of application", "replay after snapshot Raft no-op",
	} {
		t.Run(scenario, func(t *testing.T) {
			d := newOfflineBootstrapDisk(t)
			manifest, records := bootstrapFixture(t, &Store{nodeID: d.nodeID})
			begin := BootstrapCommand{Action: "begin", StageID: manifest.StageID, Manifest: &manifest}
			activate := BootstrapCommand{Action: "activate", StageID: manifest.StageID, Manifest: &manifest}
			if result := d.bootstrap(t, begin, true); result.Err != nil || !result.Allowed {
				t.Fatal(result.Err)
			}
			if result := d.bootstrap(t, BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: 1, Record: &records[0]}, true); result.Err != nil || !result.Allowed {
				t.Fatal(result.Err)
			}
			pending := scenario == "log only pending" || scenario == "rejected activation" || scenario == "uncommitted activation"
			if scenario == "rejected activation" {
				if result := d.bootstrap(t, activate, true); !errors.Is(result.Err, ErrBootstrapConflict) {
					t.Fatal("fixture activation was not rejected", result.Err)
				}
			}
			if scenario == "activated after pending snapshot" || scenario == "active after API edits" {
				d.snapshotAndCompact(t)
			}
			if scenario != "log only pending" && scenario != "rejected activation" {
				if result := d.bootstrap(t, BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: 2, Record: &records[1]}, true); result.Err != nil || !result.Allowed {
					t.Fatal(result.Err)
				}
				if result := d.bootstrap(t, activate, scenario != "uncommitted activation"); scenario != "uncommitted activation" && (result.Err != nil || !result.Allowed) {
					t.Fatal(result.Err)
				}
			}
			if scenario == "active after API edits" {
				// Snapshot no longer has the original input count or digest after
				// an admitted edit. Migration remains permanently active.
				record := catalogRecord(t, &Store{nodeID: d.nodeID}, "Recipient", "after-activation", "recipient-uid", "recipient-rv", "private recipient")
				mutation := CatalogMutation{Create: true, Record: record, OperationID: record.Revision, Actor: "operator"}
				if result := d.command(t, Command{Kind: "catalog", At: record.UpdatedAt, Catalog: &mutation}, true); result.Err != nil || !result.Allowed {
					t.Fatal(result.Err)
				}
				d.snapshotAndCompact(t)
			}
			if scenario == "snapshot Raft no-op ahead of application" || scenario == "replay after snapshot Raft no-op" {
				d.appendNoop(t)
				d.snapshotAndCompact(t)
				if scenario == "replay after snapshot Raft no-op" {
					d.appendNoop(t)
					d.command(t, Command{Kind: "recover", At: time.Date(2026, 9, 14, 14, 0, 0, 0, time.UTC)}, true)
				}
			}
			d.stop(t)
			before := offlineFileDigests(t, d.dir)
			lock, err := LockOffline(d.dir)
			if lock != nil {
				if closeErr := lock.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			if pending {
				if !errors.Is(err, ErrBootstrapPending) || lock != nil {
					t.Fatalf("pending migration admitted as complete backup: lock=%v error=%v", lock != nil, err)
				}
			} else if err != nil || lock == nil {
				t.Fatalf("committed active state rejected: lock=%v error=%v", lock != nil, err)
			}
			// Snapshot-store construction uses a transient permission probe;
			// the application data and retained file inventory must not change.
			if after := offlineFileDigests(t, d.dir); !reflect.DeepEqual(before, after) {
				t.Fatal("offline validation changed persistent state files")
			}
		})
	}
}

func TestOfflineBootstrapRejectsMissingCommittedReplay(t *testing.T) {
	d := newOfflineBootstrapDisk(t)
	manifest, _ := bootstrapFixture(t, &Store{nodeID: d.nodeID})
	d.bootstrap(t, BootstrapCommand{Action: "begin", StageID: manifest.StageID, Manifest: &manifest}, true)
	if err := d.log.DeleteRange(d.index, d.index); err != nil {
		t.Fatal(err)
	}
	d.stop(t)
	lock, err := LockOffline(d.dir)
	if lock != nil {
		_ = lock.Close()
	}
	if err == nil || errors.Is(err, ErrBootstrapPending) {
		t.Fatalf("missing committed command was not detected before deciding migration state: %v", err)
	}
}

func TestOfflineBootstrapActualRaftStoreRejectsIncompleteMigration(t *testing.T) {
	config := testConfig(t)
	store, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manifest, records := bootstrapFixture(t, store)
	if result := bootstrapSubmit(t, store, BootstrapCommand{Action: "begin", StageID: manifest.StageID, Manifest: &manifest}); result.Err != nil || !result.Allowed {
		t.Fatal(result.Err)
	}
	if result := bootstrapSubmit(t, store, BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: 1, Record: &records[0]}); result.Err != nil || !result.Allowed {
		t.Fatal(result.Err)
	}
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := LockOffline(config.Storage.Directory)
	if lock != nil {
		_ = lock.Close()
	}
	if !errors.Is(err, ErrBootstrapPending) || lock != nil {
		t.Fatalf("real Raft pending snapshot admitted as complete backup: lock=%v error=%v", lock != nil, err)
	}
}
