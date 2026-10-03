//go:build externaljobs

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
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func mainExternalDriver(t *testing.T, category, version string) api.DriverConfig {
	t.Helper()
	raw, err := json.Marshal(api.ExternalConfig{JobTypeID: "configured-" + category, Version: version,
		Parameters: json.RawMessage(`{"target":"operator-target"}`), CredentialProfile: "worker-local"})
	if err != nil {
		t.Fatal(err)
	}
	return api.DriverConfig{Type: "external", Config: raw}
}

func mainExternalMonitor(t *testing.T, id, category, targetURL string) api.Monitor {
	t.Helper()
	local, err := json.Marshal(map[string]string{"url": targetURL})
	if err != nil {
		t.Fatal(err)
	}
	monitor := api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: id},
		Spec: api.MonitorSpec{Enabled: api.Pointer(false), Check: api.CheckSpec{
			Driver: api.DriverConfig{Type: "http", Config: local}, Interval: "60s", Timeout: "5s"}}}
	driver := mainExternalDriver(t, category, "v1")
	switch category {
	case "check":
		monitor.Spec.Check.Driver = driver
	case "recovery":
		monitor.Spec.Recovery = &api.RecoverySpec{Driver: driver}
	case "notification":
		monitor.Spec.Notifications = api.Pointer(map[string]api.AlertRule{"yellow": {Driver: &driver}})
	}
	return monitor
}

