package persistence

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/raft"
)

func TestSnapshotStoreProtectsTemporaryAndPublishedFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := newSnapshotStore(dir, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := store.Create(raft.SnapshotVersion(1), 10, 1, raft.Configuration{}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Cancel() })
	check := func(suffix string) {
		t.Helper()
		root := filepath.Join(dir, "snapshots")
		checkSnapshotPermissions(t, root, true)
		path := filepath.Join(root, sink.ID()+suffix)
		checkSnapshotPermissions(t, path, true)
		for _, name := range []string{"meta.json", "state.bin"} {
			checkSnapshotPermissions(t, filepath.Join(path, name), false)
		}
	}
	check(".tmp")
	payload := []byte("private snapshot fixture")
	if _, err := sink.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	check("")
	_, read, err := store.Open(sink.ID())
	if err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(read)
	closeErr := read.Close()
	if err != nil || closeErr != nil || !bytes.Equal(actual, payload) {
		t.Fatal("snapshot round trip failed", err, closeErr)
	}
}

func TestSnapshotStoreRejectsLinkedRoot(t *testing.T) {
	dir, target := t.TempDir(), t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, "snapshots")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := newSnapshotStore(dir, 3, false); err == nil {
		t.Fatal("followed a linked snapshot root")
	}
}
