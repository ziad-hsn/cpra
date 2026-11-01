//go:build linux || darwin

package persistence

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCollectionAllocatedBytesDistinguishesSparseLength(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "sparse-file"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("allocated")); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		allocated, available := collectionAllocatedBytes(file)
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !available || allocated != stat.Blocks*512 || allocated <= 0 {
			t.Fatalf("allocation differs from native filesystem: allocation=%d available=%t info=%v", allocated, available, info)
		}
		t.Logf("apparent=%d allocated=%d sparse=%t", info.Size(), allocated, allocated < info.Size())
	}
	// A small unaligned file also detects an incorrect apparent-size fallback
	// on filesystems that fully allocate an extended file instead of holes.
	check()
	if err := file.Truncate(16 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("allocated"), (16<<20)-16); err != nil {
		t.Fatal(err)
	}
	check()
}
