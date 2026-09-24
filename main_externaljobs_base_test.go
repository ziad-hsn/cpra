//go:build !externaljobs

package main

import (
	"context"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/httpserver"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestMainExternalJobsExcludedFromBaseBuild(t *testing.T) {
	if _, exists := reflect.TypeFor[runtimeconfig.Config]().FieldByName("ExternalJobs"); exists {
		t.Fatal("base runtime exposes custom-job configuration")
	}
	if _, exists := reflect.TypeFor[httpserver.ServerConfig]().FieldByName("ExternalJobsEnabled"); exists {
		t.Fatal("base server exposes custom-job configuration")
	}
	fixture := newMainManagementFixture(t)
	_, stop := startMainManagement(t, fixture)
	defer stop()
	if status := mainAuthStatus(t, fixture, "/api/v2/job-types", startupOperatorToken, false); status != http.StatusNotFound {
		t.Fatal("normal base application registered JobTypes", status)
	}
}

func TestMainExternalJobsBaseRejectsConfigurationBeforeStorage(t *testing.T) {
	fixture := newMainManagementFixture(t)
	raw, err := os.ReadFile(fixture.options.runtimeFile)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\nexternal_jobs: {enabled: true}\n")...)
	if err := os.WriteFile(fixture.options.runtimeFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	clear(raw)
	if err := runCPRa(context.Background(), fixture.options, func() { t.Error("unsupported opt-in became ready") }); err == nil || !strings.Contains(err.Error(), "external_jobs") {
		t.Fatal("base startup accepted custom-job configuration", err)
	}
	if _, err := os.Stat(fixture.settings.Storage.Directory); !os.IsNotExist(err) {
		t.Fatal("rejected configuration created/opened state", err)
	}
}
