package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/localadmin"
)

func localAuthTestPaths(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("native Linux local administration evidence")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	return state, filepath.Join(root, "tokens")
}

func runLocalAuth(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.ExecuteContext(t.Context())
	return stdout.String(), stderr.String(), err
}

func TestLocalAuthCLIRealPolicyAndSecretFreeOutput(t *testing.T) {
	state, tokens := localAuthTestPaths(t)
	var remoteCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { remoteCalls.Add(1) }))
	t.Cleanup(server.Close)
	var outputs []string
	invoke := func(args ...string) localadmin.AuthenticationReport {
		t.Helper()
		command := append([]string{"--server", server.URL, "local", "auth"}, args...)
		command = append(command, "--data-dir", state)
		stdout, stderr, err := runLocalAuth(t, command...)
		outputs = append(outputs, stdout, stderr)
		var report localadmin.AuthenticationReport
		if err != nil || json.Unmarshal([]byte(stdout), &report) != nil {
			t.Fatal("local auth command did not return safe structured metadata", err)
		}
		return report
	}
	firstPath := filepath.Join(tokens, "operator.token")
	first := invoke("bootstrap", "team/oncall", "--role", "operator", "--token-output", firstPath)
	if first.Outcome != "committed" || first.Epoch == "" || len(first.Principals) != 1 || first.Principals[0].ExpiresAt != nil {
		t.Fatal("CLI bootstrap invented expiry or lost the named principal")
	}
	expiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339)
	readerPath := filepath.Join(tokens, "reader.token")
	second := invoke("issue", "team/observer", "--role", "reader", "--token-output", readerPath, "--expires-at", expiry)
	if second.Epoch != first.Epoch || len(second.Principals) != 2 || second.Principals[0].Role != "reader" || second.Principals[0].ExpiresAt == nil {
		t.Fatal("CLI issue did not retain the requested reader role/expiry")
	}
	nextPath := filepath.Join(tokens, "reader-next.token")
	rotated := invoke("rotate", "team/observer", "--token-output", nextPath)
	if rotated.Principals[0].Role != "reader" || !rotated.Principals[0].ExpiresAt.Equal(*second.Principals[0].ExpiresAt) {
		t.Fatal("CLI rotation silently changed the role or expiry")
	}
	revoked := invoke("revoke", "team/observer")
	listed := invoke("list")
	if !revoked.Principals[0].Revoked || listed.Revision != revoked.Revision || !listed.Principals[0].Revoked || remoteCalls.Load() != 0 {
		t.Fatal("CLI revocation/list did not remain authoritative and entirely local")
	}
	combined := strings.Join(outputs, "\n")
	for _, path := range []string{firstPath, readerPath, nextPath} {
		token, err := os.ReadFile(path)
		if err != nil || len(token) != 44 {
			t.Fatal("CLI token output missing", err)
		}
		if strings.Contains(combined, strings.TrimSpace(string(token))) {
			t.Fatal("CLI printed a bearer token")
		}
		clear(token)
	}
	if strings.Contains(combined, "token_sha256") || strings.Contains(combined, "TokenSHA256") {
		t.Fatal("CLI printed a token verifier")
	}
}

func TestLocalAuthCLIRejectsImplicitInputsAndUnsupportedRoles(t *testing.T) {
	state, tokens := localAuthTestPaths(t)
	output := filepath.Join(tokens, "new.token")
	for _, args := range [][]string{
		{"bootstrap", "named", "--token-output", output},
		{"bootstrap", "named", "--role", "admin", "--token-output", output},
		{"bootstrap", "named", "--role", "operator"},
		{"bootstrap", "named", "--role", "operator", "--token-output", "-"},
		{"bootstrap", "named", "--role", "operator", "--token-output", output, "--expires-at", "1h"},
		{"bootstrap", "named", "--role", "operator", "--token-output", output, "--expires-at", ""},
		{"issue", "legacy-read", "--role", "reader", "--token-output", output},
		{"rotate", "named", "--role", "reader", "--token-output", output},
		{"rotate", "named", "--token-output", output, "--overlap", "10m"},
		{"revoke", "named", "--token-output", output},
		{"list", "named"},
		{"list", "--all"},
	} {
		command := append([]string{"local", "auth"}, args...)
		command = append(command, "--data-dir", state)
		if stdout, _, err := runLocalAuth(t, command...); err == nil || stdout != "" {
			t.Fatal("invalid local auth syntax caused a policy operation")
		}
	}
	if entries, err := os.ReadDir(state); err != nil || len(entries) != 0 {
		t.Fatal("invalid CLI input initialized state")
	}
	if _, err := os.Stat(tokens); !os.IsNotExist(err) {
		t.Fatal("invalid CLI input provisioned token material")
	}
}
