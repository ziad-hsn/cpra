package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
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

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// TestMainManagementRealCLI is explicitly opt-in because it builds a separate
// cpractl executable. CPRa uses the normal runCPRa entrypoint in this test process;
// the CLI is a native OS subprocess. All provider effects target local fixtures.
func TestMainManagementRealCLI(t *testing.T) {
	if os.Getenv("CPRA_RUN_CLI_INTEGRATION") != "1" {
		t.Skip("set CPRA_RUN_CLI_INTEGRATION=1 to build and execute the real CLI against normal startup")
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
	const secret = "cli-recovery-destination-must-not-appear-in-plaintext"
	var checks, effects atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/recover/"+secret {
			effects.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error("recovery fixture could not drop its response")
				return
			}
			_ = connection.Close()
			return
		}
		checks.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(target.Close)
	client, stop := startMainManagement(t, fixture)
	cli := &mainCLIRunner{binary: tool, origin: "https://" + fixture.options.webAddr, ca: fixture.settings.Management.TLS.CertFile, token: operatorPath, secret: secret}

	credentialInput, _ := json.Marshal(api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "recovery-destination"}, Spec: api.CredentialSpec{Value: api.Pointer(target.URL + "/recover/" + secret)}})
	createdCredential := cli.success(t, credentialInput, "create", "secret", "-f", "-", "-o", "json")
	clear(credentialInput)
	cli.applied(t, client, createdCredential)
	credential := decodeMainCLI[api.Credential](t, createdCredential.stdout)
	if credential.Spec.Value != nil {
		t.Fatal("real CLI returned a credential value")
	}

	healthConfig, _ := json.Marshal(map[string]string{"url": target.URL + "/health"})
	monitor := api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "cli-managed", Name: api.Pointer("Before CLI edit")}, Spec: api.MonitorSpec{
		Enabled: api.Pointer(false),
		Check:   api.CheckSpec{Interval: "250ms", Timeout: "1s", UnhealthyThreshold: api.Pointer(int64(1)), HealthyThreshold: api.Pointer(int64(1)), Driver: api.DriverConfig{Type: "http", Config: healthConfig}},
		// Leave retry budget available: the unknown-outcome hold, rather than
		// an exhausted attempt limit, must prevent a second external effect.
		Recovery: &api.RecoverySpec{MaxFailures: api.Pointer(int64(1)), MaxAttempts: api.Pointer(int64(3)), Cooldown: api.Pointer("250ms"), Driver: api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{"method":"POST","timeout":"500ms"}`), CredentialRefs: api.Pointer(map[string]string{"url": "recovery-destination"})}},
	}}
	monitorInput, _ := json.Marshal(monitor)
	created := cli.success(t, monitorInput, "create", "monitor", "-f", "-", "-o", "json")
	cli.applied(t, client, created)
	observed := decodeMainCLI[api.Monitor](t, cli.success(t, nil, "get", "monitor/cli-managed", "-o", "json").stdout)
	if observed.Metadata.UID == "" || observed.Spec.Enabled == nil || *observed.Spec.Enabled || checks.Load() != 0 {
		t.Fatal("disabled CLI creation did not reach the owner without checks")
	}
	initialUID := observed.Metadata.UID
	patched := cli.success(t, []byte(`{"metadata":{"name":"Edited with real CLI"}}`), "patch", "monitor/cli-managed", "--patch-file", "-", "--resource-version", observed.Metadata.ResourceVersion, "-o", "json")
	cli.applied(t, client, patched)
	observed = decodeMainCLI[api.Monitor](t, patched.stdout)
	cli.success(t, nil, "describe", "monitor/cli-managed")
	if checks.Load() != 0 || effects.Load() != 0 {
		t.Fatal("CLI reads or disabled configuration edits invoked a provider")
	}

	// A reader can observe but cannot mutate through the same CLI executable.
	reader := *cli
	reader.token = readerPath
	reader.success(t, nil, "get", "secrets", "-o", "json")
	assertMainCLIOperationPages(t, &reader, observed.Metadata.ResourceVersion)
	if checks.Load() != 0 || effects.Load() != 0 {
		t.Fatal("CLI operation list or detail invoked a provider for the disabled monitor")
	}
	denied := reader.invoke(t, []byte(`{"metadata":{"name":"Forbidden"}}`), "patch", "monitor/cli-managed", "--patch-file", "-", "--resource-version", observed.Metadata.ResourceVersion, "-o", "json")
	if denied.err == nil || !strings.Contains(denied.stderr, "permission denied") || denied.stdout != "" {
		t.Fatal("reader CLI mutation was accepted or lost the permission error")
	}

	enabled := cli.success(t, nil, "enable", "monitor/cli-managed", "--resource-version", observed.Metadata.ResourceVersion, "-o", "json")
	cli.applied(t, client, enabled)
	awaitMainCLI(t, func() bool {
		got, err := client.Monitors.Get(t.Context(), "cli-managed")
		return err == nil && got.Data.Status.IncidentID != "" && got.Data.Status.UnknownActions == 1 && effects.Load() == 1
	}, "real failed checks and ambiguous webhook did not produce one held action")
	status, err := client.Monitors.Get(t.Context(), "cli-managed")
	if err != nil {
		t.Fatal(err)
	}
	incidentID := status.Data.Status.IncidentID
	actionPage := decodeMainCLI[api.ActionList](t, cli.success(t, nil, "get", "actions", "--monitor-id", "cli-managed", "--limit", "100", "-o", "json").stdout)
	if len(actionPage.Items) != 1 || actionPage.Items[0].State != "unknown" || !actionPage.Items[0].Held {
		t.Fatal("real CLI action read lost the uncertain provider outcome")
	}
	originalAction := actionPage.Items[0]
	reviewed := cli.success(t, []byte("Provider outcome still being investigated"), "review", "action/"+originalAction.ID, "--review-revision", originalAction.ReviewRevision, "--resolution", "inconclusive", "--reason-file", "-", "-o", "json")
	cli.applied(t, client, reviewed)
	reviewedAction := decodeMainCLI[api.Action](t, reviewed.stdout)
	if reviewedAction.State != "unknown" || !reviewedAction.Held || reviewedAction.Review == nil || reviewedAction.Review.Actor != "team/oncall" || reviewedAction.Review.Resolution != api.Inconclusive {
		t.Fatal("real CLI review changed provider facts or lost the unresolved hold/actor")
	}
	staleReview := cli.invoke(t, []byte("Duplicate review"), "review", "action/"+originalAction.ID, "--review-revision", originalAction.ReviewRevision, "--resolution", "inconclusive", "--reason-file", "-", "-o", "json")
	if staleReview.err == nil || !strings.Contains(staleReview.stderr, "conflict") {
		t.Fatal("duplicate CLI review overwrote a newer assertion")
	}
	denied = reader.invoke(t, []byte("Reader review denied"), "review", "action/"+originalAction.ID, "--review-revision", reviewedAction.ReviewRevision, "--resolution", "inconclusive", "--reason-file", "-", "-o", "json")
	if denied.err == nil || !strings.Contains(denied.stderr, "permission denied") {
		t.Fatal("reader CLI action review was accepted")
	}
	incidentAddress := "incident/" + incidentID
	incident := decodeMainCLI[api.Incident](t, cli.success(t, nil, "get", incidentAddress, "-o", "json").stdout)
	originalIncidentRevision := incident.Revision
	acknowledged := cli.success(t, []byte("Investigating through the native CLI"), "acknowledge", incidentAddress, "--revision", incident.Revision, "--note-file", "-", "-o", "json")
	cli.applied(t, client, acknowledged)
	incident = decodeMainCLI[api.Incident](t, acknowledged.stdout)
	if incident.AcknowledgedBy != "team/oncall" || incident.Revision == originalIncidentRevision {
		t.Fatal("acknowledgment did not record the named actor and new revision")
	}
	duplicate := cli.invoke(t, nil, "acknowledge", incidentAddress, "--revision", originalIncidentRevision, "-o", "json")
	if duplicate.err == nil || !strings.Contains(duplicate.stderr, "conflict") {
		t.Fatal("an old incident revision was replayed by the CLI")
	}
	denied = reader.invoke(t, nil, "acknowledge", incidentAddress, "--revision", incident.Revision, "-o", "json")
	if denied.err == nil || !strings.Contains(denied.stderr, "permission denied") {
		t.Fatal("reader CLI incident control was accepted")
	}
	dismissed := cli.success(t, []byte("Known local integration outage"), "dismiss", incidentAddress, "--revision", incident.Revision, "--reason-file", "-", "-o", "json")
	cli.applied(t, client, dismissed)
	incident = decodeMainCLI[api.Incident](t, dismissed.stdout)
	if !incident.Dismissed {
		t.Fatal("dismissal was not returned as committed")
	}
	cli.success(t, nil, "describe", incidentAddress)
	status, err = client.Monitors.Get(t.Context(), "cli-managed")
	if err != nil {
		t.Fatal(err)
	}
	snoozed := cli.success(t, []byte("Restart verification maintenance"), "snooze", "monitor/cli-managed", "--control-revision", status.Data.Status.ControlRevision, "--for", "5m", "--reason-file", "-", "-o", "json")
	cli.applied(t, client, snoozed)
	assertMainCLIQuiet(t, &checks, &effects)
	status, err = client.Monitors.Get(t.Context(), "cli-managed")
	if err != nil || !status.Data.Status.SnoozedUntil.After(time.Now()) {
		t.Fatal("normal owner did not apply snooze", err)
	}
	snoozedUntil := status.Data.Status.SnoozedUntil
	disabled := cli.success(t, nil, "disable", "monitor/cli-managed", "--resource-version", status.Data.Metadata.ResourceVersion, "-o", "json")
	cli.applied(t, client, disabled)

	// Forward exactly one real conditional mutation, then drop the CLI reply
	// after the real server has returned a committed receipt. This is not a fake
	// HTTP success: the independent SDK later observes that receipt as applied.
	status, err = client.Monitors.Get(t.Context(), "cli-managed")
	if err != nil {
		t.Fatal(err)
	}
	lostVersion := status.Data.Metadata.ResourceVersion
	forwarded := make(chan string, 1)
	var forwardedCalls atomic.Int64
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardedCalls.Add(1)
		request, err := http.NewRequestWithContext(r.Context(), r.Method, cli.origin+r.URL.RequestURI(), r.Body)
		if err != nil {
			t.Error("could not construct the forwarding fixture request")
			return
		}
		request.Header = r.Header.Clone()
		response, err := fixture.client.Do(request)
		if err != nil {
			t.Error("forwarded mutation did not reach normal startup")
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || response.Header.Get("X-CPRa-Admission") != "committed" {
			t.Error("forwarded mutation did not receive committed admission")
		}
		select {
		case forwarded <- response.Header.Get("X-Operation-ID"):
		default:
			t.Error("CLI retried the original forwarded mutation")
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error("could not drop committed CLI reply")
			return
		}
		_ = connection.Close()
	}))
	t.Cleanup(proxy.Close)
	proxyCA := filepath.Join(private, "forwarder.pem")
	if err := os.WriteFile(proxyCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	uncertainCLI := *cli
	uncertainCLI.origin, uncertainCLI.ca = proxy.URL, proxyCA
	uncertain := uncertainCLI.invoke(t, []byte(`{"metadata":{"name":"Committed despite a lost CLI reply"}}`), "patch", "monitor/cli-managed", "--patch-file", "-", "--resource-version", lostVersion, "-o", "json")
	if uncertain.err == nil || !strings.Contains(uncertain.stderr, "unconfirmed") || uncertain.stdout != "" || forwardedCalls.Load() != 1 {
		t.Fatal("lost committed CLI response was retried or reported as a known rejection")
	}
	select {
	case operationID := <-forwarded:
		waitMainOperation(t, client, operationID)
	case <-time.After(time.Second):
		t.Fatal("forwarding fixture lost the actual committed receipt")
	}
	duplicate = cli.invoke(t, []byte(`{"metadata":{"name":"Unintended second edit"}}`), "patch", "monitor/cli-managed", "--patch-file", "-", "--resource-version", lostVersion, "-o", "json")
	if duplicate.err == nil || !strings.Contains(duplicate.stderr, "conflict") {
		t.Fatal("an uncertain committed mutation was applied again using its old version")
	}

	// Prove acknowledgment auditing once, not simply its current summary.
	history := decodeMainCLI[api.EventList](t, cli.success(t, nil, "get", "events", "--monitor-id", "cli-managed", "--limit", "100", "-o", "json").stdout)
	ackEvents := 0
	for _, event := range history.Items {
		if event.Note == "Investigating through the native CLI" {
			ackEvents++
		}
	}
	if ackEvents != 1 {
		t.Fatal("duplicate or missing acknowledgment audit event")
	}
	stop()
	if err := os.Remove(fixture.options.manifest); err != nil {
		t.Fatal(err)
	}
	fixture.options.allowEmpty = false
	fixture.options.webAddr = availableLocalAddress(t)
	restarted, stopRestarted := startMainManagement(t, fixture)
	cli.origin = "https://" + fixture.options.webAddr
	restored := decodeMainCLI[api.Monitor](t, cli.success(t, nil, "get", "monitor/cli-managed", "-o", "json").stdout)
	if restored.Metadata.UID != initialUID || restored.Metadata.Name == nil || *restored.Metadata.Name != "Committed despite a lost CLI reply" || restored.Spec.Enabled == nil || *restored.Spec.Enabled || restored.Status.UnknownActions != 1 || !restored.Status.SnoozedUntil.Equal(snoozedUntil) {
		t.Fatal("restart lost CLI configuration, unknown action hold or snooze")
	}
	restoredIncident := decodeMainCLI[api.Incident](t, cli.success(t, nil, "get", incidentAddress, "-o", "json").stdout)
	if restoredIncident.ID != incident.ID || restoredIncident.AcknowledgedBy != "team/oncall" || !restoredIncident.Dismissed {
		t.Fatal("restart lost the exact incident's acknowledgment or dismissal")
	}
	restoredAction := decodeMainCLI[api.Action](t, cli.success(t, nil, "get", "action/"+originalAction.ID, "-o", "json").stdout)
	if restoredAction.State != "unknown" || !restoredAction.Held || restoredAction.Review == nil || restoredAction.Review.Resolution != api.Inconclusive || restoredAction.Review.Actor != "team/oncall" {
		t.Fatal("restart lost the separately audited inconclusive CLI review")
	}
	restoredCredential := decodeMainCLI[api.Credential](t, cli.success(t, nil, "get", "secret/recovery-destination", "-o", "json").stdout)
	if restoredCredential.Metadata.UID != credential.Metadata.UID || restoredCredential.Spec.Value != nil {
		t.Fatal("restart lost credential identity or write-only protection")
	}
	assertMainCLIQuiet(t, &checks, &effects)
	unsnoozed := cli.success(t, nil, "unsnooze", "monitor/cli-managed", "--control-revision", restored.Status.ControlRevision, "-o", "json")
	cli.applied(t, restarted, unsnoozed)
	assertMainCLIQuiet(t, &checks, &effects)
	restored = decodeMainCLI[api.Monitor](t, cli.success(t, nil, "get", "monitor/cli-managed", "-o", "json").stdout)
	checksBefore := checks.Load()
	enabledAt := time.Now()
	enabled = cli.success(t, nil, "enable", "monitor/cli-managed", "--resource-version", restored.Metadata.ResourceVersion, "-o", "json")
	cli.applied(t, restarted, enabled)
	awaitMainCLI(t, func() bool { return checks.Load() > checksBefore }, "safe health checks did not resume after enable")
	completedChecks := make(map[int64]bool)
	awaitMainCLI(t, func() bool {
		observation, err := restarted.Monitors.Get(t.Context(), "cli-managed")
		if err == nil && observation.Data.Status.LastCheckedAt.After(enabledAt) {
			completedChecks[observation.Data.Status.LastCheckedAt.UnixNano()] = true
		}
		return len(completedChecks) >= 4 && time.Since(enabledAt) >= time.Second
	}, "restart did not commit several complete scheduling cycles before the duplicate-effect check")
	assertEffects := effects.Load()
	if assertEffects != 1 {
		t.Fatal("restart or control changes repeated an uncertain external action")
	}
	stopRestarted()
	if effects.Load() != 1 {
		t.Fatal("shutdown repeated the unknown recovery")
	}
	scanMainCLIPrivateState(t, fixture.settings.Storage.Directory, secret)
	restoreDiagnostics()
	logs, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(logs, []byte(secret)) || bytes.Contains(logs, []byte(startupOperatorToken)) || bytes.Contains(logs, []byte(startupReaderToken)) {
		t.Fatal("plaintext credential or authentication token appeared in application diagnostics")
	}
	evidence := map[string]any{"status": "passed", "compiler": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "cpractl_sha256": binaryHash, "build": []string{"CGO_ENABLED=0", "GOTOOLCHAIN=local", "-trimpath", "-buildvcs=false", "-mod=readonly"}, "workspace": os.Getenv("GOWORK"), "source": "current working tree; no publication", "application_boundary": "normal runCPRa entrypoint in test process with real TLS, Raft and owner loop", "cli_boundary": "fresh native executable subprocesses", "checks_observed": checks.Load(), "unknown_recovery_effects_observed": effects.Load(), "forwarded_uncertain_mutations": forwardedCalls.Load(), "acknowledgment_audit_events": ackEvents, "primary_cli_invocations": cli.calls, "provider_accounts": "none; local HTTP fixtures only", "power_loss_or_process_crash": "not exercised by this graceful-restart fixture"}
	evidence["action_review"] = "inconclusive assertion retained across restart; stale revision and reader rejected; provider facts and hold preserved"
	evidence["v2_audit_reader"] = "real cpractl get events --monitor-id; exact acknowledgment event retained once"
	evidence["v2_operation_reader"] = "real reader cpractl: bounded original monitor pages, identical continuation retry, exact operation lookup, JSON and table output, zero effects while disabled"
	writeMainCLIEvidence(t, evidence, logs)
	t.Logf("real CLI integration passed: compiler=%s platform=%s/%s binary_sha256=%s checks=%d unknown effects=%d", runtime.Version(), runtime.GOOS, runtime.GOARCH, binaryHash, checks.Load(), effects.Load())
}

type mainCLIResult struct {
	stdout, stderr string
	err            error
}

func assertMainCLIOperationPages(t *testing.T, reader *mainCLIRunner, patchedVersion string) {
	t.Helper()
	first := decodeMainCLI[api.OperationList](t, reader.success(t, nil, "get", "operations", "--monitor-id", "cli-managed", "--limit", "1", "-o", "json").stdout)
	if len(first.Items) != 1 || first.NextCursor == "" || first.Snapshot == "" || first.GeneratedAt.IsZero() {
		t.Fatal("native CLI did not return one operation and its original continuation")
	}
	args := []string{"get", "operations", "--monitor-id", "cli-managed", "--limit", "1", "--cursor", first.NextCursor, "-o", "json"}
	secondRaw := reader.success(t, nil, args...).stdout
	second := decodeMainCLI[api.OperationList](t, secondRaw)
	if len(second.Items) != 1 || second.Snapshot != first.Snapshot || !second.GeneratedAt.Equal(first.GeneratedAt) || second.Items[0].ID == first.Items[0].ID || second.NextCursor != "" {
		t.Fatal("native CLI operation continuation changed the snapshot or repeated an identity")
	}
	if retried := reader.success(t, nil, args...).stdout; retried != secondRaw {
		t.Fatal("native CLI original continuation returned a different page on retry")
	}
	for _, receipt := range []api.Operation{first.Items[0], second.Items[0]} {
		if receipt.State != "completed" || receipt.Committed == nil || *receipt.Committed != 1 || receipt.Applied == nil || *receipt.Applied != 1 || len(receipt.Items) != 1 || receipt.Items[0].ID != "cli-managed" {
			t.Fatal("native CLI monitor filter or explicit operation progress disagrees with the owner")
		}
	}
	if second.Items[0].Items[0].NewVersion != patchedVersion {
		t.Fatal("native CLI operation list did not retain the exact patch revision")
	}
	detail := decodeMainCLI[api.Operation](t, reader.success(t, nil, "get", "operations", second.Items[0].ID, "-o", "json").stdout)
	if detail.ID != second.Items[0].ID || len(detail.Items) != 1 || detail.Items[0].NewVersion != patchedVersion {
		t.Fatal("native CLI lookup changed the operation chosen from its page")
	}
	table := reader.success(t, nil, "get", "operations", "--monitor-id", "cli-managed", "--limit", "1")
	if !strings.Contains(table.stdout, first.Items[0].ID) || strings.Contains(table.stdout, second.Items[0].ID) || !strings.Contains(table.stderr, "Next cursor:") {
		t.Fatal("native CLI table omitted continuation or implicitly collected more pages")
	}
}

type mainCLIRunner struct {
	binary, origin, ca, token, secret string
	calls                             int
}

func (c *mainCLIRunner) invoke(t *testing.T, input []byte, args ...string) mainCLIResult {
	t.Helper()
	c.calls++
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, c.binary, append([]string{"--server", c.origin, "--ca-file", c.ca, "--token-file", c.token, "--request-timeout", "5s"}, args...)...)
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	result := mainCLIResult{stdout.String(), stderr.String(), err}
	for _, secret := range []string{c.secret, startupOperatorToken, startupReaderToken} {
		if strings.Contains(result.stdout, secret) || strings.Contains(result.stderr, secret) {
			t.Fatal("real CLI exposed a credential or bearer token")
		}
	}
	return result
}

func (c *mainCLIRunner) success(t *testing.T, input []byte, args ...string) mainCLIResult {
	t.Helper()
	result := c.invoke(t, input, args...)
	if result.err != nil {
		t.Fatalf("real CLI %s failed: %v; %s", args[0], result.err, result.stderr)
	}
	return result
}

func (c *mainCLIRunner) applied(t *testing.T, client *cpra.Client, result mainCLIResult) {
	t.Helper()
	operationID := ""
	for _, line := range strings.Split(result.stderr, "\n") {
		if strings.HasPrefix(line, "Operation: ") {
			fields := strings.Fields(line)
			if len(fields) > 1 {
				operationID = fields[1]
			}
		}
	}
	if operationID == "" {
		var operation api.Operation
		if json.Unmarshal([]byte(result.stdout), &operation) == nil {
			operationID = operation.ID
		}
	}
	if operationID == "" {
		t.Fatal("CLI mutation omitted its operation identity")
	}
	waitMainOperation(t, client, operationID)
	operation := decodeMainCLI[api.Operation](t, c.success(t, nil, "get", "operation", operationID, "-o", "json").stdout)
	if operation.State != "completed" || (operation.Applied == nil || *operation.Applied != 1) {
		t.Fatal("CLI operation observation disagreed with the normal owner")
	}
}

func decodeMainCLI[T any](t *testing.T, raw string) T {
	t.Helper()
	var result T
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal("CLI stdout was not the standalone canonical API response", err)
	}
	return result
}

func awaitMainCLI(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal(message)
		case <-t.Context().Done():
			t.Fatal("normal CLI fixture canceled")
		case <-ticker.C:
		}
	}
}

func assertMainCLIQuiet(t *testing.T, checks, effects *atomic.Int64) {
	t.Helper()
	// Let any check already started before the committed control finish; the
	// second interval must contain no new target work across multiple cadences.
	time.Sleep(300 * time.Millisecond)
	before, actions := checks.Load(), effects.Load()
	time.Sleep(600 * time.Millisecond)
	if checks.Load() != before || effects.Load() != actions {
		t.Fatal("paused/disabled owner continued admitting provider work")
	}
}

func buildMainCLI(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cpractl")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-buildvcs=false", "-mod=readonly", "-o", path, "./cmd/cpractl")
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOFLAGS=-p=2", "GOEXPERIMENT=", "GOAMD64=v1", "GOARM64=v8.0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build native CLI with current test compiler: %v\n%s", err, output)
	}
	binary, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	return path, hex.EncodeToString(digest[:])
}

func captureMainCLIDiagnostics(t *testing.T) (string, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "application.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = file, file
	var restored atomic.Bool
	restore := func() {
		if restored.CompareAndSwap(false, true) {
			os.Stdout, os.Stderr = stdout, stderr
			if err := file.Close(); err != nil {
				t.Error("close application diagnostics", err)
			}
		}
	}
	// Registered before application cleanup, so failure stops the owner before
	// restoring process descriptors. This opt-in test must not run in parallel.
	t.Cleanup(restore)
	return path, restore
}

func scanMainCLIPrivateState(t *testing.T, directory, secret string) {
	t.Helper()
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		defer clear(data)
		if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(startupOperatorToken)) || bytes.Contains(data, []byte(startupReaderToken)) {
			return fmt.Errorf("plaintext credential found in stopped durable state")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeMainCLIEvidence(t *testing.T, evidence map[string]any, logs []byte) {
	t.Helper()
	directory := os.Getenv("CPRA_CLI_EVIDENCE_DIR")
	if directory == "" {
		return
	}
	if !filepath.IsAbs(directory) {
		t.Fatal("CLI evidence directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "result.json"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "application.log"), logs, 0600); err != nil {
		t.Fatal(err)
	}
}
