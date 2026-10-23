//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func TestMainTokenFileRejectsUnsafeBootstrapPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-token-location")
	const marker = "disposable-bootstrap-token-must-not-leak"
	if err := os.WriteFile(path, []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0644, 0600} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := loadAuthenticationBootstrap(t.Context(), runtimeconfig.Config{}, runOptions{webAuthFile: path})
		if mode == 0600 {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, secureconfig.ErrTokenFilePolicy) || strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), path) {
			t.Fatal("unsafe source was accepted or disclosed", err)
		}
	}
}
