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
	"strings"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

func TestMainManagementCollectionReselectionBrowser(t *testing.T) {
	runMainCollectionReselectionBrowser(t, false)
}

func TestMainManagementCollectionContinuationBrowser(t *testing.T) {
	runMainCollectionReselectionBrowser(t, true)
}

func runMainCollectionReselectionBrowser(t *testing.T, activate bool) {
	t.Helper()
	module := os.Getenv("CPRA_BROWSER_MODULE")
	if module == "" {
		if os.Getenv("CPRA_BROWSER_REQUIRED") == "1" {
			t.Fatal("required browser harness is missing CPRA_BROWSER_MODULE")
		}
		t.Skip("set explicit installed browser paths for the native browser campaign")
	}
	node, browser := os.Getenv("CPRA_BROWSER_NODE"), os.Getenv("CPRA_BROWSER_EXECUTABLE")
	for _, path := range []string{module, node, browser} {
		if !filepath.IsAbs(path) {
			t.Fatal("browser campaign requires absolute installed paths")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	f := newMainManagementFixture(t)
	client, stop := startMainManagement(t, f)
	id, raw := seedMainCollectionReselection(t, client)
	paths := make([]string, len(raw))
	for n, name := range []string{"a-first.yaml", "b-second.yaml", "z-empty.yaml"} {
		paths[n] = filepath.Join(filepath.Dir(f.options.runtimeFile), name)
		if err := os.WriteFile(paths[n], raw[n], 0600); err != nil {
			t.Fatal(err)
		}
	}
	encoded, _ := json.Marshal(paths)
	certPEM, err := os.ReadFile(f.settings.Management.TLS.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("missing TLS fixture certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, node, "scripts/dashboard/verify_collection_reselection_browser.cjs")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "NODE_TLS_REJECT_UNAUTHORIZED=") && !strings.HasPrefix(value, "NODE_EXTRA_CA_CERTS=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "NODE_EXTRA_CA_CERTS="+f.settings.Management.TLS.CertFile, "CPRA_BROWSER_ORIGIN=https://"+f.options.webAddr, "CPRA_BROWSER_CERT_PIN="+base64.StdEncoding.EncodeToString(pin[:]), "CPRA_BROWSER_FILES="+string(encoded), "CPRA_BROWSER_OPERATION="+id)
	if activate {
		command.Env = append(command.Env, "CPRA_BROWSER_CONTINUE=1")
	} else {
		command.Env = append(command.Env, "CPRA_BROWSER_CONTINUE=0")
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("reselection browser failed: %v\n%s", err, output)
	}
	var report struct {
		OperationID string `json:"operationID"`
		Resumes     int    `json:"resumes"`
		Activations int    `json:"activations"`
	}
	activationCount := 0
	if activate {
		activationCount = 1
	}
	if len(output) > 64<<10 || json.Unmarshal(output, &report) != nil || report.OperationID != id || report.Resumes != 1 || report.Activations != activationCount {
		t.Fatalf("browser did not report exactly one original resume: %q", output)
	}
	operation, err := client.Operations.Get(t.Context(), id)
	state, count := "uploading", int64(0)
	if activate {
		state, count = "completed", 2
	}
	if err != nil || operation.Data.State != state || operation.Data.Uploaded == nil || *operation.Data.Uploaded != 2 || operation.Data.Committed == nil || *operation.Data.Committed != count || operation.Data.Applied == nil || *operation.Data.Applied != count {
		t.Fatal("browser did not reach the expected original operation outcome", err)
	}
	credentials, err := client.Credentials.List(t.Context(), cpra.ListOptions{Limit: 3})
	if err != nil || len(credentials.Data.Items) != int(count) {
		t.Fatal("active credentials disagree with explicit activation", err)
	}
	stop()
	t.Logf("normal-startup TLS/Raft reselection browser: %s", output)
}
