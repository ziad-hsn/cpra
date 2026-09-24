package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestMainManagementCollectionApplyCLI(t *testing.T) {
	if os.Getenv("CPRA_RUN_CLI_INTEGRATION") != "1" {
		t.Skip("set CPRA_RUN_CLI_INTEGRATION=1 to build and execute the real CLI")
	}
	fixture := newMainManagementFixture(t)
	binary, binaryHash := buildMainCLI(t)
	private := filepath.Dir(fixture.options.runtimeFile)
	tokenPath := filepath.Join(private, "operator.token")
	if err := os.WriteFile(tokenPath, []byte(startupOperatorToken), 0600); err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		checks.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	client, stop := startMainManagement(t, fixture)
	cli := &mainCLIRunner{binary: binary, origin: "https://" + fixture.options.webAddr, ca: fixture.settings.Management.TLS.CertFile, token: tokenPath, secret: "private-cli-collection-value"}
	write := func(name string, resource any) string {
		t.Helper()
		raw, err := json.Marshal(resource)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(raw)
		path := filepath.Join(private, name)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	monitor := api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "collection-cli-service"}, Spec: api.MonitorSpec{
		Enabled: api.Pointer(false), Check: api.CheckSpec{Interval: "250ms", Timeout: "1s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"url": "collection-cli-target"})}},
	}}
	monitorPath := write("monitor.json", monitor)
	credentialPath := write("credential.json", api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "collection-cli-target"}, Spec: api.CredentialSpec{Value: api.Pointer(target.URL + "/" + cli.secret)}})
	dry := cli.success(t, nil, "apply", "-f", monitorPath, "-f", credentialPath, "--dry-run=server", "-o", "json")
	var preview struct{ Valid, Changed bool }
	if json.Unmarshal([]byte(dry.stdout), &preview) != nil || !preview.Valid || !preview.Changed || dry.stderr != "" {
		t.Fatal("native collection dry-run did not return proposed changes")
	}
	before, err := client.Operations.List(t.Context(), cpra.ListOptions{})
	if err != nil || len(before.Data.Items) != 0 {
		t.Fatal("dry-run allocated an operation", err)
	}
	applied := cli.success(t, nil, "apply", "-f", monitorPath, "-f", credentialPath, "-o", "json")
	admission := decodeMainCLI[api.Operation](t, applied.stdout)
	if admission.ID == "" || admission.ItemCount == nil || *admission.ItemCount != 2 || admission.Uploaded == nil || *admission.Uploaded != 2 || !strings.Contains(applied.stderr, admission.ID) {
		t.Fatal("native apply lost original inventory admission")
	}
	waited := cli.success(t, nil, "wait", "operation/"+admission.ID, "--timeout=12s", "-o", "json")
	result := decodeMainCLI[api.Operation](t, waited.stdout)
	if result.ID != admission.ID || result.ContentDigest != admission.ContentDigest || result.ExecutionResult == nil || result.ExecutionResult.State != "ready" || result.ExecutionResult.Summary == nil || result.ExecutionResult.Summary.Outcome != "completed" || result.ExecutionResult.Summary.Accepted != 2 || result.ExecutionResult.Summary.ChildApplied != 2 || len(result.Items) != 2 {
		t.Fatal("native wait did not observe complete controller-applied collection", waited.stdout)
	}
	if result.Items[0].ID != "Monitor/collection-cli-service" || result.Items[1].ID != "Credential/collection-cli-target" || *result.Items[0].InputOrdinal != 1 || *result.Items[1].InputOrdinal != 2 || *result.Items[0].PlanOrdinal <= *result.Items[1].PlanOrdinal {
		t.Fatal("dependency execution changed original input order or skipped plan ordering")
	}
	read, err := client.Monitors.Get(t.Context(), monitor.Metadata.ID)
	if err != nil || read.Data.Spec.Enabled == nil || *read.Data.Spec.Enabled || read.Data.Spec.Check.Driver.CredentialRefs == nil || (*read.Data.Spec.Check.Driver.CredentialRefs)["url"] != "collection-cli-target" {
		t.Fatal("normal controller did not project the original monitor", err)
	}
	page := cli.success(t, nil, "get", "operation", admission.ID, "--results", "--limit=1", "-o", "json")
	first := decodeMainCLI[api.Operation](t, page.stdout)
	if len(first.Items) != 1 || first.NextCursor == "" || first.Items[0].ID != result.Items[0].ID {
		t.Fatal("native result read did not preserve bounded original-order pagination")
	}
	second := decodeMainCLI[api.Operation](t, cli.success(t, nil, "get", "operation", admission.ID, "--results", "--limit=1", "--cursor", first.NextCursor, "-o", "json").stdout)
	if len(second.Items) != 1 || second.NextCursor != "" || second.Items[0].ID != result.Items[1].ID {
		t.Fatal("native continuation changed the original retained result")
	}
	stop()
	fixture.options.webAddr = availableLocalAddress(t)
	restarted, stopRestarted := startMainManagement(t, fixture)
	cli.origin = "https://" + fixture.options.webAddr
	after := decodeMainCLI[api.Operation](t, cli.success(t, nil, "wait", "operation/"+admission.ID, "--timeout=12s", "-o", "json").stdout)
	if !reflect.DeepEqual(result.ExecutionResult, after.ExecutionResult) || !reflect.DeepEqual(result.Items, after.Items) {
		t.Fatal("restart changed original immutable execution result")
	}
	restored, err := restarted.Monitors.Get(t.Context(), monitor.Metadata.ID)
	if err != nil || restored.Data.Metadata.UID != read.Data.Metadata.UID || restored.ResourceVersion != read.ResourceVersion {
		t.Fatal("restart changed catalog identity", err)
	}
	stopRestarted()
	if checks.Load() != 0 {
		t.Fatal("disabled collection monitor invoked a provider")
	}
	scanMainCLIPrivateState(t, fixture.settings.Storage.Directory, cli.secret)
	t.Logf("native collection apply evidence: cli_sha256=%s calls=%d public_activation=true dry_run_allocations=0 controller_applied=2 retained_pages=2 owner_restarted=true provider_calls=%d", binaryHash, cli.calls, checks.Load())
}
