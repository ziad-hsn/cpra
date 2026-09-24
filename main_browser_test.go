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

// TestMainManagementControlsBrowser uses normal startup, its TLS listener,
// encrypted Raft catalog, owner loop, actual HTTP check driver, and embedded SPA.
// It is explicit opt-in and never downloads tooling or invokes external targets.
func TestMainManagementControlsBrowser(t *testing.T) {
	module := os.Getenv("CPRA_BROWSER_MODULE")
	if module == "" {
		if os.Getenv("CPRA_BROWSER_REQUIRED") == "1" {
			t.Fatal("required browser harness is missing CPRA_BROWSER_MODULE")
		}
		t.Skip("set explicit installed browser paths to run normal-startup browser verification")
	}
	node, browser := os.Getenv("CPRA_BROWSER_NODE"), os.Getenv("CPRA_BROWSER_EXECUTABLE")
	for name, p := range map[string]string{"Playwright": module, "Node": node, "Chrome": browser} {
		if !filepath.IsAbs(p) {
			t.Fatalf("%s requires an absolute installed path", name)
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatal(name, err)
		}
	}
	fixture := newMainManagementFixture(t)
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/count" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(calls.Load())
			return
		}
		calls.Add(1)
		w.WriteHeader(503)
	}))
	defer target.Close()
	client, stop := startMainManagement(t, fixture)
	defer stop()
	config, _ := json.Marshal(map[string]string{"url": target.URL})
	created, err := client.Monitors.Create(t.Context(), api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "browser-controls", Name: api.Pointer("Browser controls")}, Spec: api.MonitorSpec{Enabled: api.Pointer(true), Check: api.CheckSpec{Interval: "250ms", Timeout: "1s", UnhealthyThreshold: api.Pointer(int64(1)), HealthyThreshold: api.Pointer(int64(1)), Driver: api.DriverConfig{Type: "http", Config: config}}}})
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
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, node, filepath.Join(root, "scripts/dashboard/verify_controls_browser.cjs"))
	cmd.Env = append(os.Environ(), "CPRA_BROWSER_ORIGIN=https://"+fixture.options.webAddr, "CPRA_BROWSER_TARGET="+target.URL, "CPRA_BROWSER_CERT_PIN="+base64.StdEncoding.EncodeToString(pin[:]))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("normal startup browser failed: %v\n%s", err, output)
	}
	if calls.Load() == 0 {
		t.Fatal("browser fixture never exercised real HTTP checks")
	}
	index, err := os.ReadFile(filepath.Join(root, "internal/httpserver/assets/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("embedded index sha256=%x; compiler=%s; platform=%s/%s; real target calls=%d\n%s", sha256.Sum256(index), runtime.Version(), runtime.GOOS, runtime.GOARCH, calls.Load(), output)
}
