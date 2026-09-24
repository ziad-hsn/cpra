package main

import (
	"bytes"
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
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

// TestMainManagementCollectionBrowser qualifies local file preparation and
// ephemeral preview and staged validation through the embedded dashboard and
// normal TLS/Raft owner. It verifies one original result across owner restart.
// It does not qualify collection activation, large imports, or provider accounts.
func TestMainManagementCollectionBrowser(t *testing.T) {
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
	fixture := newMainManagementFixture(t)
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	const secretMarker = "browser-import-private-target-never-retained"
	private := filepath.Dir(fixture.options.runtimeFile)
	files := []struct{ name, text string }{
		{"a-private-import-monitor.yaml", "apiVersion: cpra.io/v2\nkind: Monitor\nmetadata:\n  id: browser-import-monitor\nspec:\n  enabled: false\n  check:\n    interval: 60s\n    timeout: 5s\n    driver:\n      type: http\n      config: {}\n      credentialRefs:\n        url: browser-import-secret\n"},
		{"z-private-import-secret.json", `{"apiVersion":"cpra.io/v2","kind":"Credential","metadata":{"id":"browser-import-secret"},"spec":{"value":"` + target.URL + "/" + secretMarker + `"}}`},
		{"zz-private-malformed.yaml", "apiVersion: cpra.io/v2\nkind: Credential\nspec: [" + secretMarker + "\n"},
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		path := filepath.Join(private, file.name)
		if err := os.WriteFile(path, []byte(file.text), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	filePaths, err := json.Marshal(paths)
	if err != nil {
		t.Fatal(err)
	}
	client, stop := startMainManagement(t, fixture)
	assertMainCollectionBrowserState(t, client, "")
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
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join(root, "scripts/dashboard/verify_collection_browser.cjs"))
	cmd.Env = append(os.Environ(), "CPRA_BROWSER_ORIGIN=https://"+fixture.options.webAddr, "CPRA_BROWSER_CERT_PIN="+base64.StdEncoding.EncodeToString(pin[:]), "CPRA_BROWSER_FILES="+string(filePaths), "CPRA_BROWSER_PRIVATE_MARKER="+secretMarker)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("normal startup collection browser failed: %v\n%s", err, output)
	}
	var browserReport struct {
		OperationID string `json:"operationID"`
		ResultID    string `json:"resultID"`
	}
	if len(output) > 64<<10 || json.Unmarshal(output, &browserReport) != nil || browserReport.OperationID == "" || browserReport.ResultID == "" {
		t.Fatal("browser did not return a bounded original result identity")
	}
	t.Logf("completed browser checks before SDK reconciliation and restart:\n%s", output)
	original := assertMainCollectionBrowserState(t, client, browserReport.OperationID)
	if !strings.HasPrefix(original, browserReport.OperationID+"/"+browserReport.ResultID+"/") {
		t.Fatal("SDK result identity differs from the original browser result")
	}
	stop()
	fixture.options.webAddr = availableLocalAddress(t)
	restarted, stopRestarted := startMainManagement(t, fixture)
	recovered := assertMainCollectionBrowserState(t, restarted, browserReport.OperationID)
	if recovered != original {
		t.Fatal("restart changed original validation identity")
	}
	stopRestarted()
	if calls.Load() != 0 {
		t.Fatal("browser validation or restart invoked the proposed monitor")
	}
	// Original result metadata is retained. Resource contents remain encrypted;
	// local source names and private provider values never enter plaintext state.
	if err := filepath.WalkDir(fixture.settings.Storage.Directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		defer clear(data)
		for _, value := range []string{secretMarker, "private-import-"} {
			if bytes.Contains(data, []byte(value)) {
				t.Error("private browser input reached plaintext durable state")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(root, "internal/httpserver/assets/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("embedded index sha256=%x; compiler=%s; platform=%s/%s; target requests=%d; active catalog empty; one original sealed validation preserved across restart", sha256.Sum256(index), runtime.Version(), runtime.GOOS, runtime.GOARCH, calls.Load())
}

func assertMainCollectionBrowserState(t *testing.T, client *cpra.Client, id string) string {
	t.Helper()
	monitors, err := client.Monitors.List(t.Context(), cpra.ListOptions{Limit: 100})
	if err != nil || monitors == nil || len(monitors.Data.Items) != 0 || monitors.Data.NextCursor != "" {
		t.Fatal("browser preview changed active monitors", err)
	}
	credentials, err := client.Credentials.List(t.Context(), cpra.ListOptions{Limit: 100})
	if err != nil || credentials == nil || len(credentials.Data.Items) != 0 || credentials.Data.NextCursor != "" {
		t.Fatal("browser preview changed active credentials", err)
	}
	if id == "" {
		operations, err := client.Operations.List(t.Context(), cpra.ListOptions{Limit: 100})
		if err != nil || operations == nil || len(operations.Data.Items) != 0 || operations.Data.NextCursor != "" {
			t.Fatal("unexpected initial operation receipts", err)
		}
		return ""
	}
	// Resolve the actual original handle from the owner-visible list and detail,
	// then compare the same result before and after the normal owner restart.
	operations, err := client.Operations.List(t.Context(), cpra.ListOptions{Limit: 100})
	if err != nil || operations == nil || len(operations.Data.Items) != 1 || operations.Data.NextCursor != "" || operations.Data.Items[0].ID != id || operations.Data.Items[0].State != "validated" {
		t.Fatal("browser original operation was absent from its owner's list", err)
	}
	operation, err := client.Operations.Get(t.Context(), id)
	if err != nil || operation == nil || operation.Data.ID != id {
		t.Fatal("browser original operation was not readable", err)
	}
	op := operation.Data
	if !reflect.DeepEqual(operations.Data.Items[0], op) {
		t.Fatal("owner list and detail disagree on original collection metadata")
	}
	if op.State != "validated" || op.Validated == nil || !*op.Validated || op.Committed == nil || *op.Committed != 0 || op.Applied == nil || *op.Applied != 0 {
		t.Fatal("browser operation claimed activation or lacked original verdict")
	}
	result, err := client.Operations.Validation(t.Context(), op.ID, cpra.ValidationPageOptions{Limit: 100})
	if err != nil || result == nil || !result.Data.Summary.Valid || result.Data.ItemCount != 2 || len(result.Data.Items) != 2 || result.Data.NextCursor != "" || result.Data.ContentDigest != op.ContentDigest {
		t.Fatal("SDK did not observe browser's original sealed result", err)
	}
	return op.ID + "/" + result.Data.Summary.ResultID + "/" + result.Data.Summary.Digest
}
