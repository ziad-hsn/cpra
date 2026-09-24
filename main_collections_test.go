package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

// This exercises public file freezing and preflight through the normal TLS/Raft
// application, then restarts that owner. It is deliberately not an activation,
// process-crash, production-provider or large-collection qualification.
func TestMainManagementCollectionPreflightDoesNotActivate(t *testing.T) {
	fixture := newMainManagementFixture(t)
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	client, stop := startMainManagement(t, fixture)
	ctx := context.Background()
	config, _ := json.Marshal(map[string]string{"url": target.URL})
	original := api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "preflight-service", Name: api.Pointer("Original disabled service")}, Spec: api.MonitorSpec{
		Enabled: api.Pointer(false), Check: api.CheckSpec{Interval: "250ms", Timeout: "1s", Driver: api.DriverConfig{Type: "http", Config: config}},
	}}
	created, err := client.Monitors.Create(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	waitMainOperation(t, client, created.OperationID)
	before, err := client.Operations.List(ctx, cpra.ListOptions{Limit: 100})
	if err != nil || before == nil || len(before.Data.Items) != 1 || before.Data.NextCursor != "" {
		t.Fatal("expected the single original creation receipt", err)
	}

	private := filepath.Dir(fixture.options.runtimeFile)
	secretMarker := "private-preflight-target-never-persisted"
	secret := target.URL + "/" + secretMarker
	logPath := filepath.Join(private, "notification-must-not-run.log")
	logConfig, _ := json.Marshal(map[string]string{"file": logPath})
	candidate := created.Data
	candidate.Metadata.Name = api.Pointer("Proposed enabled service")
	candidate.Spec.Enabled = api.Pointer(true)
	candidate.Spec.Check.Driver = api.DriverConfig{Type: "http", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"url": "preflight-target"})}
	candidate.Spec.Notifications = api.Pointer(map[string]api.AlertRule{"red": {NotifyType: api.Pointer("log"), GroupRef: api.Pointer("preflight-group")}})
	candidate.Status = api.MonitorStatus{}
	values := []any{
		candidate, // Dependencies deliberately occur in later files.
		api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "preflight-target"}, Spec: api.CredentialSpec{Value: &secret}},
		api.NotificationEndpoint{APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: "preflight-log"}, Spec: api.DriverConfig{Type: "log", Config: logConfig}},
		api.Recipient{APIVersion: api.APIVersion, Kind: "Recipient", Metadata: api.Metadata{ID: "preflight-contact"}, Spec: api.RecipientSpec{EndpointRefs: []string{"preflight-log"}}},
		api.NotificationGroup{APIVersion: api.APIVersion, Kind: "NotificationGroup", Metadata: api.Metadata{ID: "preflight-group"}, Spec: api.NotificationGroupSpec{RecipientRefs: []string{"preflight-contact"}}},
	}
	var sources []collection.Source
	for i, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(private, "private-source-"+string(rune('a'+i))+".json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		clear(raw)
		sources = append(sources, collection.File(path))
	}
	frozen, err := collection.Freeze(ctx, sources, collection.Options{TempDir: private})
	if err != nil {
		t.Fatal("freeze complete multi-file input", err)
	}
	defer frozen.Close()
	preflight, err := collection.Preflight(ctx, client.Operations, frozen)
	if err != nil || preflight == nil || !preflight.Valid || preflight.ContentDigest != frozen.Digest() || len(preflight.Items) != len(values) {
		t.Fatal("normal application did not validate the complete staged union", err)
	}
	changes := map[string]string{}
	for _, item := range preflight.Items {
		if item.Committed == nil || *item.Committed || item.Applied == nil || *item.Applied || item.NewVersion != "" {
			t.Fatal("preflight reported active changes")
		}
		if item.ID == "Monitor/preflight-service" && item.OldVersion != created.ResourceVersion {
			t.Fatal("preflight did not describe the originally observed monitor version")
		}
		changes[item.ID] = item.Outcome
	}
	expectedChanges := map[string]string{
		"Monitor/preflight-service": "update", "Credential/preflight-target": "create",
		"NotificationEndpoint/preflight-log": "create", "Recipient/preflight-contact": "create",
		"NotificationGroup/preflight-group": "create",
	}
	if len(changes) != len(expectedChanges) {
		t.Fatal("preflight diff omitted or misclassified desired identities")
	}
	for id, outcome := range expectedChanges {
		if changes[id] != outcome {
			t.Fatal("preflight diff omitted or misclassified a desired identity", id)
		}
	}
	raw, _ := json.Marshal(preflight)
	for _, privateValue := range []string{secretMarker, target.URL, logPath, "private-source-", "identityKey", "sourceFingerprint"} {
		if bytes.Contains(raw, []byte(privateValue)) {
			t.Fatal("preflight leaked private input")
		}
	}
	clear(raw)
	assertMainPreflightUnchanged(t, client, created.Data, before.Data.Items[0].ID)

	// An invalid final file is rejected locally before any preflight request.
	badPath := filepath.Join(private, "malformed-final.yaml")
	if err := os.WriteFile(badPath, []byte("spec: [unterminated"), 0600); err != nil {
		t.Fatal(err)
	}
	if bad, err := collection.Freeze(ctx, append(append([]collection.Source{}, sources...), collection.File(badPath)), collection.Options{TempDir: private}); err == nil || bad != nil {
		if bad != nil {
			_ = bad.Close()
		}
		t.Fatal("malformed final input was accepted")
	}
	// A schema-valid omitted reference needs authoritative server validation.
	missing := api.Recipient{APIVersion: api.APIVersion, Kind: "Recipient", Metadata: api.Metadata{ID: "preflight-missing"}, Spec: api.RecipientSpec{EndpointRefs: []string{"no-such-endpoint"}}}
	missingRaw, _ := json.Marshal(missing)
	rejected, err := collection.Freeze(ctx, []collection.Source{collection.Reader("private-missing-reference.json", bytes.NewReader(missingRaw))}, collection.Options{TempDir: private})
	clear(missingRaw)
	if err != nil {
		t.Fatal("freeze locally valid reference", err)
	}
	defer rejected.Close()
	invalid, err := collection.Preflight(ctx, client.Operations, rejected)
	if !errors.Is(err, collection.ErrPreflightRejected) || invalid == nil || invalid.Valid {
		t.Fatal("missing reference was not rejected by preflight", err)
	}
	if len(invalid.Items) != 1 || invalid.Items[0].ID != "Recipient/preflight-missing" || invalid.Items[0].Outcome != "invalid" || invalid.Items[0].Committed == nil || *invalid.Items[0].Committed || invalid.Items[0].Applied == nil || *invalid.Items[0].Applied || len(invalid.Errors) != 1 || invalid.Errors[0].Reason != "missingReference" || invalid.Errors[0].Field != "items[0].resource" {
		t.Fatal("preflight did not attribute the authoritative missing reference")
	}
	assertMainPreflightUnchanged(t, client, created.Data, before.Data.Items[0].ID)
	stop()

	fixture.options.webAddr = availableLocalAddress(t)
	restarted, stopRestarted := startMainManagement(t, fixture)
	assertMainPreflightUnchanged(t, restarted, created.Data, before.Data.Items[0].ID)
	stopRestarted()
	if providerCalls.Load() != 0 {
		t.Fatal("preflight or restart invoked the proposed monitor")
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight opened its proposed notification destination")
	}
	if err := filepath.WalkDir(fixture.settings.Storage.Directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		defer clear(data)
		for _, value := range []string{secretMarker, "private-source-", "Proposed enabled service"} {
			if bytes.Contains(data, []byte(value)) {
				t.Error("preflight input reached durable state")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertMainPreflightUnchanged(t *testing.T, client *cpra.Client, original api.Monitor, operationID string) {
	t.Helper()
	ctx := context.Background()
	got, err := client.Monitors.Get(ctx, original.Metadata.ID)
	if err != nil || got == nil || got.Data.Metadata.UID != original.Metadata.UID || got.Data.Metadata.ResourceVersion != original.Metadata.ResourceVersion || got.Data.Metadata.Name == nil || *got.Data.Metadata.Name != "Original disabled service" || got.Data.Spec.Enabled == nil || *got.Data.Spec.Enabled {
		t.Fatal("preflight changed the active monitor", err)
	}
	credentials, err := client.Credentials.List(ctx, cpra.ListOptions{Limit: 100})
	if err != nil || credentials == nil || len(credentials.Data.Items) != 0 {
		t.Fatal("preflight admitted a credential", err)
	}
	endpoints, err := client.NotificationEndpoints.List(ctx, cpra.ListOptions{Limit: 100})
	if err != nil || endpoints == nil || len(endpoints.Data.Items) != 0 {
		t.Fatal("preflight admitted an endpoint", err)
	}
	contacts, err := client.Recipients.List(ctx, cpra.ListOptions{Limit: 100})
	if err != nil || contacts == nil || len(contacts.Data.Items) != 0 {
		t.Fatal("preflight admitted a recipient", err)
	}
	groups, err := client.NotificationGroups.List(ctx, cpra.ListOptions{Limit: 100})
	if err != nil || groups == nil || len(groups.Data.Items) != 0 {
		t.Fatal("preflight admitted a group", err)
	}
	operations, err := client.Operations.List(ctx, cpra.ListOptions{Limit: 100})
	if err != nil || operations == nil || len(operations.Data.Items) != 1 || operations.Data.Items[0].ID != operationID || strings.TrimSpace(operations.Data.NextCursor) != "" {
		t.Fatal("preflight allocated a durable operation", err)
	}
}
