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
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

// The real file chooser, embedded parser, public API and normal controller own
// every step. No stopped-store seeding or private activation admission is used.
func TestMainManagementCollectionActivationBrowser(t *testing.T) {
	module := os.Getenv("CPRA_BROWSER_MODULE")
	if module == "" {
		if os.Getenv("CPRA_BROWSER_REQUIRED") == "1" {
			t.Fatal("required browser harness is missing CPRA_BROWSER_MODULE")
		}
		t.Skip("set explicit installed browser paths to run normal-startup browser verification")
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
	f := newMainManagementFixture(t)
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	const marker = "private-collection-activation-canary"
	private := filepath.Dir(f.options.runtimeFile)
	files := []struct{ name, content string }{
		{"a-activation-monitor.yaml", "apiVersion: cpra.io/v2\nkind: Monitor\nmetadata:\n  id: activated-monitor\nspec:\n  enabled: false\n  check:\n    interval: 60s\n    timeout: 5s\n    driver:\n      type: http\n      config: {}\n      credentialRefs:\n        url: activated-secret\n"},
		{"z-activation-secret.json", `{"apiVersion":"cpra.io/v2","kind":"Credential","metadata":{"id":"activated-secret"},"spec":{"value":"` + target.URL + "/" + marker + `"}}`},
	}
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = filepath.Join(private, file.name)
		if err := os.WriteFile(paths[i], []byte(file.content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	encodedPaths, err := json.Marshal(paths)
	if err != nil {
		t.Fatal(err)
	}
	client, stop := startMainManagement(t, f)
	certPEM, err := os.ReadFile(f.settings.Management.TLS.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("missing fixture certificate")
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
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join(root, "scripts/dashboard/verify_collection_activation_browser.cjs"))
	cmd.Env = append(os.Environ(), "CPRA_BROWSER_ORIGIN=https://"+f.options.webAddr,
		"CPRA_BROWSER_CERT_PIN="+base64.StdEncoding.EncodeToString(pin[:]),
		"CPRA_BROWSER_FILES="+string(encodedPaths), "CPRA_BROWSER_PRIVATE_MARKER="+marker)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("public activation browser failed: %v\n%s", err, output)
	}
	var report struct {
		OperationID string `json:"operationID"`
		ResultID    string `json:"resultID"`
		Activations int    `json:"activations"`
	}
	if len(output) > 64<<10 || json.Unmarshal(output, &report) != nil || report.OperationID == "" || report.ResultID == "" || report.Activations != 1 {
		t.Fatal("browser did not report one original activation")
	}
	before, err := client.Operations.ExecutionResult(ctx, report.OperationID, cpra.ExecutionResultPageOptions{})
	if err != nil || before.Data.ExecutionResult == nil || before.Data.ExecutionResult.Summary == nil || before.Data.ExecutionResult.Summary.ResultID != report.ResultID ||
		before.Data.State != "completed" || before.Data.Committed == nil || *before.Data.Committed != 2 || before.Data.Applied == nil || *before.Data.Applied != 2 || len(before.Data.Items) != 2 {
		t.Fatal("browser activation did not reach authoritative applied result", err)
	}
	monitor, err := client.Monitors.Get(ctx, "activated-monitor")
	if err != nil || monitor.Data.Metadata.ID != "activated-monitor" {
		t.Fatal("monitor was not created by original operation", err)
	}
	credential, err := client.Credentials.Get(ctx, "activated-secret")
	if err != nil || credential.Data.Metadata.ID != "activated-secret" {
		t.Fatal("cross-file credential dependency was not created", err)
	}
	stop()
	f.options.webAddr = availableLocalAddress(t)
	restarted, stopRestarted := startMainManagement(t, f)
	after, err := restarted.Operations.ExecutionResult(ctx, report.OperationID, cpra.ExecutionResultPageOptions{})
	if err != nil || before.Data.NormalizationProfile != "cpra.file.base.v1" || after.Data.NormalizationProfile != before.Data.NormalizationProfile || !reflect.DeepEqual(before.Data.ExecutionResult, after.Data.ExecutionResult) || !reflect.DeepEqual(before.Data.Items, after.Data.Items) {
		t.Fatal("restart changed the original public activation result", err)
	}
	repeated, err := restarted.Operations.Activate(ctx, report.OperationID)
	if err != nil || repeated.Data.ID != report.OperationID || repeated.Data.NormalizationProfile != before.Data.NormalizationProfile || !reflect.DeepEqual(after.Data.ExecutionResult, repeated.Data.ExecutionResult) {
		t.Fatal("restart retry did not reconcile original completed activation", err)
	}
	stopRestarted()
	if calls.Load() != 0 {
		t.Fatal("disabled imported monitor invoked its target")
	}
	t.Logf("public activation browser evidence: %s; original result retained across normal restart; disabled target calls=%d", output, calls.Load())
}
