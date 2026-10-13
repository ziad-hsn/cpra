//go:build linux || darwin

package persistence

import (
	"os"
	"testing"
)

func collectionGCTestPrivate(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func collectionGCTestWide(t *testing.T, path string, directory bool) {
	t.Helper()
	mode := os.FileMode(0644)
	if directory {
		mode = 0750
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
