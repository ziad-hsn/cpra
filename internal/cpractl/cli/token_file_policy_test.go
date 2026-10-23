//go:build !windows

package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAPITokenFilePolicyRejectsBeforeNetwork(t *testing.T) {
	t.Setenv("CPRA_AUTH_TOKEN", "")
	t.Setenv("CPRA_AUTH_TOKEN_FILE", "")
	t.Setenv("CPRA_AUTH_TOKEN_ROOT", "")
	t.Setenv("CPRA_AUTH_TOKEN_READER_GROUP", "")
	t.Setenv("CPRA_AUTH_TOKEN_READER_SID", "")
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true}`))
	}))
	defer server.Close()
	root := t.TempDir()
	path := filepath.Join(root, "private-token-name")
	const marker = "disposable-secret-must-not-appear"
	if err := os.WriteFile(path, []byte(marker), 0644); err != nil {
		t.Fatal(err)
	}
	command, output := newTestCommand(t, server.URL, "health", "--allow-insecure-http", "--token-file", path)
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "unsafe ownership") || strings.Contains(err.Error()+output.String(), marker) || strings.Contains(err.Error(), path) || requests.Load() != 0 {
		t.Fatal("unsafe token sent or diagnostic lost", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0644 {
		t.Fatal("reader changed supplied permissions")
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	command, _ = newTestCommand(t, server.URL, "health", "--allow-insecure-http", "--token-file", path)
	if err := command.Execute(); err != nil || requests.Load() != 1 {
		t.Fatal("protected token unusable", err)
	}
}
