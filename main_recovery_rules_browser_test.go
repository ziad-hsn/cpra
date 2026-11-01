package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
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

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestMainRecoveryRulesBrowser(t *testing.T) {
	module, node, browser := os.Getenv("CPRA_BROWSER_MODULE"), os.Getenv("CPRA_BROWSER_NODE"), os.Getenv("CPRA_BROWSER_EXECUTABLE")
	if module == "" {
		if os.Getenv("CPRA_BROWSER_REQUIRED") == "1" {
			t.Fatal("required browser tooling missing")
		}
		t.Skip("explicit browser tooling required")
	}
	for _, p := range []string{module, node, browser} {
		if !filepath.IsAbs(p) {
			t.Fatal("browser paths must be absolute")
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
	fixture := newMainManagementFixture(t)
	var checks, effects atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/count":
			_ = json.NewEncoder(w).Encode(map[string]int64{"checks": checks.Load(), "effects": effects.Load()})
		case "/recover":
			effects.Add(1)
			w.WriteHeader(500)
		default:
			checks.Add(1)
			w.WriteHeader(503)
		}
	}))
	defer target.Close()
	client, stop := startMainManagement(t, fixture)
	defer stop()
	cred, err := client.Credentials.Create(t.Context(), api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "rule-receiver"}, Spec: api.CredentialSpec{Value: api.Pointer(target.URL + "/recover")}})
	if err != nil {
		t.Fatal(err)
	}
	waitMainOperation(t, client, cred.OperationID)
	config, _ := json.Marshal(map[string]string{"url": target.URL + "/health"})
	action := api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{"method":"POST","timeout":"2s"}`), CredentialRefs: api.Pointer(map[string]string{"url": "rule-receiver"})}
	created, err := client.Monitors.Create(t.Context(), api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "browser-rules", Name: api.Pointer("Browser rules")}, Spec: api.MonitorSpec{Enabled: api.Pointer(false), Check: api.CheckSpec{Driver: api.DriverConfig{Type: "http", Config: config}, Interval: "250ms", Timeout: "1s", UnhealthyThreshold: api.Pointer(int64(2)), HealthyThreshold: api.Pointer(int64(2))}, Recovery: &api.RecoverySpec{TaxonomyVersion: "1", MaxAttempts: api.Pointer(int64(3)), Rules: []api.RecoveryRule{{ID: "do-not-restart-auth", Match: api.FailureMatch{Codes: []string{"unexpected_http_status"}, HTTPStatuses: []int{401, 403}}, None: true}, {ID: "recover-unavailable", Match: api.FailureMatch{Codes: []string{"unexpected_http_status"}, HTTPStatuses: []int{502}}, Driver: &action}}}}})
	if err != nil {
		t.Fatal(err)
	}
	waitMainOperation(t, client, created.OperationID)
	raw, err := os.ReadFile(fixture.settings.Management.TLS.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("certificate missing")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join(root, "scripts/dashboard/verify_recovery_rules_browser.cjs"))
	cmd.Env = append(os.Environ(), "CPRA_BROWSER_ORIGIN=https://"+fixture.options.webAddr, "CPRA_BROWSER_TARGET="+target.URL, "CPRA_BROWSER_CERT_PIN="+base64.StdEncoding.EncodeToString(pin[:]))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rules browser: %v\n%s", err, output)
	}
	if checks.Load() < 2 || effects.Load() != 1 {
		t.Fatal("independent invocation counts", checks.Load(), effects.Load())
	}
	m, err := client.Monitors.Get(t.Context(), "browser-rules")
	if err != nil {
		t.Fatal(err)
	}
	if m.Data.Status.RecoveryDecision == nil || m.Data.Status.RecoveryDecision.Selected == nil || m.Data.Status.RecoveryDecision.Selected.RuleID != "recover-unavailable" {
		t.Fatal("committed selection unavailable")
	}
	t.Logf("compiler=%s platform=%s/%s checks=%d effects=%d\n%s", runtime.Version(), runtime.GOOS, runtime.GOARCH, checks.Load(), effects.Load(), output)
}
