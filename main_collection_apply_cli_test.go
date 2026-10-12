package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	for _, count := range []int{35, 50} {
		t.Run(fmt.Sprintf("import_%d_monitors", count), func(t *testing.T) {
			testMainMonitorCollectionImport(t, binary, binaryHash, count)
		})
	}
}

func testMainMonitorCollectionImport(t *testing.T, binary, binaryHash string, count int) {
	t.Helper()
	fixture := newMainManagementFixture(t)
	private := filepath.Dir(fixture.options.runtimeFile)
	tokenPath := filepath.Join(private, "operator.token")
	if err := os.WriteFile(tokenPath, []byte(startupOperatorToken), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	client, stop := startMainManagement(t, fixture)
	cli := &mainCLIRunner{binary: binary, origin: "https://" + fixture.options.webAddr, ca: fixture.settings.Management.TLS.CertFile, token: tokenPath, secret: "private-sized-import-marker", processTimeout: 45 * time.Second}
	ids := make([]string, count)
	args := []string{"apply"}
	// Exercise one manifest and team-separated manifests, using the same public
	// existing manifest loader and collection protocol as an operator's import.
	perFile := count
	if count == 50 {
		perFile = 10
	}
	for first := 0; first < count; first += perFile {
		var content strings.Builder
		content.WriteString("monitors:\n")
		for n := first; n < min(count, first+perFile); n++ {
			ids[n] = fmt.Sprintf("import-service-%03d", n)
			fmt.Fprintf(&content, "  - id: %s\n    name: Imported service %d\n    enabled: false\n    pulse_check:\n      type: http\n      interval: 60s\n      timeout: 1s\n      unhealthy_threshold: 1\n      healthy_threshold: 1\n      config:\n        url: %s/health\n", ids[n], n, target.URL)
		}
		path := filepath.Join(private, fmt.Sprintf("team-%03d.yaml", first/perFile))
		if err := os.WriteFile(path, []byte(content.String()), 0600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-f", path)
	}
	args = append(args, "-o", "json")
	started := time.Now()
	admission := decodeMainCLI[api.Operation](t, cli.success(t, nil, args...).stdout)
	if admission.ID == "" || admission.ItemCount == nil || *admission.ItemCount != int64(count) || admission.Uploaded == nil || *admission.Uploaded != int64(count) {
		t.Fatal("sized import did not retain its complete original admission")
	}
	result := decodeMainCLI[api.Operation](t, cli.success(t, nil, "wait", "operation/"+admission.ID, "--timeout=30s", "-o", "json").stdout)
	if result.ID != admission.ID || result.ContentDigest != admission.ContentDigest || result.ExecutionResult == nil || result.ExecutionResult.State != "ready" || result.ExecutionResult.Summary == nil || result.ExecutionResult.Summary.Outcome != "completed" || result.ExecutionResult.Summary.Accepted != int64(count) || result.ExecutionResult.Summary.ChildApplied != int64(count) || len(result.Items) != count {
		t.Fatal("sized import did not complete the original collection with every child applied")
	}
	state, err := client.State(t.Context())
	if err != nil || !state.Data.Ready || !state.Data.ControllerAvailable || !state.Data.ControllerReady || !state.Data.Storage.Available {
		t.Fatal("ordinary import left controller or storage admission unavailable", err)
	}
	identities := make(map[string]api.Metadata, count)
	for _, id := range ids {
		read, err := client.Monitors.Get(t.Context(), id)
		if err != nil || read.Data.Spec.Enabled == nil || *read.Data.Spec.Enabled || read.Data.Metadata.Generation != 1 || read.Data.Status.ObservedGeneration != read.Data.Metadata.Generation {
			t.Fatal("sized import did not project each original monitor exactly once", id, err)
		}
		identities[id] = read.Data.Metadata
	}
	elapsed := time.Since(started)
	stop()
	restarted, stopRestarted := startMainManagement(t, fixture)
	for _, id := range ids {
		read, err := restarted.Monitors.Get(t.Context(), id)
		original := identities[id]
		if err != nil || read.Data.Metadata.UID != original.UID || read.Data.Metadata.ResourceVersion != original.ResourceVersion || read.Data.Status.ObservedGeneration != original.Generation {
			t.Fatal("sized import restart changed original identity or lost owner projection", id, err)
		}
	}
	stopRestarted()
	if calls.Load() != 0 {
		t.Fatal("disabled sized import or reads caused a provider operation", calls.Load())
	}
	t.Logf("sized CLI import evidence: monitors=%d files=%d cli_sha256=%s original_operation=%s applied=%d elapsed_ms=%d owner_restarted=true provider_calls=0", count, (count+perFile-1)/perFile, binaryHash, admission.ID, count, elapsed.Milliseconds())
}
