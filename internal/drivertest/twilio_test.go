//go:build twilio

package drivertest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

// This is a local runner contract test of Twilio's no-send evidence boundary,
// not passing evidence from Twilio's account-backed test API.
func TestTwilioAcceptanceBoundaryNeverClaimsDelivery(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				user, token, ok := r.BasicAuth()
				if !ok || user != "ACfixture" || token != "SECRET_TEST_TOKEN" || r.Method != http.MethodPost || r.URL.Path != "/2010-04-01/Accounts/ACfixture/Messages.json" {
					t.Error("Twilio production request did not match the selected endpoint/authentication contract")
				}
				if err := r.ParseForm(); err != nil || r.Form.Get("From") != "+15005550006" || r.Form.Get("Body") == "" {
					t.Error("Twilio request form is invalid")
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"sid":"SMfixture","secret":"SECRET_PROVIDER_RESPONSE"}`))
			}))
			defer server.Close()
			cfg := Config{
				Manifest: manifest.Manifest{Monitors: []manifest.Monitor{{ID: "twilio", Name: "twilio", Codes: manifest.Codes{"red": {
					Notify: "twilio", Dispatch: true, Config: &manifest.CodeNotificationTwilio{URL: server.URL + "/2010-04-01/Accounts/ACfixture/Messages.json", AccountSID: "ACfixture", AuthToken: "SECRET_TEST_TOKEN", From: "+15005550006", To: "+15005550006"},
				}}}}},
				Cases: []Case{{Kind: "code", Driver: "twilio", Configured: true, MonitorID: "twilio", Color: "red", EvidenceType: EvidenceProviderSandbox, ObservationBoundary: BoundaryAPIAcceptance}},
			}
			dir := t.TempDir()
			t.Setenv("CPRA_VERIFY_EVIDENCE_DIR", dir)
			r, err := Run(context.Background(), cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			row := r.Records[len(r.Records)-1]
			if requests.Load() != 1 || row.Observed || r.Complete || row.ObservationBoundary != BoundaryAPIAcceptance || row.EvidenceType != EvidenceProviderSandbox {
				t.Fatal(row, requests.Load())
			}
			if status == http.StatusCreated {
				if row.Status != "pass" || !row.Accepted || !r.AllConfiguredPassed {
					t.Fatal(row)
				}
				data, err := os.ReadFile(filepath.Join(dir, row.EvidenceRef))
				if err != nil || strings.Contains(string(data), "SECRET_") || !strings.Contains(string(data), `"observed":false`) || strings.Contains(string(data), `"after"`) {
					t.Fatalf("invalid no-send evidence: %s, %v", data, err)
				}
			} else if row.Status != "fail" || row.Accepted || r.AllConfiguredPassed {
				t.Fatal("provider rejection incorrectly passed", row)
			}
			encoded, err := json.Marshal(r)
			if err != nil || strings.Contains(string(encoded), "SECRET_") {
				t.Fatal("report exposes credentials or provider response", err)
			}
		})
	}
}
