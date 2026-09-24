package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Exercise the native entrypoint's exit contract, not just Cobra Execute.
// The real CLI calls the normal TLS/Raft owner; proposed checks remain inactive.
func TestMainManagementRealCLIDiff(t *testing.T) {
	if os.Getenv("CPRA_RUN_CLI_INTEGRATION") != "1" {
		t.Skip("set CPRA_RUN_CLI_INTEGRATION=1 to build and execute the real CLI")
	}
	fixture := newMainManagementFixture(t)
	tool, binaryHash := buildMainCLI(t)
	private := filepath.Dir(fixture.options.runtimeFile)
	operatorPath, readerPath := filepath.Join(private, "operator.token"), filepath.Join(private, "reader.token")
	for path, token := range map[string]string{operatorPath: startupOperatorToken, readerPath: startupReaderToken} {
		if err := os.WriteFile(path, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
	}
	logPath, restoreDiagnostics := captureMainCLIDiagnostics(t)
	var checks atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		checks.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	client, stop := startMainManagement(t, fixture)
	cli := &mainCLIRunner{binary: tool, origin: "https://" + fixture.options.webAddr, ca: fixture.settings.Management.TLS.CertFile, token: operatorPath, secret: "private-cli-diff-credential"}
	config, _ := json.Marshal(map[string]string{"url": target.URL})
	created, err := client.Monitors.Create(t.Context(), api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "preflight-service", Name: api.Pointer("Original disabled service")}, Spec: api.MonitorSpec{
		Enabled: api.Pointer(false), Check: api.CheckSpec{Interval: "250ms", Timeout: "1s", Driver: api.DriverConfig{Type: "http", Config: config}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitMainOperation(t, client, created.OperationID)
	write := func(name string, value any) string {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(data)
		path := filepath.Join(private, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	assertExit := func(result mainCLIResult, want int) {
		t.Helper()
		got := 0
		if result.err != nil {
			var exit *exec.ExitError
			if !errors.As(result.err, &exit) {
				t.Fatal("native diff did not exit normally", result.err)
			}
			got = exit.ExitCode()
		}
		if got != want {
			t.Fatalf("native diff exit=%d want=%d: %s", got, want, result.stderr)
		}
		if want < 2 && result.stderr != "" {
			t.Fatal("successful native diff printed an error banner", result.stderr)
		}
		if strings.Contains(result.stdout, "identityKey") || strings.Contains(result.stdout, "sourceFingerprint") || strings.Contains(result.stdout, "contentDigest") {
			t.Fatal("native diff exposed a private inventory commitment")
		}
	}
	type report struct {
		Valid   bool `json:"valid"`
		Changed bool `json:"changed"`
		Items   []struct {
			ID, Outcome, OldVersion string
			Source                  struct {
				File           string
				Document, Item int
			}
		}
		Errors []struct{ ID, Reason string }
	}
	original := created.Data
	original.Status = api.MonitorStatus{}
	originalPath := write("unchanged.json", original)
	unchanged := cli.invoke(t, nil, "diff", "-f", originalPath, "-o", "json")
	assertExit(unchanged, 0)
	unchangedReport := decodeMainCLI[report](t, unchanged.stdout)
	if !unchangedReport.Valid || unchangedReport.Changed || len(unchangedReport.Items) != 1 || unchangedReport.Items[0].Outcome != "unchanged" {
		t.Fatal("native diff did not observe an unchanged monitor")
	}

	candidate := original
	candidate.Metadata.Name = api.Pointer("Proposed native diff monitor")
	candidate.Spec.Enabled = api.Pointer(true)
	candidate.Spec.Check.Driver = api.DriverConfig{Type: "http", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"url": "preflight-target"})}
	candidatePath := write("monitor.json", candidate)
	credentialPath := write("credential.json", api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "preflight-target"}, Spec: api.CredentialSpec{Value: api.Pointer(target.URL + "/" + cli.secret)}})
	different := cli.invoke(t, nil, "diff", "-f", candidatePath, "-f", credentialPath, "-o", "json")
	assertExit(different, 1)
	proposal := decodeMainCLI[report](t, different.stdout)
	if !proposal.Valid || !proposal.Changed || len(proposal.Items) != 2 || proposal.Items[0].ID != "Monitor/preflight-service" || proposal.Items[0].Outcome != "update" || proposal.Items[0].OldVersion != created.ResourceVersion || proposal.Items[1].ID != "Credential/preflight-target" || proposal.Items[1].Outcome != "create" || proposal.Items[1].Source.File != credentialPath || proposal.Items[1].Source.Document != 1 || proposal.Items[1].Source.Item != 1 {
		t.Fatal("native multi-file diff lost identity, dependencies or local attribution")
	}
	bad := filepath.Join(private, "invalid-final.yaml")
	if err := os.WriteFile(bad, []byte("spec: ["+cli.secret), 0600); err != nil {
		t.Fatal(err)
	}
	malformed := cli.invoke(t, nil, "diff", "-f", candidatePath, "-f", bad)
	assertExit(malformed, 2)
	if malformed.stdout != "" {
		t.Fatal("malformed final input produced a partial diff")
	}
	missing := write("missing-reference.json", api.Recipient{APIVersion: api.APIVersion, Kind: "Recipient", Metadata: api.Metadata{ID: "preflight-missing"}, Spec: api.RecipientSpec{EndpointRefs: []string{"absent"}}})
	rejected := cli.invoke(t, nil, "diff", "-f", missing, "-o", "json")
	assertExit(rejected, 2)
	rejection := decodeMainCLI[report](t, rejected.stdout)
	if rejection.Valid || len(rejection.Errors) != 1 || rejection.Errors[0].ID != "Recipient/preflight-missing" || rejection.Errors[0].Reason != "missingReference" {
		t.Fatal("native diff omitted authoritative graph rejection")
	}
	reader := *cli
	reader.calls, reader.token = 0, readerPath
	denied := reader.invoke(t, nil, "diff", "-f", candidatePath)
	assertExit(denied, 2)
	if denied.stdout != "" || !strings.Contains(denied.stderr, "permission denied") {
		t.Fatal("reader diff did not preserve authorization failure")
	}
	assertMainPreflightUnchanged(t, client, original, created.OperationID)
	stop()
	fixture.options.webAddr = availableLocalAddress(t)
	restarted, stopRestarted := startMainManagement(t, fixture)
	assertMainPreflightUnchanged(t, restarted, original, created.OperationID)
	stopRestarted()
	if checks.Load() != 0 {
		t.Fatal("native diff or restart invoked proposed checks")
	}
	scanMainCLIPrivateState(t, fixture.settings.Storage.Directory, cli.secret)
	restoreDiagnostics()
	logs, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{cli.secret, startupOperatorToken, startupReaderToken} {
		if strings.Contains(string(logs), value) {
			t.Fatal("native diff exposed private input in application diagnostics")
		}
	}
	writeMainCLIEvidence(t, map[string]any{"status": "passed", "time": time.Now().UTC(), "test": t.Name(), "compiler": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH,
		"cli_sha256": binaryHash, "native_cli_calls": cli.calls + reader.calls, "exit_codes_verified": []int{0, 1, 2}, "provider_calls": checks.Load(),
		"retained_operation_receipts": 1, "new_diff_operation_receipts": 0, "owner_restarted": true,
		"boundaries": []string{"normal startup with TLS/Raft", "native CLI executable", "no activation", "no process crash", "local target only", "private candidate only"}}, logs)
}
