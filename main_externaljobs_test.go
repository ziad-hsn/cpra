//go:build externaljobs

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/httpserver"
	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestMainExternalJobsConfigurationFollowsExplicitOptIn(t *testing.T) {
	settings := runtimeconfig.Default()
	config := httpserver.ServerConfig{}
	config.ExternalJobsEnabled = true
	configureExternalJobsServer(&config, settings)
	if config.ExternalJobsEnabled {
		t.Fatal("tagged build retained implicit external-job enablement")
	}
	settings.ExternalJobs.Enabled = true
	configureExternalJobsServer(&config, settings)
	if !config.ExternalJobsEnabled {
		t.Fatal("server configuration lost explicit opt-in")
	}
}

func TestMainExternalWorkerProvisioningAndPoll(t *testing.T) {
	fixture := newMainManagementFixture(t)
	fixture.settings.ExternalJobs.Enabled = true
	writeMainRuntime(t, fixture.options.runtimeFile, fixture.settings)
	client, stop := startMainManagement(t, fixture)
	job, err := client.JobTypes().Create(t.Context(), api.JobType{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: "main-worker-check"}, Spec: api.JobTypeSpec{
		Kind: "check", Version: "v1", Handler: "main-check", ProtocolVersion: "1", Timeout: "5s",
		ParameterSchema: json.RawMessage(`{"type":"object"}`), ResultSchema: json.RawMessage(`{"type":"boolean"}`),
	}})
	if err != nil {
		t.Fatal("type registration through normal startup", err)
	}
	stop()
	private := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	grants, err := json.Marshal(struct {
		Grants []persistence.WorkerGrant `json:"grants"`
	}{Grants: []persistence.WorkerGrant{{JobTypeID: job.Data.Metadata.ID, JobTypeUID: job.Data.Metadata.UID,
		Version: "v1", Category: "check", ResourceKind: "Monitor", ResourceIDs: []string{"worker-monitor"}}}})
	if err != nil {
		t.Fatal(err)
	}
	grantsFile, tokenFile := filepath.Join(private, "grants.json"), filepath.Join(private, "worker.token")
	if err := os.WriteFile(grantsFile, grants, 0600); err != nil {
		t.Fatal(err)
	}
	provisioned, err := localadmin.AdministerWorkerAuthentication(t.Context(), localadmin.WorkerAuthenticationRequest{
		Action: "issue", DataDirectory: fixture.settings.Storage.Directory, Actor: "team/oncall", WorkerID: "main-worker",
		GrantsFile: grantsFile, TokenOutput: tokenFile,
	})
	if err != nil {
		t.Fatal("stopped worker provisioning", err)
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(token)
	client, stop = startMainManagement(t, fixture)
	defer stop()
	worker, err := cpra.NewWorkerClient(cpra.Config{BaseURL: "https://" + fixture.options.webAddr, AuthToken: strings.TrimSpace(string(token)), HTTPClient: fixture.client})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.CloseIdleConnections()
	request := api.PollRequest{ServerID: provisioned.ProtocolServerID, WorkerID: "main-worker", ClientSessionID: "main-worker-session",
		PollSequence: 1, Capacity: 1, Limit: 1, Capabilities: []api.WorkerCapability{{JobTypeID: job.Data.Metadata.ID, Version: "v1", Kind: "check"}}}
	offers, err := worker.Poll(t.Context(), request)
	if err != nil || offers == nil || offers.SessionID == "" || offers.WorkerUID != provisioned.Workers[0].UID || offers.ServerID != provisioned.ProtocolServerID || len(offers.Items) != 0 {
		t.Fatal("normal startup did not authenticate the provisioned worker", err)
	}
	replayed, err := worker.Poll(t.Context(), request)
	if err != nil || replayed.SessionID != offers.SessionID || !replayed.SessionExpiresAt.Equal(offers.SessionExpiresAt) {
		t.Fatal("normal route changed the original polling session", err)
	}
	observed, err := client.Workers(t.Context(), cpra.ListOptions{})
	if err != nil || len(observed.Data.Items) != 1 {
		t.Fatal("normal startup lost worker observations", err)
	}
	row := observed.Data.Items[0]
	if row.WorkerUID != offers.WorkerUID || row.WorkerID != request.WorkerID || row.SessionStatus != "current" || !row.CapacityAvailable || row.Capacity != 1 || row.LastSeenAt == nil {
		t.Fatal("normal startup changed committed worker observations")
	}
	capabilities, err := client.Capabilities(t.Context())
	if err != nil || !slices.Contains(capabilities.Data.Resources, "ExternalWorker") || !slices.Equal(capabilities.Data.ResourceOperations["ExternalWorker"], []string{"ListWorkers"}) {
		t.Fatal("worker observation discovery missing", err)
	}
}

func TestMainExternalJobsRuntimeOptInControlsJobTypeRoute(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newMainManagementFixture(t)
			fixture.settings.ExternalJobs.Enabled = enabled
			writeMainRuntime(t, fixture.options.runtimeFile, fixture.settings)
			client, stop := startMainManagement(t, fixture)
			defer stop()
			for _, operation := range []string{"poll", "start", "result", "heartbeat", "late-evidence"} {
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+fixture.options.webAddr+"/api/v2/external-workers/"+operation, strings.NewReader(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer "+startupOperatorToken)
				response, err := fixture.client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8192))
				response.Body.Close()
				want := http.StatusNotFound
				if enabled {
					want = http.StatusUnauthorized
				}
				if response.StatusCode != want {
					t.Fatal("normal startup worker route/runtime authority boundary", operation, enabled, response.StatusCode)
				}
			}
			if !enabled {
				if status := mainAuthStatus(t, fixture, "/api/v2/job-types", startupOperatorToken, false); status != http.StatusNotFound {
					t.Fatal("tagged application enabled JobTypes implicitly", status)
				}
				if status := mainAuthStatus(t, fixture, "/api/v2/external-workers", startupOperatorToken, false); status != http.StatusNotFound {
					t.Fatal("tagged application enabled worker observations implicitly", status)
				}
				return
			}
			workers, err := client.Workers(t.Context(), cpra.ListOptions{})
			if err != nil || len(workers.Data.Items) != 0 || workers.Data.NextCursor != "" || workers.Data.GeneratedAt.IsZero() {
				t.Fatal("normal startup did not expose empty worker observations", err)
			}
			listed, err := client.JobTypes().List(context.Background(), cpra.ListOptions{Limit: 100})
			if err != nil || listed == nil || len(listed.Data.Items) != 0 || listed.Data.NextCursor != "" {
				t.Fatal("normal TLS/Raft application did not expose opted-in empty JobType list", err)
			}
		})
	}
}

func TestMainExternalJobsRejectMemoryBeforeStorage(t *testing.T) {
	fixture := newMainManagementFixture(t)
	fixture.settings.Storage.Mode = "memory"
	fixture.settings.ExternalJobs.Enabled = true
	writeMainRuntime(t, fixture.options.runtimeFile, fixture.settings)
	if err := runCPRa(context.Background(), fixture.options, func() { t.Error("volatile external jobs became ready") }); err == nil || !strings.Contains(err.Error(), "external_jobs.enabled") {
		t.Fatal("external jobs accepted memory storage", err)
	}
	if _, err := os.Stat(fixture.settings.Storage.Directory); !os.IsNotExist(err) {
		t.Fatal("rejected external jobs opened state", err)
	}
}
