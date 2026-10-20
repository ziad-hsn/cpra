//go:build !windows

package persistence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/raft"
)

func checkSnapshotPermissions(t *testing.T, path string, directory bool) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0600)
	if directory {
		mode = 0700
	}
	if info.IsDir() != directory || info.Mode().Perm() != mode {
		t.Fatalf("snapshot permissions: %s: %v", path, info.Mode())
	}
}

func TestSnapshotAdministrativeOpenPreservesRuntimeOwnership(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "snapshots")
	if err := os.Mkdir(root, 0750); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newSnapshotStore(dir, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(root)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("administration changed runtime directory permissions", err)
	}
	if _, err := store.Create(1, 10, 1, raft.Configuration{}, 0, nil); err == nil {
		t.Fatal("administrative store created a snapshot")
	}
}

func TestSnapshotProtectionRejectsHardLinks(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "original"), filepath.Join(dir, "alias")
	if err := os.WriteFile(first, []byte("unchanged"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Fatal(err)
	}
	if err := protectSnapshotPath(second, false); err == nil {
		t.Fatal("changed permissions through an extra hard link")
	}
	info, err := os.Stat(first)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("rejected path changed original", err)
	}
}
