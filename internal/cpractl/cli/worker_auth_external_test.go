//go:build externaljobs

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/localadmin"
)

func TestLocalWorkerAuthCLIRealPolicyAndSafeOutput(t *testing.T) {
	state, tokens := localAuthTestPaths(t)
	if _, err := localadmin.AdministerAuthentication(t.Context(), localadmin.AuthenticationRequest{Action: "bootstrap", DataDirectory: state, PrincipalID: "operator", Role: "operator", TokenOutput: filepath.Join(tokens, "operator.token")}); err != nil {
		t.Fatal(err)
	}
	grants := filepath.Join(tokens, "grants.json")
	if err := os.WriteFile(grants, []byte(`{"grants":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	var remoteCalls atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { remoteCalls.Add(1) }))
	t.Cleanup(remote.Close)
	var outputs []string
	invoke := func(args ...string) localadmin.WorkerAuthenticationReport {
		t.Helper()
		command := append([]string{"--server", remote.URL, "local", "worker-auth"}, args...)
		command = append(command, "--data-dir", state, "--actor", "operator")
		stdout, stderr, err := runLocalAuth(t, command...)
		outputs = append(outputs, stdout, stderr)
		var report localadmin.WorkerAuthenticationReport
		if err != nil || json.Unmarshal([]byte(stdout), &report) != nil {
			t.Fatal("worker CLI did not return safe structured metadata", err)
		}
		return report
	}
	firstPath, secondPath := filepath.Join(tokens, "worker.token"), filepath.Join(tokens, "worker-next.token")
	issued := invoke("issue", "worker-a", "--grants-file", grants, "--token-output", firstPath)
	if issued.Outcome != "committed" || len(issued.Workers) != 1 || issued.Workers[0].UID == "" {
		t.Fatal("worker issuance lost committed identity")
	}
	rotated := invoke("rotate", "worker-a", "--token-output", secondPath, "--expires-at", "never")
	if rotated.Workers[0].UID != issued.Workers[0].UID || rotated.Workers[0].GrantRevision != issued.Workers[0].GrantRevision || rotated.Workers[0].CredentialRevision == issued.Workers[0].CredentialRevision {
		t.Fatal("CLI rotation changed identity/grants or retained credential revision")
	}
	set := invoke("set-grants", "worker-a", "--grants-file", grants)
	if set.TokenOutput != "" || set.Workers[0].CredentialRevision != rotated.Workers[0].CredentialRevision {
		t.Fatal("grant change issued a new credential")
	}
	revoked := invoke("revoke", "worker-a")
	listed := invoke("list")
	if !listed.Workers[0].Revoked || listed.Revision != revoked.Revision || listed.Outcome != "observed" || remoteCalls.Load() != 0 {
		t.Fatal("worker CLI inspection/revocation did not remain local and authoritative")
	}
	stdout, stderr, err := runLocalAuth(t, "local", "worker-auth", "issue", "different", "--data-dir", state, "--actor", "operator", "--grants-file", grants, "--token-output", firstPath)
	if err == nil || stdout != "" || stderr != "" {
		t.Fatal("existing worker output was accepted or printed")
	}
	combined := strings.Join(outputs, "\n")
	for _, path := range []string{firstPath, secondPath} {
		token, err := os.ReadFile(path)
		if err != nil || len(token) != 44 || strings.Contains(combined, strings.TrimSpace(string(token))) {
			t.Fatal("worker CLI lost or printed its token", err)
		}
		clear(token)
	}
	if strings.Contains(combined, "token_sha256") || strings.Contains(combined, "TokenSHA256") || strings.Contains(combined, "restored_token") {
		t.Fatal("worker CLI printed token verifiers")
	}
}

func TestLocalWorkerAuthCLIRejectsImplicitOrUnsupportedInputs(t *testing.T) {
	state, tokens := localAuthTestPaths(t)
	grants, output := filepath.Join(tokens, "grants.json"), filepath.Join(tokens, "worker.token")
	for _, args := range [][]string{
		{"issue", "worker", "--grants-file", grants, "--token-output", output},
		{"issue", "worker", "--actor", "operator", "--token-output", output},
		{"issue", "worker", "--actor", "operator", "--grants-file", grants},
		{"issue", "worker", "--actor", "operator", "--grants-file", grants, "--token-output", "-"},
		{"issue", "worker", "--actor", "operator", "--grants-file", grants, "--token-output", output, "--expires-at", "private-invalid-expiry"},
		{"rotate", "worker", "--actor", "operator", "--token-output", output, "--grants-file", grants},
		{"rotate", "worker", "--actor", "operator", "--token-output", output, "--overlap", "10m"},
		{"set-grants", "worker", "--actor", "operator", "--grants-file", grants, "--token-output", output},
		{"revoke", "worker", "--actor", "operator", "--expires-at", "never"},
		{"list", "worker", "--actor", "operator"},
		{"bootstrap", "worker"},
	} {
		command := append([]string{"local", "worker-auth"}, args...)
		command = append(command, "--data-dir", state)
		stdout, _, err := runLocalAuth(t, command...)
		if err == nil || stdout != "" || strings.Contains(err.Error(), "private-invalid-expiry") {
			t.Fatal("invalid worker CLI request caused a policy operation or leaked input", err)
		}
	}
	if entries, err := os.ReadDir(state); err != nil || len(entries) != 0 {
		t.Fatal("invalid worker CLI initialized a store", err)
	}
	if _, err := os.Stat(tokens); !os.IsNotExist(err) {
		t.Fatal("invalid worker CLI created credential files")
	}
}