func registerMainExternalTypes(t *testing.T, client *cpra.Client) {
	t.Helper()
	for _, category := range []string{"check", "recovery", "notification"} {
		_, err := client.JobTypes().Create(t.Context(), api.JobType{APIVersion: api.APIVersion, Kind: "JobType",
			Metadata: api.Metadata{ID: "configured-" + category}, Spec: api.JobTypeSpec{Kind: category, Version: "v1",
				Handler: "configured-" + category, ProtocolVersion: "1", Timeout: "5s",
				ParameterSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["target"],"properties":{"target":{"type":"string"}}}`),
				ResultSchema:    json.RawMessage(`{"type":"object"}`)}})
		if err != nil {
			t.Fatal("registered contract", category, err)
		}
	}
}

func TestMainExternalConfigurationCRUDAndAuthorization(t *testing.T) {
	fixture := newMainManagementFixture(t)
	fixture.settings.ExternalJobs.Enabled = true
	writeMainRuntime(t, fixture.options.runtimeFile, fixture.settings)
	client, stop := startMainManagement(t, fixture)
	registerMainExternalTypes(t, client)
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	versions := map[string]string{}
	endpoint, err := client.NotificationEndpoints.Create(t.Context(), api.NotificationEndpoint{
		APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: "managed-worker-endpoint"},
		Spec: mainExternalDriver(t, "notification", "v1")})
	if err != nil {
		t.Fatal("public external endpoint", err)
	}
	for _, category := range []string{"check", "recovery", "notification"} {
		monitor := mainExternalMonitor(t, "managed-"+category, category, target.URL)
		created, err := client.Monitors.Create(t.Context(), monitor)
		if err != nil {
			t.Fatal("public create", category, err)
		}
		patched, err := client.Monitors.Patch(t.Context(), monitor.Metadata.ID, created.ResourceVersion,
			api.MergePatch(`{"metadata":{"name":"Operator label"}}`))
		if err != nil || patched.Data.Metadata.UID != created.Data.Metadata.UID {
			t.Fatal("public patch", category, err)
		}
		if _, err := client.Monitors.Replace(t.Context(), monitor.Metadata.ID, created.ResourceVersion, patched.Data); !errors.Is(err, cpra.ErrConflict) {
			t.Fatal("stale external replacement", err)
		}
		patched.Data.Spec.Tags = api.Pointer([]string{"configured"})
		replaced, err := client.Monitors.Replace(t.Context(), monitor.Metadata.ID, patched.ResourceVersion, patched.Data)
		if err != nil {
			t.Fatal("public replacement", err)
		}
		versions[monitor.Metadata.ID] = replaced.ResourceVersion
		job, err := client.JobTypes().Get(t.Context(), "configured-"+category)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.JobTypes().Delete(t.Context(), job.Data.Metadata.ID, job.ResourceVersion); err == nil {
			t.Fatal("referenced contract deleted")
		}
	}
	reader, err := cpra.New(cpra.Config{BaseURL: "https://" + fixture.options.webAddr, AuthToken: startupReaderToken, HTTPClient: fixture.client})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Monitors.Create(t.Context(), mainExternalMonitor(t, "denied", "check", target.URL))
	var problem *cpra.Error
	if !errors.As(err, &problem) || problem.StatusCode != http.StatusForbidden {
		t.Fatal("reader activated external configuration", err)
	}
	stop()
	client, stop = startMainManagement(t, fixture)
	defer stop()
	retainedEndpoint, err := client.NotificationEndpoints.Get(t.Context(), endpoint.Data.Metadata.ID)
	if err != nil || retainedEndpoint.ResourceVersion != endpoint.ResourceVersion {
		t.Fatal("restart changed registered endpoint", err)
	}
	if _, err := client.NotificationEndpoints.Delete(t.Context(), endpoint.Data.Metadata.ID, endpoint.ResourceVersion); err != nil {
		t.Fatal("public endpoint delete", err)
	}
	for id, version := range versions {
		retained, err := client.Monitors.Get(t.Context(), id)
		if err != nil || retained.ResourceVersion != version {
			t.Fatal("restart changed registered configuration", id, err)
		}
		if _, err := client.Monitors.Delete(t.Context(), id, version); err != nil {
			t.Fatal("public delete", err)
		}
	}
	if targetCalls.Load() != 0 {
		t.Fatal("disabled configurations invoked provider")
	}
}

func TestMainExternalConfigurationFileCollection(t *testing.T) {
	fixture := newMainManagementFixture(t)
	fixture.settings.ExternalJobs.Enabled = true
	writeMainRuntime(t, fixture.options.runtimeFile, fixture.settings)
	client, stop := startMainManagement(t, fixture)
	defer stop()
	registerMainExternalTypes(t, client)
	private := t.TempDir()
	var sources []collection.Source
	var sourceServer *httptest.Server
	var sourceCalls atomic.Int64
	for _, category := range []string{"check", "recovery", "notification"} {
		monitor := mainExternalMonitor(t, "batch-"+category, category, "https://example.test")
		raw, err := json.Marshal(monitor)
		if err != nil {
			t.Fatal(err)
		}
		if category == "notification" {
			sourceServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || len(r.Cookies()) != 0 {
					t.Error("URL source inherited API credentials")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				sourceCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(raw)
			}))
			defer sourceServer.Close()
			sources = append(sources, collection.URL(sourceServer.URL))
			continue
		}
		path := filepath.Join(private, category+".json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, collection.File(path))
	}
	frozen, err := collection.Freeze(t.Context(), sources, collection.Options{TempDir: private, SourceTransport: sourceServer.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Close()
	if sourceCalls.Load() != 1 {
		t.Fatal("external URL input was not fetched exactly once")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	preflight, err := collection.Preflight(ctx, client.Operations, frozen)
	if err != nil || !preflight.Valid {
		t.Fatal("external collection preflight", err)
	}
	applied, err := collection.Apply(ctx, client.Operations, frozen)
	if err != nil {
		t.Fatal("external collection activation", err)
	}
	finished, err := collection.Wait(ctx, client.Operations, applied)
	if err != nil || finished.Operation.Committed == nil || *finished.Operation.Committed != 3 || finished.Operation.Applied == nil || *finished.Operation.Applied != 3 {
		t.Fatal("external collection did not reconcile every item", err, finished.Operation)
	}
	// A bad final contract must fail complete validation before any item activates.
	valid := mainExternalMonitor(t, "uncommitted-valid", "check", "https://example.test")
	invalid := mainExternalMonitor(t, "uncommitted-invalid", "check", "https://example.test")
	invalid.Spec.Check.Driver = mainExternalDriver(t, "check", "missing")
	var badSources []collection.Source
	for _, monitor := range []api.Monitor{valid, invalid} {
		raw, _ := json.Marshal(monitor)
		badSources = append(badSources, collection.Reader(monitor.Metadata.ID, bytes.NewReader(raw)))
	}
	bad, err := collection.Freeze(ctx, badSources, collection.Options{TempDir: private})
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	if _, err := collection.Apply(ctx, client.Operations, bad); err == nil {
		t.Fatal("missing final contract accepted")
	}
	for _, id := range []string{valid.Metadata.ID, invalid.Metadata.ID} {
		if _, err := client.Monitors.Get(ctx, id); !errors.Is(err, cpra.ErrNotFound) {
			t.Fatal("invalid final contract caused active changes", id, err)
		}
	}
}
