package drivertest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestLoadConfigurationBoundaries(t *testing.T) {
	prefix := "manifest: {}\ncases: []\n"
	padding := strings.Repeat(" ", (4<<20)-len(prefix))
	for _, tc := range []struct {
		name  string
		input string
		valid bool
	}{
		{"at size limit", prefix + padding, true},
		{"oversized valid input", prefix + padding + "\n", false},
		{"trailing document", prefix + "---\nstate_directory: PRIVATE_CONFIG_VALUE\n", false},
		{"trailing document past size limit", prefix + padding + "\n---\nstate_directory: PRIVATE_CONFIG_VALUE\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "verification.yaml")
			if err := os.WriteFile(path, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tc.valid {
				if err != nil || len(cfg.Cases) != 0 || len(cfg.Manifest.Monitors) != 0 {
					t.Fatalf("valid configuration rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted oversized or multiple-document configuration")
			}
			if strings.Contains(err.Error(), "PRIVATE_CONFIG_VALUE") {
				t.Fatal("configuration error disclosed an input value")
			}
		})
	}
}

func TestUnconfiguredInventoryNeverPasses(t *testing.T) {
	r, err := Run(context.Background(), Config{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete || r.AllConfiguredPassed || r.AllDriversTested || r.AllDriversPassed || r.ConfiguredCases != 0 || r.NotConfiguredCases != 35 || len(r.Records) != 35 {
		t.Fatal(r)
	}
	counts := map[string]int{}
	for _, row := range r.Records {
		counts[row.Kind]++
		if row.Status != "not_configured" || row.Accepted || row.Observed {
			t.Fatal(row)
		}
	}
	if counts["pulse"] != 14 || counts["intervention"] != 6 || counts["code"] != 15 {
		t.Fatal(counts)
	}
}

func TestEvidenceSummariesCannotCertifyMocksOrSandboxes(t *testing.T) {
	for _, evidenceType := range []string{EvidenceLiveAccount, EvidenceLocalIntegration, EvidenceMockContract, EvidenceProviderSandbox} {
		t.Run(evidenceType, func(t *testing.T) {
			r := Report{}
			for _, driver := range Inventory() {
				r.Records = append(r.Records, Record{Driver: driver, Configured: true, Invoked: true, Status: "pass", Accepted: true, Observed: true, EvidenceType: EvidenceLiveAccount, ObservationBoundary: BoundaryEffect})
			}
			// Even one non-live case must keep the full-provider gate closed.
			r.Records[0].EvidenceType = evidenceType
			r.summarize()
			if r.Complete != (evidenceType == EvidenceLiveAccount) || !r.AllConfiguredPassed || !r.AllDriversTested || !r.AllDriversPassed || r.PassedCases != 35 {
				t.Fatal(r)
			}
			r.Records[0].Status = "fail"
			r.summarize()
			if r.Complete || r.AllConfiguredPassed || r.AllDriversPassed || !r.AllDriversTested || r.FailedCases != 1 {
				t.Fatal("a failed but executed case is tested, not passed", r)
			}
		})
	}
}

func TestInvalidEvidenceIsRejectedBeforeAnyDispatch(t *testing.T) {
	cfg, count := localHTTPScenario(t)
	for _, bad := range []Case{
		{Kind: "code", Driver: "twilio", EvidenceType: "SECRET_INVALID_TYPE"},
		{Kind: "code", Driver: "twilio", ObservationBoundary: "SECRET_INVALID_BOUNDARY"},
		{Kind: "code", Driver: "twilio", EvidenceType: EvidenceLiveAccount, ObservationBoundary: BoundaryAPIAcceptance},
		{Kind: "code", Driver: "twilio", EvidenceType: EvidenceMockContract, ObservationBoundary: BoundaryAPIAcceptance},
		{Kind: "code", Driver: "telegram", EvidenceType: EvidenceProviderSandbox, ObservationBoundary: BoundaryAPIAcceptance},
	} {
		candidate := cfg
		candidate.Cases = append(append([]Case{}, cfg.Cases...), bad)
		_, err := Run(context.Background(), candidate, true)
		if err == nil || strings.Contains(err.Error(), "SECRET_") {
			t.Fatalf("invalid evidence not rejected safely: %v", err)
		}
	}
	if count.Load() != 0 {
		t.Fatal("invalid configuration invoked an earlier valid operation")
	}
}

func TestLocalAndMockEvidenceUsesProductionDriverAndIndependentObserver(t *testing.T) {
	for _, evidenceType := range []string{EvidenceLocalIntegration, EvidenceMockContract, EvidenceLiveAccount} {
		t.Run(evidenceType, func(t *testing.T) {
			cfg, count := localHTTPScenario(t)
			cfg.Cases[0].EvidenceType = evidenceType
			dir := t.TempDir()
			t.Setenv("CPRA_VERIFY_EVIDENCE_DIR", dir)
			r, err := Run(context.Background(), cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			row := r.Records[0]
			if count.Load() != 1 || row.Status != "pass" || row.Outcome != "accepted" || !row.Accepted || !row.Observed || !row.Invoked || row.EvidenceType != evidenceType || row.ObservationBoundary != BoundaryEffect {
				t.Fatal(row, count.Load())
			}
			if r.Complete || !r.AllConfiguredPassed || r.AllDriversTested || r.PassedCases != 1 || r.NotConfiguredCases != len(Inventory())-1 || r.PassedByEvidenceType[evidenceType] != 1 {
				t.Fatal(r)
			}
			data, err := os.ReadFile(filepath.Join(dir, row.EvidenceRef))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "SECRET_OBSERVER_TEXT") || !strings.Contains(string(data), evidenceType) || !strings.Contains(string(data), `"observed":true`) {
				t.Fatalf("invalid or unsafe evidence: %s", data)
			}
		})
	}
}

func TestObserverCannotBeOmittedFromMockOrLocalEvidence(t *testing.T) {
	for _, evidenceType := range []string{EvidenceMockContract, EvidenceLocalIntegration, EvidenceProviderSandbox, EvidenceLiveAccount} {
		cfg, count := localHTTPScenario(t)
		cfg.Cases[0].EvidenceType = evidenceType
		cfg.Cases[0].Observer = nil
		r, err := Run(context.Background(), cfg, true)
		if err != nil {
			t.Fatal(err)
		}
		if r.Records[0].Status != "not_configured" || r.AllConfiguredPassed || count.Load() != 0 {
			t.Fatal("missing observer was treated as verification", r.Records[0])
		}
	}
}

func TestAcceptanceWithoutObservedEffectDoesNotPass(t *testing.T) {
	cfg, count := localHTTPScenario(t)
	cfg.Cases[0].Observer = append(cfg.Cases[0].Observer, "deny")
	r, err := Run(context.Background(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	row := r.Records[0]
	if count.Load() != 1 || row.Status != "fail" || row.Outcome != "accepted" || !row.Accepted || row.Observed || r.AllConfiguredPassed {
		t.Fatal(row)
	}
}

func TestReportDistinguishesObservedHTTPStatusFromEffectCertainty(t *testing.T) {
	for _, status := range []int{400, 404, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			cfg, count := localHTTPScenarioStatus(t, status)
			monitor := &cfg.Manifest.Monitors[0]
			monitor.Codes = manifest.Codes{"red": {Notify: "webhook", Dispatch: true, Config: &manifest.CodeNotificationWebhook{URL: monitor.Pulse.Config.(*manifest.PulseHTTPConfig).Url}}}
			cfg.Cases[0].Kind, cfg.Cases[0].Driver, cfg.Cases[0].Color = "code", "webhook", "red"
			r, err := Run(context.Background(), cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range r.Records {
				if row.Kind == "code" && row.Name == "webhook" {
					if row.Status != "fail" || row.HTTPStatus != status || row.Outcome != "unknown" || !row.Invoked || row.Accepted || row.Observed || count.Load() != 1 {
						t.Fatal("HTTP status was lost or asserted effect rejection", row)
					}
				}
			}
		})
	}
	// Keep the observer available while the selected operation endpoint drops
	// connections before any HTTP response; no HTTP status can be inferred.
	cfg, count := localHTTPScenario(t)
	disconnected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(disconnected.Close)
	cfg.Manifest.Monitors[0].Codes = manifest.Codes{"red": {Notify: "webhook", Dispatch: true, Config: &manifest.CodeNotificationWebhook{URL: disconnected.URL}}}
	cfg.Cases[0].Kind, cfg.Cases[0].Driver, cfg.Cases[0].Color = "code", "webhook", "red"
	r, err := Run(context.Background(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range r.Records {
		if row.Kind == "code" && row.Name == "webhook" && (row.Status != "fail" || row.HTTPStatus != 0 || row.Outcome != "unknown" || !row.Invoked || count.Load() != 0) {
			t.Fatal("transport failure presented as HTTP rejection", row)
		}
	}
}

func TestReportRecordsProvenRejectionWithoutRetrying(t *testing.T) {
	cfg, _ := localHTTPScenario(t)
	var invoked atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		invoked.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "action_prohibited")
	}))
	t.Cleanup(target.Close)
	cfg.Manifest.Monitors[0].Codes = manifest.Codes{"red": {Notify: "slack", Dispatch: true, Config: &manifest.CodeNotificationSlack{WebHook: target.URL}}}
	cfg.Cases[0].Kind, cfg.Cases[0].Driver, cfg.Cases[0].Color = "code", "slack", "red"
	r, err := Run(context.Background(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range r.Records {
		if row.Kind == "code" && row.Name == "slack" {
			if row.Status != "fail" || row.HTTPStatus != http.StatusForbidden || row.RejectionCode != http.StatusForbidden || row.Outcome != "rejected" || !row.Invoked || row.Accepted || row.Observed || invoked.Load() != 1 {
				t.Fatal("documented rejection was lost or repeated", row, invoked.Load())
			}
			return
		}
	}
	t.Fatal("configured Slack case missing from the report")
}

func TestReportRetainsReceivedStatusForBodyRejectionAndReadFailure(t *testing.T) {
	for _, mode := range []string{"body-rejection", "body-timeout"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _ := localHTTPScenario(t)
			var invoked atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				invoked.Add(1)
				w.WriteHeader(http.StatusOK)
				if mode == "body-rejection" {
					_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"PRIVATE_RESPONSE_TEXT"}`)
					return
				}
				_, _ = io.WriteString(w, `{"private":"PRIVATE_RESPONSE_TEXT`)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			t.Cleanup(target.Close)
			cfg.Manifest.Monitors[0].Codes = manifest.Codes{"red": {Notify: "telegram", Dispatch: true, Config: &manifest.CodeNotificationTelegram{URL: target.URL, BotToken: "fixture-token", ChatID: "fixture-chat"}}}
			cfg.Cases[0].Kind, cfg.Cases[0].Driver, cfg.Cases[0].Color = "code", "telegram", "red"
			cfg.Cases[0].Timeout = 2 * time.Second
			r, err := Run(context.Background(), cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			want := "rejected"
			code := 400
			if mode == "body-timeout" {
				want = "unknown"
				code = 0
			}
			for _, row := range r.Records {
				if row.Kind == "code" && row.Name == "telegram" {
					if row.Status != "fail" || row.HTTPStatus != http.StatusOK || row.RejectionCode != code || row.Outcome != want || !row.Invoked || row.Accepted || row.Observed || invoked.Load() != 1 {
						t.Fatal("received status or effect certainty was lost", row, invoked.Load())
					}
					data, _ := json.Marshal(row)
					if strings.Contains(string(data), "PRIVATE_RESPONSE_TEXT") || strings.Contains(string(data), "fixture-token") {
						t.Fatal("provider response or credential escaped into report")
					}
					return
				}
			}
			t.Fatal("configured Telegram case missing from the report")
		})
	}
}

func TestReferencedEndpointPlaceholdersAreNotInvoked(t *testing.T) {
	m := manifest.Monitor{ID: "group", Name: "group", Codes: manifest.Codes{"red": {NotifyGroup: "test"}}}
	cfg := Config{
		Manifest: manifest.Manifest{
			Monitors:           []manifest.Monitor{m},
			NotificationGroups: manifest.NotificationGroups{"test": {"notification"}},
			Endpoints:          map[string]manifest.Endpoint{"notification": {Type: "webhook", Config: &manifest.CodeNotificationWebhook{URL: "http://REPLACE_WITH_DESTINATION"}}},
		},
		Cases: []Case{{Kind: "code", Driver: "webhook", MonitorID: "group", Color: "red", Configured: true, Observer: []string{"never-invoke"}, EvidenceType: EvidenceMockContract}},
	}
	r, err := Run(context.Background(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range r.Records {
		if row.Name == "webhook" && row.Kind == "code" && (row.Status != "not_configured" || row.Invoked) {
			t.Fatal(row)
		}
	}
}

func TestTwilioAcceptanceRequiresNoSendTestConfiguration(t *testing.T) {
	valid := manifest.CodeNotificationTwilio{AccountSID: "ACtest", AuthToken: "test", From: "+15005550006", To: "+15005550006"}
	for _, mutate := range []func(*manifest.CodeNotificationTwilio){
		func(c *manifest.CodeNotificationTwilio) { c.AccountSID = "" },
		func(c *manifest.CodeNotificationTwilio) { c.AuthToken = "" },
		func(c *manifest.CodeNotificationTwilio) { c.To = "" },
		func(c *manifest.CodeNotificationTwilio) { c.From = "+12025550123" },
		func(c *manifest.CodeNotificationTwilio) { c.AuthToken = "REPLACE_WITH_TEST_TOKEN" },
	} {
		cfg := valid
		mutate(&cfg)
		m := manifest.Monitor{Codes: manifest.Codes{"red": {Notify: "twilio", Config: &cfg}}}
		c := Case{Kind: "code", Driver: "twilio", Color: "red", EvidenceType: EvidenceProviderSandbox, ObservationBoundary: BoundaryAPIAcceptance}
		if caseConfigured(c, m, manifest.Manifest{}) {
			t.Fatal("incomplete or ordinary Twilio configuration qualifies for no-send acceptance")
		}
	}
}

func TestLoadSandboxExampleIsDisabledAndClassified(t *testing.T) {
	cfg, err := Load("../../examples/verification/sandboxes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Cases) != 4 {
		t.Fatalf("sandbox example has %d cases", len(cfg.Cases))
	}
	for _, c := range cfg.Cases {
		if c.Configured || c.EvidenceType != EvidenceProviderSandbox {
			t.Fatal("example can invoke operations without account configuration", c)
		}
		if c.Driver == "twilio" && c.ObservationBoundary != BoundaryAPIAcceptance {
			t.Fatal("Twilio no-send test incorrectly requires delivery evidence")
		}
	}
	r, err := Run(context.Background(), cfg, false)
	if err != nil || r.AllConfiguredPassed || r.Complete || r.ExecutedCases != 0 {
		t.Fatal(r, err)
	}
}

func localHTTPScenario(t *testing.T) (Config, *atomic.Int64) {
	t.Helper()
	return localHTTPScenarioStatus(t, http.StatusOK)
}

func localHTTPScenarioStatus(t *testing.T, status int) (Config, *atomic.Int64) {
	t.Helper()
	count := new(atomic.Int64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/check" {
			count.Add(1)
			w.WriteHeader(status)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"count": count.Load()})
	}))
	t.Cleanup(server.Close)
	observer, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Manifest: manifest.Manifest{Monitors: []manifest.Monitor{{ID: "fixture", Name: "fixture", Pulse: manifest.Pulse{Type: "http", Timeout: time.Second, Config: &manifest.PulseHTTPConfig{Url: server.URL + "/check"}}}}},
		Cases:    []Case{{Kind: "pulse", Driver: "http", MonitorID: "fixture", Configured: true, Timeout: 10 * time.Second, Observer: []string{observer, "-test.run=^TestVerificationObserverProcess$", "--", server.URL + "/count"}}},
	}, count
}

// The helper process reads target-side accounting independently of the driver.
func TestVerificationObserverProcess(t *testing.T) {
	phase := os.Getenv("CPRA_VERIFY_PHASE")
	if phase == "" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(1)
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(args[1])
	if err != nil {
		os.Exit(1)
	}
	var current, before struct {
		Count int64 `json:"count"`
	}
	err = json.NewDecoder(response.Body).Decode(&current)
	response.Body.Close()
	if err != nil {
		os.Exit(1)
	}
	observed := false
	if phase == "after" {
		if err := json.NewDecoder(os.Stdin).Decode(&before); err != nil {
			os.Exit(1)
		}
		observed = current.Count > before.Count && len(args) == 2
	}
	fmt.Printf(`{"count":%d,"observed":%t,"token":"SECRET_OBSERVER_TEXT"}`, current.Count, observed)
	os.Exit(0)
}
func TestConfiguredOperationsRequireExplicitLiveInvocation(t *testing.T) {
	_, err := Run(context.Background(), Config{Cases: []Case{{Kind: "intervention", Driver: "aws", Configured: true}}}, false)
	if err == nil {
		t.Fatal("live operation implicitly enabled")
	}
}

func TestRetainedEvidenceDoesNotContainArbitraryProviderText(t *testing.T) {
	data := redactedObservation([]byte(`{"observed":true,"count":2,"token":"secret","url":"https://user:password@example.com","before":"credentials","resource_digest":"invalid"}`))
	if len(data) != 3 || data["observed"] != true || data["count"] != float64(2) {
		t.Fatal(data)
	}
}

func TestRetainedPacketCountersAreNumericOnly(t *testing.T) {
	data := redactedObservation([]byte(`{"request_count":3,"reply_count":2,"payload":"secret"}`))
	if len(data) != 3 || data["request_count"] != float64(3) || data["reply_count"] != float64(2) {
		t.Fatal(data)
	}
	data = redactedObservation([]byte(`{"request_count":"secret","reply_count":true}`))
	if len(data) != 1 {
		t.Fatal("non-numeric packet counters were retained", data)
	}
}
