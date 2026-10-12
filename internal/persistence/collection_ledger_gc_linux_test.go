package persistence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCollectionGCFailedIntentSyncAuthorizesNoUnlink(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	fd, err := unix.Open(f.dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	realDir := r.dir
	badDir := os.NewFile(uintptr(fd), "pinned-root-without-fsync-capability")
	r.dir = badDir
	defer func() { _ = badDir.Close(); r.dir = realDir }()
	if _, err := r.Step(context.Background(), f.old); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("failed directory fsync was not visible", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, f.old, "ledger.db")); err != nil {
		t.Fatal("failed intent publication deleted database", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, collectionGCIntentName)); err != nil {
		t.Fatal("uncertain intent evidence was removed", err)
	}
	_ = r.dir.Close()
	r.dir = realDir
	if step := gcTestStep(t, r, ""); step.Phase != "removed-ledger.db" {
		t.Fatal("durably revalidated intent did not resume", step)
	}
}
