//go:build linux || darwin

package localadmin

import (
	"os"
	"path/filepath"
	"testing"
)

func backupCollectionRoot(t *testing.T) string {
	t.Helper()
	// The native temporary directory can traverse /var on macOS. Only resolve
	// this test-owned root; supplied key and installation paths still reject links.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
