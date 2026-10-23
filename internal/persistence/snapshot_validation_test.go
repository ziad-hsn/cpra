package persistence

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/raft"
)

func TestSnapshotEnvelopeStreamsFleetWithoutMaterialization(t *testing.T) {
	for _, payload := range []string{
		`{"version":1,"installation_id":"version","monitors":{}}`,
		`CPRA-COLLECTION-SNAPSHOT-44
{"version":44,"monitors":{}}
CPRA-COLLECTION-LEDGER` + "\x00\x01",
	} {
		if err := validateSnapshotEnvelope(bytes.NewBufferString(payload)); err != nil {
			t.Fatal(err)
		}
	}
	for _, payload := range []string{`{"version":999}`, `{"version":1,"version":1}`, `{"version":1} trailing`, "CPRA-COLLECTION-SNAPSHOT-44\n{\"version\":44}\nmissing"} {
		if err := validateSnapshotEnvelope(bytes.NewBufferString(payload)); err == nil {
			t.Fatalf("accepted invalid envelope %q", payload)
		}
	}
}

func TestSnapshotListRejectsHiddenMalformedCompletedSnapshot(t *testing.T) {
	store, err := newSnapshotStore(t.TempDir(), 3, false)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := store.Create(1, 10, 1, raft.Configuration{}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write([]byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(store.directory, "1-11-1")
	if err := os.Mkdir(newer, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newer, "meta.json"), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("malformed newer metadata silently disappeared")
	}
}

func TestNewestSnapshotSelectionDoesNotPermitFallback(t *testing.T) {
	store, err := newSnapshotStore(t.TempDir(), 3, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []uint64{10, 11} {
		sink, err := store.Create(1, index, 1, raft.Configuration{}, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(sink).Encode(map[string]any{"version": 1}); err != nil {
			t.Fatal(err)
		}
		if err := sink.Close(); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.List()
	if err != nil || len(all) != 2 {
		t.Fatal(all, err)
	}
	selected, err := (newestSnapshotStore{store}).List()
	if err != nil || len(selected) != 1 || selected[0].Index != 11 {
		t.Fatal(selected, err)
	}
	if err := os.WriteFile(filepath.Join(store.directory, selected[0].ID, "state.bin"), bytes.Repeat([]byte("x"), int(selected[0].Size)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, reader, err := store.Open(selected[0].ID); err == nil {
		reader.Close()
		t.Fatal("corrupt selected snapshot accepted")
	}
	selected, err = (newestSnapshotStore{store}).List()
	if err != nil || len(selected) != 1 || selected[0].Index != 11 {
		t.Fatal("selection fell back after corruption", selected, err)
	}
}
