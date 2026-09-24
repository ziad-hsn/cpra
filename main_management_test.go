package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

const startupOperatorToken = "main-operator-fixture-012345678901234567890123456789"
const startupReaderToken = "main-reader-fixture-012345678901234567890123456789"

type mainManagementFixture struct {
	settings runtimeconfig.Config
	options  runOptions
	client   *http.Client
}

func newMainManagementFixture(t *testing.T) mainManagementFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("real startup filesystem fixture qualified on Linux; native platform key/service gates remain separate")
	}
	t.Setenv("CPRA_ENV", "test")
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(base, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	settings := runtimeconfig.Default()
	settings.Storage.Directory = filepath.Join(base, "state")
	key := secureconfig.KeyFileOptions{Path: filepath.Join(private, "wrapping.key"), DataDirectory: settings.Storage.Directory}
	if _, err := secureconfig.GenerateLocalKeyFile(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	// Reuse a test-only certificate and trust roots; the real application server
	// loads the certificate files and owns its independent TLS listener.
	tlsSource := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := tlsSource.TLS.Certificates[0]
	client := tlsSource.Client()
	tlsSource.Close()
	encodedKey, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(private, "server.pem"), filepath.Join(private, "server.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), 0600); err != nil {
		t.Fatal(err)
	}
	clear(encodedKey)
	operator, _ := httpauth.HashToken(startupOperatorToken)
	reader, _ := httpauth.HashToken(startupReaderToken)
	policyPath := filepath.Join(private, "principals.yaml")
	policy, err := yaml.Marshal(map[string]any{"principals": []httpauth.Principal{{ID: "team/oncall", Role: httpauth.Operator, TokenSHA256: operator}, {ID: "team/observer", Role: httpauth.Reader, TokenSHA256: reader}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, policy, 0600); err != nil {
		t.Fatal(err)
	}
	settings.Management = runtimeconfig.Management{Enabled: true, PolicyFile: policyPath, TLS: &runtimeconfig.ManagementTLS{CertFile: certPath, KeyFile: keyPath}, Encryption: &runtimeconfig.ManagementEncryption{Local: &runtimeconfig.ManagementLocalKeys{ActiveKeyFile: key.Path}}}
	options := runOptions{runtimeFile: filepath.Join(private, "runtime.yaml"), manifest: filepath.Join(private, "monitors.yaml"), web: true, webAddr: availableLocalAddress(t), allowEmpty: true, shutdownTimeout: 5 * time.Second}
	if err := os.WriteFile(options.manifest, []byte("monitors: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeMainRuntime(t, options.runtimeFile, settings)
	return mainManagementFixture{settings: settings, options: options, client: client}
}

func availableLocalAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func writeMainRuntime(t *testing.T, path string, settings runtimeconfig.Config) {
	t.Helper()
	raw, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func startMainManagement(t *testing.T, fixture mainManagementFixture) (*cpra.Client, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- runCPRa(ctx, fixture.options, func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatal("application failed before readiness", err)
	case <-time.After(15 * time.Second):
		cancel()
		select {
		case <-done:
		case <-time.After(7 * time.Second):
		}
		t.Fatal("application did not become ready")
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		fixture.client.CloseIdleConnections()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error("application shutdown", err)
			}
		case <-time.After(7 * time.Second):
			t.Error("application did not complete bounded shutdown")
		}
	}
	t.Cleanup(stop)
	client, err := cpra.New(cpra.Config{BaseURL: "https://" + fixture.options.webAddr, AuthToken: startupOperatorToken, HTTPClient: fixture.client})
	if err != nil {
		stop()
		t.Fatal(err)
	}
	return client, stop
}

func waitMainOperation(t *testing.T, client *cpra.Client, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for {
		result, err := client.Operations.Get(ctx, id)
		if err != nil {
			t.Fatal("read admitted operation", err)
		}
		if result.Data.State == "completed" && (result.Data.Applied != nil && *result.Data.Applied == 1) {
			return
		}
		if result.Data.State == "failed" || result.Data.State == "partial" {
			t.Fatal("normal owner failed to apply mutation", result.Data.State)
		}
		select {
		case <-ctx.Done():
			t.Fatal("normal owner did not apply mutation", result.Data.State)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func TestMainManagedTLSWritesRecoverAuthoritativeCatalog(t *testing.T) {
	fixture := newMainManagementFixture(t)
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { providerCalls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	client, stop := startMainManagement(t, fixture)
	access, err := client.Access(context.Background())
	if err != nil || access.Data.PrincipalID != "team/oncall" || access.Data.Role != "operator" {
		t.Fatal("named startup policy not active", err)
	}
	secret := "main-durable-secret-must-never-appear-in-raft-plaintext"
	credential, err := client.Credentials.Create(context.Background(), api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "shared-secret"}, Spec: api.CredentialSpec{Value: &secret}})
	if err != nil {
		t.Fatal("create write-only credential", err)
	}
	if credential.Data.Spec.Value != nil {
		t.Fatal("credential returned plaintext")
	}
	waitMainOperation(t, client, credential.OperationID)
	config, _ := json.Marshal(map[string]string{"url": target.URL})
	monitor := api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "managed-disabled", Name: api.Pointer("Original")}, Spec: api.MonitorSpec{Enabled: api.Pointer(false), Check: api.CheckSpec{Interval: "60s", Timeout: "1s", Driver: api.DriverConfig{Type: "http", Config: config}}}}
	created, err := client.Monitors.Create(context.Background(), monitor)
	if err != nil {
		t.Fatal("create monitor through normal application", err)
	}
	waitMainOperation(t, client, created.OperationID)
	created.Data.Metadata.Name = api.Pointer("Edited in dashboard")
	edited, err := client.Monitors.Replace(context.Background(), "managed-disabled", created.ResourceVersion, created.Data)
	if err != nil {
		t.Fatal(err)
	}
	waitMainOperation(t, client, edited.OperationID)
	stop()
	if providerCalls.Load() != 0 {
		t.Fatal("disabled monitor invoked provider")
	}
	if err := os.Remove(fixture.options.manifest); err != nil {
		t.Fatal(err)
	}
	// Restart has no original source file and no permission to reinterpret it.
	fixture.options.allowEmpty = false
	fixture.options.webAddr = availableLocalAddress(t)
	restarted, stopRestarted := startMainManagement(t, fixture)
	got, err := restarted.Monitors.Get(context.Background(), "managed-disabled")
	if err != nil || got.Data.Metadata.UID != created.Data.Metadata.UID || got.Data.Metadata.Name == nil || *got.Data.Metadata.Name != "Edited in dashboard" || got.Data.Spec.Enabled == nil || *got.Data.Spec.Enabled {
		t.Fatal("restart did not use committed catalog", err)
	}
	recovered, err := restarted.Credentials.Get(context.Background(), "shared-secret")
	if err != nil || recovered.Data.Metadata.UID != credential.Data.Metadata.UID || recovered.Data.Spec.Value != nil {
		t.Fatal("credential identity/read protection lost", err)
	}
	stopRestarted()
	if providerCalls.Load() != 0 {
		t.Fatal("restart invoked disabled monitor")
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
		if bytes.Contains(data, []byte(secret)) {
			t.Error("plaintext credential persisted in state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Turning off management cannot silently reinstate a legacy manifest.
	fixture.settings.Management = runtimeconfig.Management{}
	writeMainRuntime(t, fixture.options.runtimeFile, fixture.settings)
	if err := os.WriteFile(fixture.options.manifest, []byte("monitors: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture.options.allowEmpty = true
	err = runCPRa(context.Background(), fixture.options, func() { t.Error("legacy process became ready against managed state") })
	if err == nil || !strings.Contains(err.Error(), "enable management") {
		t.Fatal("disabled management ignored retained catalog", err)
	}
}

func TestMainManagementValidationAndInvalidBootstrapDoNotAdmitConfiguration(t *testing.T) {
	fixture := newMainManagementFixture(t)
	fixture.options.validate = true
	if err := runCPRa(context.Background(), fixture.options, func() { t.Error("validation announced running") }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fixture.settings.Storage.Directory); !os.IsNotExist(err) {
		t.Fatal("validation opened persistent state", err)
	}
	fixture.options.validate = false
	for _, invalid := range []string{
		"principals: [{id: operator, role: operator, token_sha256: malformed-private-marker}]\n",
		"principals: []\nunknown-private-marker: keep-out-of-errors\n",
		"principals: []\nprincipals: []\n",
		"principals: []\n---\nprincipals: []\n",
	} {
		if err := os.WriteFile(fixture.settings.Management.PolicyFile, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		err := runCPRa(context.Background(), fixture.options, func() { t.Error("invalid policy became ready") })
		if err == nil || strings.Contains(err.Error(), "private-marker") || strings.Contains(err.Error(), "keep-out-of-errors") {
			t.Fatal("invalid policy accepted or exposed", err)
		}
		store, err := persistence.Open(context.Background(), fixture.settings)
		if err != nil {
			t.Fatal(err)
		}
		authority, authErr := store.Authentication()
		hasCatalog, catalogErr := store.HasCatalog()
		closeErr := store.Close()
		if authErr != nil || catalogErr != nil || closeErr != nil || authority.Version != 0 || hasCatalog {
			t.Fatal("invalid bootstrap committed authority or configuration", authErr, catalogErr, closeErr)
		}
	}
}

func TestMainManagementTransportSettingsFailBeforeState(t *testing.T) {
	for _, scenario := range []string{"headless", "cors", "proxy-public", "bad-tls"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newMainManagementFixture(t)
			switch scenario {
			case "headless":
				fixture.options.web = false
			case "cors":
				fixture.options.webCors = "https://other.example"
			case "proxy-public":
				fixture.settings.Management.TLS = nil
				fixture.settings.Management.TrustedProxy = &runtimeconfig.ManagementProxy{PublicOrigin: "https://cpra.example"}
				fixture.options.webAddr = "0.0.0.0:8060"
				writeMainRuntime(t, fixture.options.runtimeFile, fixture.settings)
			case "bad-tls":
				if err := os.WriteFile(fixture.settings.Management.TLS.KeyFile, []byte("private-invalid-key-marker"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := runCPRa(context.Background(), fixture.options, func() { t.Error("invalid transport became ready") })
			if err == nil || strings.Contains(err.Error(), "private-invalid-key-marker") {
				t.Fatal("invalid transport accepted or exposed", err)
			}
			if _, err := os.Stat(fixture.settings.Storage.Directory); !os.IsNotExist(err) {
				t.Fatal("invalid transport opened state", err)
			}
		})
	}
}

func TestMainLegacyTokenReaderIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, 4097), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLegacyWebToken(path); err == nil {
		t.Fatal("unbounded token accepted")
	}
	if err := os.WriteFile(path, []byte("legacy-read-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readLegacyWebToken(path)
	if err != nil || string(got) != "legacy-read-token\n" {
		t.Fatal("legacy compatibility changed", err)
	}
	clear(got)
}

func TestMainStartupSourceLazyAndCompressed(t *testing.T) {
	fixture := newMainManagementFixture(t)
	options := managementStartupOptions(fixture.settings, fixture.options)
	if options.OpenSource == nil {
		t.Fatal("missing lazy source")
	}
	source, err := options.OpenSource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(source)
	if closeErr := source.Close(); err != nil || closeErr != nil || string(data) != "monitors: []\n" {
		t.Fatal("source ownership", err, closeErr)
	}
	clear(data)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte("monitors: []\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.options.manifest += ".gz"
	if err := os.WriteFile(fixture.options.manifest, compressed.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	options = managementStartupOptions(fixture.settings, fixture.options)
	source, err = options.OpenSource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(source)
	if closeErr := source.Close(); err != nil || closeErr != nil || string(data) != "monitors: []\n" {
		t.Fatal("compressed source ownership", err, closeErr)
	}
	clear(data)
}
