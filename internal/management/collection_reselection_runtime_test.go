package management

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectionReselectionRuntimeMemoryOwnsPrivateParentUntilJoin(t *testing.T) {
	f := newReselectionProofFixture(t, 1, nil, nil)
	unused := filepath.Join(t.TempDir(), "unused-memory-store")
	m, err := StartCollectionReselectionManager(t.Context(), f.catalog, "memory", unused, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(m.root.directory)
	t.Cleanup(func() { m.BeginStop(); _ = m.Wait(context.Background()) })
	if parent == unused {
		t.Fatal("memory staging used the lexical state boundary")
	}
	if _, err := os.Stat(unused); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("memory staging created the configured state directory", err)
	}
	if err := checkStartupDirectory(parent); err != nil {
		t.Fatal("runtime parent is not private", err)
	}
	s := managerUploadSources(t, m, f, f.raw)
	if s.RawBytes == 0 {
		t.Fatal("fixture did not stage input")
	}
	m.BeginStop()
	if err := m.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.Done():
	default:
		t.Fatal("joined runtime owner retained an open Done")
	}
	if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("joined memory owner retained temporary state", err)
	}
	head, ok, err := f.store.CollectionGet(f.head.ID)
	if err != nil || !ok || head.Uploaded != 1 {
		t.Fatal("shutdown changed the original upload", err)
	}
}

func TestCollectionReselectionRuntimeCleanupPreservesUnexpectedFiles(t *testing.T) {
	f := newReselectionProofFixture(t, 0, nil, nil)
	m, err := StartCollectionReselectionManager(t.Context(), f.catalog, "memory", "", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(m.root.directory)
	foreign := filepath.Join(parent, "unowned")
	if err := os.WriteFile(foreign, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	m.BeginStop()
	if err := m.Wait(t.Context()); !errors.Is(err, errReselectionSpool) {
		t.Fatal("unexpected cleanup succeeded", err)
	}
	if m.Err() == nil {
		t.Fatal("cleanup failure was hidden from supervision")
	}
	for _, path := range []string{foreign, filepath.Join(m.root.directory, reselectionOwnershipFile)} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatal("cleanup removed unapproved contents", err)
		}
	}
}

func TestCollectionReselectionRuntimeCleanupRejectsReplacedParent(t *testing.T) {
	f := newReselectionProofFixture(t, 0, nil, nil)
	m, err := StartCollectionReselectionManager(t.Context(), f.catalog, "memory", "", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(m.root.directory)
	original := parent + "-moved"
	if err := os.Rename(parent, original); err != nil {
		m.BeginStop()
		_ = m.Wait(t.Context())
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent); _ = os.RemoveAll(original) })
	m.BeginStop()
	if err := m.Wait(t.Context()); !errors.Is(err, errReselectionSpool) {
		t.Fatal("replaced parent accepted", err)
	}
	if _, err := os.Lstat(parent); err != nil {
		t.Fatal("replacement parent removed", err)
	}
}
