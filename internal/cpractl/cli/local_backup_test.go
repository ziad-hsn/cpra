package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func TestLocalBackupCLIRequiresIndependentAuthenticationAndResetsRestore(t *testing.T) {
	state, tokens := localAuthTestPaths(t)
	if _, err := localadmin.AdministerAuthentication(t.Context(), localadmin.AuthenticationRequest{Action: "bootstrap", DataDirectory: state, PrincipalID: "operator", Role: "operator", TokenOutput: filepath.Join(tokens, "initial")}); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(filepath.Dir(state), "backup-keys", "authentication.key")
	if _, err := secureconfig.GenerateLocalKeyFile(t.Context(), secureconfig.KeyFileOptions{Path: key, DataDirectory: state}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(server.Close)
	backup, restored := state+"-backup", state+"-restored"
	var outputs []string
	invoke := func(args ...string) error {
		t.Helper()
		all := append([]string{"--server", server.URL, "local"}, args...)
		stdout, stderr, err := runLocalAuth(t, all...)
		outputs = append(outputs, stdout, stderr)
		return err
	}
	if err := invoke("backup", "--data-dir", state, "--output", backup); err == nil || !strings.Contains(err.Error(), "backup-auth-key") {
		t.Fatal("CLI backup omitted independent key", err)
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing key published backup", err)
	}
	if err := invoke("backup", "--data-dir", state, "--output", backup, "--backup-auth-key", key); err != nil {
		t.Fatal(err)
	}
	if err := invoke("restore", "--backup", backup, "--data-dir", restored); err == nil || !strings.Contains(err.Error(), "backup-auth-key") {
		t.Fatal("CLI restore omitted independent key", err)
	}
	if err := invoke("restore", "--backup", backup, "--data-dir", restored, "--backup-auth-key", key); err != nil {
		t.Fatal(err)
	}
	report, err := localadmin.AdministerAuthentication(t.Context(), localadmin.AuthenticationRequest{Action: "list", DataDirectory: restored})
	if err != nil || !report.ResetRequired {
		t.Fatal("authenticated restore retained previous API authority", err)
	}
	raw, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	if strings.Contains(strings.Join(outputs, "\n"), string(raw)) {
		t.Fatal("CLI printed backup authentication key")
	}
	if calls.Load() != 0 {
		t.Fatal("offline backup/restore contacted configured API")
	}
	command, _, err := NewRootCommand().Find([]string{"local", "service", "update"})
	if err != nil || command.Flags().Lookup("backup-auth-key") == nil {
		t.Fatal("service update cannot receive pre-stop backup authority", err)
	}
}
