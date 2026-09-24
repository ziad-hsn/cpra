package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Explicit browser qualification through the normal application entrypoint and
// compiled webhook driver. Only the designated local fixture receives effects.
func TestMainManagementRecoveryReviewBrowser(t *testing.T) {
	module := os.Getenv("CPRA_BROWSER_MODULE")
	if module == "" {
		if os.Getenv("CPRA_BROWSER_REQUIRED") == "1" {
			t.Fatal("required browser harness is missing CPRA_BROWSER_MODULE")
		}
		t.Skip("set explicit installed browser paths for normal-startup action verification")
	}
	node, browser := os.Getenv("CPRA_BROWSER_NODE"), os.Getenv("CPRA_BROWSER_EXECUTABLE")
	for name, path := range map[string]string{"Playwright": module, "Node": node, "Chrome": browser} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s requires an absolute installed path", name)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(name, err)
		}
	}
	fixture := newMainManagementFixture(t)
	var checks, recoveries atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/count":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int64{"checks": checks.Load(), "recoveries": recoveries.Load()})
		case "/recover":
			recoveries.Add(1)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		default:
			checks.Add(1)
			w.WriteHeader(503)
		}
	}))
	defer target.Close()
	client, stop := startMainManagement(t, fixture)
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	credential, err := client.Credentials.Create(t.Context(), api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "browser-recovery-target"}, Spec: api.CredentialSpec{Value: api.Pointer(target.URL + "/recover")}})
	if err != nil {
		t.Fatal(err)
	}
	waitMainOperation(t, client, credential.OperationID)
	config, _ := json.Marshal(map[string]string{"url": target.URL + "/health"})
	created, err := client.Monitors.Create(t.Context(), api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "browser-recovery", Name: api.Pointer("Browser recovery")}, Spec: api.MonitorSpec{Enabled: api.Pointer(true), Check: api.CheckSpec{Interval: "250ms", Timeout: "1s", UnhealthyThreshold: api.Pointer(int64(10000)), HealthyThreshold: api.Pointer(int64(1)), Driver: api.DriverConfig{Type: "http", Config: config}}, Recovery: &api.RecoverySpec{Driver: api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{"method":"POST","timeout":"2s"}`), CredentialRefs: api.Pointer(map[string]string{"url": "browser-recovery-target"})}, MaxAttempts: api.Pointer(int64(3)), Cooldown: api.Pointer(api.Duration("250ms"))}}})
	if err != nil {
		t.Fatal(err)
	}
	waitMainOperation(t, client, created.OperationID)
	certificatePEM, err := os.ReadFile(fixture.settings.Management.TLS.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certificatePEM)
	if block == nil {
		t.Fatal("missing fixture certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	evidenceDir := filepath.Join(root, "bin/verification/main-browser-actions")
	if err := os.MkdirAll(evidenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join(root, "scripts/dashboard/verify_actions_browser.cjs"))
	cmd.Env = append(os.Environ(), "CPRA_BROWSER_ORIGIN=https://"+fixture.options.webAddr, "CPRA_BROWSER_TARGET="+target.URL, "CPRA_BROWSER_CERT_PIN="+base64.StdEncoding.EncodeToString(pin[:]), "CPRA_BROWSER_EVIDENCE_DIR="+evidenceDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("normal startup action browser failed: %v\n%s", err, output)
	}
	if checks.Load() < 2 || recoveries.Load() != 1 {
		t.Fatal("real target counts failed", checks.Load(), recoveries.Load())
	}
	page, err := client.Actions.List(t.Context(), cpra.ListOptions{MonitorID: "browser-recovery"})
	if err != nil || len(page.Data.Items) != 1 {
		t.Fatal("action read missing", err)
	}
	actionID := page.Data.Items[0].ID
	stop()
	stop = nil
	client, stop = startMainManagement(t, fixture)
	restored, err := client.Actions.Get(t.Context(), actionID)
	if err != nil || restored.Data.State != "unknown" || restored.Data.Held || restored.Data.Review == nil || restored.Data.Review.Resolution != api.Accepted || restored.Data.Review.Actor != "team/oncall" {
		t.Fatal("browser review failed normal restart recovery", err)
	}
	before := checks.Load()
	deadline := time.Now().Add(5 * time.Second)
	for checks.Load() < before+4 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if checks.Load() < before+4 || recoveries.Load() != 1 {
		t.Fatal("reviewed action repeated or checks failed after restart")
	}
	index, err := os.ReadFile(filepath.Join(root, "internal/httpserver/assets/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	var browserEvidence json.RawMessage
	if json.Unmarshal(output, &browserEvidence) != nil {
		t.Fatalf("invalid browser evidence: %s", output)
	}
	result, _ := json.MarshalIndent(map[string]any{"result": "passed", "source_scope": "private working tree; this test does not attest the complete source identity", "compiler": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "embedded_index_sha256": fmtSHA256(index), "actual_local_checks": checks.Load(), "actual_local_recovery_requests": recoveries.Load(), "normal_restart_preserved_review": true, "browser": browserEvidence}, "", "  ")
	if err := os.WriteFile(filepath.Join(evidenceDir, "result.json"), result, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("normal startup/restart action browser passed: %s", result)
}

func fmtSHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
