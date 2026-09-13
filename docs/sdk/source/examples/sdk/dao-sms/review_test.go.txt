//go:build externaljobs

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalProviderTokenRejectsNULWithoutDisclosure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("sensitive\x00value"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(path, true); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe token error: %v", err)
	}
}
