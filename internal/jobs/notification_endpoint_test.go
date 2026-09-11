package jobs

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cpra/internal/loader/schema"
	"github.com/mlange-42/ark/ecs"
)

type endpointCase struct {
	name   string
	config func(string) schema.CodeNotification
	verify func(*testing.T, *http.Request)
}

func notificationEndpointCases() []endpointCase {
	return []endpointCase{
		{"telegram", func(target string) schema.CodeNotification {
			return &schema.CodeNotificationTelegram{URL: target, BotToken: "123:fixture", ChatID: "456"}
		}, func(t *testing.T, r *http.Request) {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["chat_id"] != "456" || !strings.Contains(body["text"], "fixture") {
				t.Errorf("unexpected telegram body: %v", body)
			}
		}},
		{"victorops", func(target string) schema.CodeNotification {
			return &schema.CodeNotificationVictorOps{URL: target, RestEndpointKey: "fixture", RoutingKey: "routing", MessageType: "INFO", EntityID: "fixture"}
		}, func(t *testing.T, r *http.Request) {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["message_type"] != "INFO" || body["entity_id"] != "fixture" || !strings.Contains(body["state_message"], "fixture") {
				t.Errorf("unexpected victorops body: %v", body)
			}
		}},
		{"pushover", func(target string) schema.CodeNotification {
			return &schema.CodeNotificationPushover{URL: target, AppToken: "fixture-token", UserKey: "fixture-user"}
		}, func(t *testing.T, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("token") != "fixture-token" || r.Form.Get("user") != "fixture-user" || !strings.Contains(r.Form.Get("message"), "fixture") {
				t.Error("unexpected pushover form")
			}
		}},
		{"datadog", func(target string) schema.CodeNotification {
			return &schema.CodeNotificationDatadog{URL: target, APIKey: "fixture-key", AppKey: "fixture-app", Tags: []string{"env:fixture"}}
		}, func(t *testing.T, r *http.Request) {
			if r.Header.Get("DD-API-KEY") != "fixture-key" || r.Header.Get("DD-APPLICATION-KEY") != "fixture-app" {
				t.Error("missing datadog auth")
			}
			var body struct {
				Title string   `json:"title"`
				Text  string   `json:"text"`
				Tags  []string `json:"tags"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Title != "CPRA alert: fixture" || !strings.Contains(body.Text, "fixture") || len(body.Tags) != 1 || body.Tags[0] != "env:fixture" {
				t.Errorf("unexpected datadog body: %+v", body)
			}
		}},
	}
}

// Requests use the production client and a real loopback listener. Only the
// endpoint configuration differs from sending to the provider service.
func testNotificationEndpoint(t *testing.T, tt endpointCase) {
	t.Helper()
	previous := SSRFProtect
	SSRFProtect = false
	t.Cleanup(func() { SSRFProtect = previous })
	var count atomic.Int32
	var code atomic.Int32
	code.Store(http.StatusAccepted)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/operation/fixture-secret" || r.URL.Query().Get("key") != "query-secret" {
			t.Errorf("unexpected destination or method: %s %s", r.Method, r.URL.Path)
		}
		tt.verify(t, r)
		w.Header().Set("Location", "/unexpected-redirect")
		w.WriteHeader(int(code.Load()))
		_, _ = io.WriteString(w, "{}")
	}))
	defer server.Close()
	target := server.URL + "/operation/fixture-secret?key=query-secret"
	job, err := CreateCodeJob("fixture", schema.CodeConfig{Notify: tt.name, Config: tt.config(target)}, ecs.Entity{}, "red")
	if err != nil {
		t.Fatal(err)
	}
	if result := job.Copy().Execute(); result.Err != nil {
		t.Fatal(result.Err)
	}
	if count.Load() != 1 {
		t.Fatalf("copy sent %d requests", count.Load())
	}
	code.Store(http.StatusTooManyRequests)
	var rejection *DeliveryError
	if result := job.Execute(); !errors.As(result.Err, &rejection) || !rejection.Retryable {
		t.Errorf("429 not classified as retryable rejection: %v", result.Err)
	}
	code.Store(http.StatusTemporaryRedirect)
	if result := job.Execute(); result.Err == nil {
		t.Error("redirect reported success")
	}
	if count.Load() != 3 {
		t.Fatalf("unexpected followup or retry: %d requests", count.Load())
	}
	SSRFProtect = true
	if result := job.Execute(); result.Err == nil {
		t.Error("private destination bypassed SSRF protection")
	}
	if count.Load() != 3 {
		t.Error("protected request reached loopback")
	}
	if _, err := CreateCodeJob("fixture", schema.CodeConfig{Notify: tt.name, Config: tt.config(target)}, ecs.Entity{}, "red"); err == nil {
		t.Error("constructor accepted blocked destination")
	}
	SSRFProtect = false
	server.Close()
	if result := job.Execute(); result.Err == nil {
		t.Error("closed endpoint reported success")
	} else if strings.Contains(result.Err.Error(), "fixture-secret") || strings.Contains(result.Err.Error(), "query-secret") {
		t.Errorf("endpoint credential leaked: %v", result.Err)
	}
	if _, err := CreateCodeJob("fixture", schema.CodeConfig{Notify: tt.name, Config: tt.config("file:///fixture-secret")}, ecs.Entity{}, "red"); err == nil {
		t.Error("invalid endpoint scheme accepted")
	}
}

func TestNotificationCustomEndpoints(t *testing.T) {
	for _, tt := range notificationEndpointCases() {
		t.Run(tt.name, func(t *testing.T) { testNotificationEndpoint(t, tt) })
	}
}

func TestTelegramTestEndpointSelection(t *testing.T) {
	for _, tt := range []struct {
		name, configured, token string
		test                    bool
		want                    string
		wantErr                 bool
	}{
		{name: "production", token: "123:fixture", want: "https://api.telegram.org/bot123:fixture/sendMessage"},
		{name: "test", token: "123:fixture", test: true, want: "https://api.telegram.org/bot123:fixture/test/sendMessage"},
		{name: "explicit", configured: "http://localhost:9876/send", token: "fixture", want: "http://localhost:9876/send"},
		{name: "conflict", configured: "https://fixture.invalid/send", test: true, wantErr: true},
		{name: "token cannot select test path", token: "123:fixture/test", want: "https://api.telegram.org/bot123:fixture%2Ftest/sendMessage"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := telegramURL(tt.configured, tt.token, tt.test)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("target=%q error=%v", got, err)
			}
		})
	}
	_, err := CreateCodeJob("fixture", schema.CodeConfig{Notify: "telegram", Config: &schema.CodeNotificationTelegram{URL: "https://fixture.invalid", TestMode: true}}, ecs.Entity{}, "red")
	if err == nil {
		t.Error("conflicting Telegram settings accepted")
	}
	if r := (&CodeTelegramJob{URL: "https://fixture.invalid", TestMode: true}).Execute(); r.Err == nil {
		t.Error("direct job accepted conflicting settings")
	}
}

// Default URL assertions are request-contract tests; they do not contact
// production services and do not count as provider sandbox evidence.
func TestNotificationDefaultURLs(t *testing.T) {
	var destination string
	notificationTestHTTP(t, func(r *http.Request) (*http.Response, error) {
		destination = r.URL.String()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})
	want := map[string]string{
		"telegram":  "https://api.telegram.org/bot123:fixture/sendMessage",
		"victorops": "https://alert.victorops.com/integrations/generic/20131114/alert/fixture/routing",
		"pushover":  "https://api.pushover.net/1/messages.json",
		"datadog":   "https://api.datadoghq.com/api/v1/events",
	}
	for _, tt := range notificationEndpointCases() {
		t.Run(tt.name, func(t *testing.T) {
			job, err := CreateCodeJob("fixture", schema.CodeConfig{Notify: tt.name, Config: tt.config("")}, ecs.Entity{}, "red")
			if err != nil {
				t.Fatal(err)
			}
			if r := job.Execute(); r.Err != nil {
				t.Fatal(r.Err)
			}
			if destination != want[tt.name] {
				t.Errorf("destination=%q want=%q", destination, want[tt.name])
			}
		})
	}
	job, err := CreateCodeJob("fixture", schema.CodeConfig{Notify: "datadog", Config: &schema.CodeNotificationDatadog{Site: "datadoghq.eu"}}, ecs.Entity{}, "red")
	if err != nil {
		t.Fatal(err)
	}
	if r := job.Execute(); r.Err != nil {
		t.Fatal(r.Err)
	}
	if destination != "https://api.datadoghq.eu/api/v1/events" {
		t.Errorf("configured Datadog site ignored: %s", destination)
	}
	job, err = CreateCodeJob("fixture", schema.CodeConfig{Notify: "telegram", Config: &schema.CodeNotificationTelegram{BotToken: "123:fixture", TestMode: true}}, ecs.Entity{}, "red")
	if err != nil {
		t.Fatal(err)
	}
	if r := job.Copy().Execute(); r.Err != nil {
		t.Fatal(r.Err)
	}
	if destination != "https://api.telegram.org/bot123:fixture/test/sendMessage" {
		t.Errorf("Telegram test environment ignored: %s", destination)
	}
}
