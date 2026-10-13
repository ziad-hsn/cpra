//go:build linux || darwin

package persistence

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCollectionGCRootReplacementAndStrictNames(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	oldPath := f.dir + "-moved"
	if err := os.Rename(f.dir, oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(oldPath, f.dir) })
	if err := os.Mkdir(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.dir) })
	if _, err := r.Step(context.Background(), f.old); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("renamed root used through stale pathname", err)
	}
	for _, name := range []string{"", "generation-00000000-0000-0000-0000-000000000000", strings.ToUpper(f.old), f.old + "/", "generation-{" + strings.TrimPrefix(f.old, "generation-") + "}"} {
		if collectionGCName(name) {
			t.Fatalf("unsafe generation name accepted: %q", name)
		}
	}
	if _, err := newCollectionRetirement("relative", f.guard); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("relative root accepted", err)
	}
}
