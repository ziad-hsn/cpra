package httpserver

import (
	"bytes"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/cpractl/cli"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCollectionExecutionCLIReadsOneOriginalServerPage(t *testing.T) {
	f, head := executionHTTPFixture(t, 2)
	t.Setenv("CPRA_AUTH_TOKEN", "")
	t.Setenv("CPRA_AUTH_TOKEN_FILE", "")
	t.Setenv("CPRA_CA_FILE", "")
	target, err := url.Parse(f.http.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = f.http.Client().Transport
	var requests atomic.Int32
	transport := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/operations/"+head.ID {
			t.Error("CLI result read sent a mutation or changed operation")
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(transport.Close)
	directory := t.TempDir()
	caFile, tokenFile := filepath.Join(directory, "ca.pem"), filepath.Join(directory, "token")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: transport.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte(managementOperatorToken), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, error) {
		t.Helper()
		command := cli.NewRootCommand()
		var stdout, stderr bytes.Buffer
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		command.SetArgs(append([]string{"--server", transport.URL, "--ca-file", caFile, "--token-file", tokenFile}, args...))
		err := command.ExecuteContext(t.Context())
		return stdout.String(), stderr.String(), err
	}
	before := f.server.cfg.Store.Status().CommittedIndex
	stdout, stderr, err := run("get", "operation", head.ID, "--results", "--limit", "1", "-o", "json")
	var first api.Operation
	if err != nil || requests.Load() != 1 || stderr != "" || api.DecodeResponse([]byte(stdout), &first) != nil || len(first.Items) != 1 || first.NextCursor == "" || first.ExecutionResult == nil || first.ExecutionResult.State != "ready" {
		t.Fatal("CLI first server page failed or collected extra pages", requests.Load(), err, stdout, stderr)
	}
	stdout, stderr, err = run("get", "operation", head.ID, "--results", "--limit", "1", "--cursor", first.NextCursor, "-o", "json")
	var second api.Operation
	if err != nil || requests.Load() != 2 || stderr != "" || api.DecodeResponse([]byte(stdout), &second) != nil || len(second.Items) != 1 || second.NextCursor != "" || !reflect.DeepEqual(first.ExecutionResult, second.ExecutionResult) || *second.Items[0].InputOrdinal != 2 {
		t.Fatal("CLI continuation changed original result or fetched additional pages", requests.Load(), err, stdout, stderr)
	}
	stdout, stderr, err = run("get", "operation", head.ID, "--results", "--limit", "1")
	if err != nil || requests.Load() != 3 || !strings.Contains(stdout, "CONFIGURATION DECISION") || !strings.Contains(stdout, "unattempted") || !strings.HasPrefix(stderr, "Next cursor: ") {
		t.Fatal("CLI table did not expose original result metadata", requests.Load(), err, stdout, stderr)
	}
	if _, _, err := run("get", "operation", head.ID, "--cursor", first.NextCursor); err == nil || requests.Load() != 3 {
		t.Fatal("plain operation detail accepted a result-only continuation")
	}
	if err := os.WriteFile(tokenFile, []byte(managementReaderToken), 0600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = run("get", "operation", head.ID, "--results", "-o", "json")
	if err == nil || requests.Load() != 4 || stdout != "" || strings.Contains(stderr+err.Error(), head.ID) {
		t.Fatal("CLI result read bypassed original collection ownership", requests.Load(), err, stdout, stderr)
	}
	if f.server.cfg.Store.Status().CommittedIndex != before {
		t.Fatal("CLI result reads mutated durable state")
	}
}
