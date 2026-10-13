//go:build linux || darwin

package persistence

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectionAllocatedBytesDistinguishesSparseLength(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "sparse-file"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(16 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("allocated"), (16<<20)-16); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	allocated, available := collectionAllocatedBytes(file)
	info, err := file.Stat()
	if err != nil || !available || allocated <= 0 || allocated >= info.Size() {
		t.Fatalf("sparse allocation was replaced by apparent length: allocation=%d available=%t stat=%v", allocated, available, err)
	}
}
