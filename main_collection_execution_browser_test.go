package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

// A private stopped admission seeds this fixture. The normal owner/controller,
// TLS routes and embedded SPA perform execution and result reading. Public
// activation and cleanup are separate qualification gates.
func TestMainManagementCollectionExecutionBrowser(t *testing.T) {
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
	original := seedMainCollectionExecution(t, f)
	client, stop := startMainManagement(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	before, err := client.Operations.WaitExecutionResult(ctx, original.ID)
	if err != nil || before.Data.Committed == nil || *before.Data.Committed != 1 || before.Data.Applied == nil || *before.Data.Applied != 1 || len(before.Data.Items) != 1 {
		t.Fatal("normal owner did not retain applied execution", err)
	}
	certificatePEM, err := os.ReadFile(f.settings.Management.TLS.CertFile)
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
	cmd := exec.CommandContext(ctx, node, filepath.Join(root, "scripts/dashboard/verify_collection_execution_browser.cjs"))
	cmd.Env = append(os.Environ(), "CPRA_BROWSER_ORIGIN=https://"+f.options.webAddr, "CPRA_BROWSER_CERT_PIN="+base64.StdEncoding.EncodeToString(pin[:]), "CPRA_BROWSER_OPERATION="+original.ID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("execution result browser failed: %v\n%s", err, output)
	}
	var report struct {
		OperationID string `json:"operationID"`
		ResultID    string `json:"resultID"`
		Writes      int    `json:"writes"`
		Reads       int    `json:"reads"`
	}
	if len(output) > 64<<10 || json.Unmarshal(output, &report) != nil || report.OperationID != original.ID || report.ResultID != original.Activation.ID || report.Writes != 0 || report.Reads < 2 {
		t.Fatal("browser returned an invalid result identity or caused a mutation")
	}
	t.Logf("normal execution browser evidence: %s", output)
	stop()
	f.options.webAddr = availableLocalAddress(t)
	restarted, stopRestarted := startMainManagement(t, f)
	after, err := restarted.Operations.ExecutionResult(ctx, original.ID, cpra.ExecutionResultPageOptions{})
	if err != nil || !reflect.DeepEqual(before.Data.ExecutionResult, after.Data.ExecutionResult) || !reflect.DeepEqual(before.Data.Items, after.Data.Items) {
		t.Fatal("restart changed original execution result", err)
	}
	stopRestarted()
}
