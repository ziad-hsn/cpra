package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// The browser reads real notification outcomes from the production controller,
// encrypted catalog, local HTTP receivers and embedded dashboard. No API replies
// are mocked. Receiver controls change only this disposable network fixture.
func TestMainReviewNotificationsBrowser(t *testing.T) {
	module := os.Getenv("CPRA_BROWSER_MODULE")
	if module == "" {
		if os.Getenv("CPRA_BROWSER_REQUIRED") == "1" {
			t.Fatal("required browser harness is missing CPRA_BROWSER_MODULE")
		}
		t.Skip("set installed browser paths to run review-notification verification")
	}
	node, browser := os.Getenv("CPRA_BROWSER_NODE"), os.Getenv("CPRA_BROWSER_EXECUTABLE")
	for name, path := range map[string]string{"Playwright": module, "Node": node, "Chrome": browser} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s requires an absolute installed path", name)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(name, err)
		}
	}
	fixture := newMainManagementFixture(t)
	var mu sync.Mutex
	healthy, recoveries, checks, tlsChecks := false, 0, 0, 0
	var notices []json.RawMessage
	caFile := filepath.Join(t.TempDir(), "monitor-roots.pem")
	tlsTarget := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		tlsChecks++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer tlsTarget.Close()
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsTarget.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/mode":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			healthy = r.URL.Query().Get("healthy") == "true"
			w.WriteHeader(http.StatusNoContent)
		case "/count":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": checks, "recoveries": recoveries, "notices": notices, "tlsChecks": tlsChecks})
		case "/remove-ca":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if err := os.Remove(caFile); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/recover":
			recoveries++
			w.WriteHeader(http.StatusInternalServerError) // Effect occurred, then acknowledgement failed.
		case "/notice", "/failed-closure":
			var body json.RawMessage
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || len(notices) >= 64 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			notices = append(notices, body)
			var notice struct {
				Review struct {
					Phase string `json:"phase"`
				} `json:"review"`
			}
			_ = json.Unmarshal(body, &notice)
			if r.URL.Path == "/failed-closure" && notice.Review.Phase == "close" {
				w.WriteHeader(http.StatusInternalServerError)
			}
		default:
			checks++
			if r.URL.Path == "/maintenance" || r.URL.Path != "/healthy" && !healthy {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}
	}))
	defer target.Close()
	client, stop := startMainManagement(t, fixture)
	defer stop()
	for id, path := range map[string]string{"review-recovery": "/recover", "review-success": "/notice", "review-failed-close": "/failed-closure"} {
		created, err := client.Credentials.Create(t.Context(), api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: id}, Spec: api.CredentialSpec{Value: api.Pointer(target.URL + path)}})
		if err != nil {
			t.Fatal(err)
		}
		waitMainOperation(t, client, created.OperationID)
	}
	createdTarget, err := client.Credentials.Create(t.Context(), api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "preparation-target"}, Spec: api.CredentialSpec{Value: api.Pointer(tlsTarget.URL + "/private-ca-target-marker")}})
	if err != nil {
		t.Fatal(err)
	}
	waitMainOperation(t, client, createdTarget.OperationID)
	for _, id := range []string{"review-success", "review-failed-close"} {
		created, err := client.NotificationEndpoints.Create(t.Context(), api.NotificationEndpoint{APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: id}, Spec: api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"url": id})}})
		if err != nil {
			t.Fatal(err)
		}
		waitMainOperation(t, client, created.OperationID)
	}
	for n, name := range []string{"Checkout API", "Inventory service", "Search service", "Billing gateway", "Queue processor", "Account service", "Review success", "Review failed close"} {
		id, endpoint := "demo-"+[]string{"checkout", "inventory", "search", "billing", "queue", "accounts", "review-success", "review-failed-close"}[n], "review-success"
		if n == 5 {
			id = "demo-account-service-with-a-deliberately-long-stable-identifier-for-layout-verification"
			name = "Account service with a deliberately long deployment and namespace name for bounded table navigation"
		}
		path := "/healthy"
		if n == 1 {
			path = "/maintenance"
		}
		if n >= 6 {
			path = "/health"
			if n == 7 {
				endpoint = "review-failed-close"
			}
		}
		check, _ := json.Marshal(map[string]string{"url": target.URL + path})
		spec := api.MonitorSpec{Enabled: api.Pointer(true), Check: api.CheckSpec{Interval: "250ms", Timeout: "1s", UnhealthyThreshold: api.Pointer(int64(1)), HealthyThreshold: api.Pointer(int64(2)), Driver: api.DriverConfig{Type: "http", Config: check}}}
		if n == 1 {
			start, end := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
			spec.Maintenance = api.Pointer([]api.MaintenanceWindow{{Start: &start, End: &end}})
		}
		if n >= 6 {
			spec.Notifications = api.Pointer(map[string]api.AlertRule{"blue": {EndpointRefs: api.Pointer([]string{endpoint})}})
			spec.Recovery = &api.RecoverySpec{Driver: api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{"method":"POST","timeout":"2s"}`), CredentialRefs: api.Pointer(map[string]string{"url": "review-recovery"})}, MaxAttempts: api.Pointer(int64(3))}
		}
		created, err := client.Monitors.Create(t.Context(), api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: id, Name: &name}, Spec: spec})
		if err != nil {
			t.Fatal(err)
		}
		waitMainOperation(t, client, created.OperationID)
	}
	preparationConfig, _ := json.Marshal(map[string]any{"caFile": caFile, "expectedStatus": []int{204}})
	preparation, err := client.Monitors.Create(t.Context(), api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "demo-preparation", Name: api.Pointer("Certificate preparation check")}, Spec: api.MonitorSpec{
		Enabled:  api.Pointer(true),
		Check:    api.CheckSpec{Interval: "250ms", Timeout: "1s", UnhealthyThreshold: api.Pointer(int64(1)), HealthyThreshold: api.Pointer(int64(2)), Driver: api.DriverConfig{Type: "http", Config: preparationConfig, CredentialRefs: api.Pointer(map[string]string{"url": "preparation-target"})}},
		Recovery: &api.RecoverySpec{Driver: api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{"method":"POST","timeout":"2s"}`), CredentialRefs: api.Pointer(map[string]string{"url": "review-recovery"})}, MaxAttempts: api.Pointer(int64(1))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitMainOperation(t, client, preparation.OperationID)
	certificatePEM, err := os.ReadFile(fixture.settings.Management.TLS.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certificatePEM)
	if block == nil {
		t.Fatal("missing fixture certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join(root, "scripts/dashboard/verify_review_notifications_browser.cjs"))
	cmd.Env = append(os.Environ(), "CPRA_BROWSER_ORIGIN=https://"+fixture.options.webAddr, "CPRA_BROWSER_TARGET="+target.URL, "CPRA_BROWSER_CERT_PIN="+base64.StdEncoding.EncodeToString(pin[:]))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("review notifications browser failed: %v\n%s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if recoveries != 2 || checks < 8 || len(notices) != 6 || tlsChecks == 0 {
		t.Fatal("independent receiver counts", recoveries, checks, len(notices), tlsChecks)
	}
	t.Logf("real recovery effects=%d; notifications=%d; checks=%d\n%s", recoveries, len(notices), checks, output)
}
