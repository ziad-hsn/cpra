//go:build linux || darwin

package localadmin

import (
	"os"
	"testing"
)

func backupCollectionRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
