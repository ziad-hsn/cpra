package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/installpath"
	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestMainGeneratedManagementFirstRunAndRestart(t *testing.T) {
	f := newMainManagementFixture(t)
	root := t.TempDir()
	layout := installpath.Layout{Scope: "user", ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), BinDir: filepath.Join(root, "bin"), ServiceFile: filepath.Join(root, "cpra.service")}
	files, err := localadmin.InitManagement(t.Context(), layout, localadmin.ManagementInitOptions{OperatorID: "team/oncall"})
	if err != nil {
		t.Fatal(err)
	}
	f.settings, err = runtimeconfig.Load(files.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	f.options.runtimeFile, f.options.manifest = files.Runtime, files.Manifest
	f.options.webAuth, f.options.webAuthFile = "", ""
	ca, err := os.ReadFile(files.CA)
	if err != nil {
		t.Fatal(err)
	}
	trust := x509.NewCertPool()
	if !trust.AppendCertsFromPEM(ca) {
		t.Fatal("generated local trust unavailable")
	}
	f.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trust, MinVersion: tls.VersionTLS12}}, Timeout: 5 * time.Second}
	token, err := os.ReadFile(files.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(token)
	newClient := func() *cpra.Client {
		client, err := cpra.New(cpra.Config{BaseURL: "https://" + f.options.webAddr, AuthToken: strings.TrimSpace(string(token)), HTTPClient: f.client})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	_, stop := startMainManagement(t, f)
	client := newClient()
	mode, err := client.DiscoverAccess(t.Context())
	if err != nil || mode.Data.Mode != api.AccessDiscoveryModeManagement {
		t.Fatal("generated management mode unavailable", err)
	}
	access, err := client.Access(t.Context())
	if err != nil || access.Data.PrincipalID != "team/oncall" {
		t.Fatal("generated operator cannot authenticate", err)
	}
	monitor := api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "generated-first-run"}, Spec: api.MonitorSpec{Enabled: api.Pointer(false), Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.invalid"}`)}}}}
	created, err := client.Monitors.Create(t.Context(), monitor)
	if err != nil {
		t.Fatal("generated operator cannot create monitor", err)
	}
	waitMainOperation(t, client, created.OperationID)
	uid, revision := created.Data.Metadata.UID, created.Data.Metadata.ResourceVersion
	client.CloseIdleConnections()
	stop()
	if err := os.Remove(f.settings.Management.PolicyFile); err != nil {
		t.Fatal(err)
	}
	_, stop = startMainManagement(t, f)
	defer stop()
	client = newClient()
	defer client.CloseIdleConnections()
	loaded, err := client.Monitors.Get(t.Context(), monitor.Metadata.ID)
	if err != nil || loaded.Data.Metadata.UID != uid || loaded.Data.Metadata.ResourceVersion != revision || loaded.Data.Spec.Enabled == nil || *loaded.Data.Spec.Enabled {
		t.Fatal("generated installation lost committed identity on restart", err)
	}
}
