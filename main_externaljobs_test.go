//go:build externaljobs

package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/httpserver"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
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
			if !enabled {
				if status := mainAuthStatus(t, fixture, "/api/v2/job-types", startupOperatorToken, false); status != http.StatusNotFound {
					t.Fatal("tagged application enabled JobTypes implicitly", status)
				}
				return
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
