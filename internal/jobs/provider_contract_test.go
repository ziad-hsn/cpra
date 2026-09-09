package jobs

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpra/internal/loader/schema"
	"github.com/mlange-42/ark/ecs"
)

type notificationTestTransport func(*http.Request) (*http.Response, error)

func (f notificationTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func notificationTestHTTP(t *testing.T, fn notificationTestTransport) {
	t.Helper()
	old := SSRFProtect
	SSRFProtect = false
	t.Cleanup(func() { SSRFProtect = old })
	for _, single := range []bool{false, true} {
		key := httpClientKey{10 * time.Second, false, false, single}
		previous, had := httpClientPool.Load(key)
		httpClientPool.Store(key, &http.Client{Transport: fn})
		t.Cleanup(func() {
			if had {
				httpClientPool.Store(key, previous)
			} else {
				httpClientPool.Delete(key)
			}
		})
	}
}
func TestRetryableNotificationRejections(t *testing.T) {
	notificationTestHTTP(t, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Status: "429 Too Many Requests", Body: io.NopCloser(strings.NewReader("rejected")), Header: make(http.Header)}, nil
	})
	cases := []struct {
		name   string
		config schema.CodeNotification
	}{
		{"slack", &schema.CodeNotificationSlack{WebHook: "https://fixture.invalid"}},
		{"discord", &schema.CodeNotificationDiscord{WebhookURL: "https://fixture.invalid"}},
		{"mattermost", &schema.CodeNotificationMattermost{WebhookURL: "https://fixture.invalid"}},
		{"telegram", &schema.CodeNotificationTelegram{BotToken: "fixture", ChatID: "1"}},
		{"pushover", &schema.CodeNotificationPushover{AppToken: "fixture", UserKey: "fixture"}},
		{"victorops", &schema.CodeNotificationVictorOps{RestEndpointKey: "fixture", RoutingKey: "fixture"}},
		{"opsgenie", &schema.CodeNotificationOpsgenie{APIKey: "fixture"}},
		{"datadog", &schema.CodeNotificationDatadog{APIKey: "fixture"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			j, err := CreateCodeJob("fixture", schema.CodeConfig{Notify: tt.name, Config: tt.config}, ecs.Entity{}, "red")
			if err != nil {
				t.Fatal(err)
			}
			result := j.Execute()
			var delivery *DeliveryError
			if !errors.As(result.Err, &delivery) || !delivery.Retryable {
				t.Errorf("HTTP 429 will not retry: %T %v", result.Err, result.Err)
			}
		})
	}
}
func TestOpsgenieMessageLimit(t *testing.T) {
	var payload map[string]string
	notificationTestHTTP(t, func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})
	j, err := CreateCodeJob("api", schema.CodeConfig{Notify: "opsgenie", Config: &schema.CodeNotificationOpsgenie{APIKey: "fixture"}}, ecs.Entity{}, "red")
	if err != nil {
		t.Fatal(err)
	}
	if r := j.Execute(); r.Err != nil {
		t.Fatal(r.Err)
	}
	if n := len([]rune(payload["message"])); n > 130 {
		t.Errorf("Opsgenie message has %d characters; API allows 130; description=%q", n, payload["description"])
	}
}
func TestPushoverEmergencyParameters(t *testing.T) {
	var retry, expire string
	notificationTestHTTP(t, func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		retry = r.Form.Get("retry")
		expire = r.Form.Get("expire")
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})
	j, err := CreateCodeJob("api", schema.CodeConfig{Notify: "pushover", Config: &schema.CodeNotificationPushover{AppToken: "fixture", UserKey: "fixture", Priority: 2}}, ecs.Entity{}, "red")
	if err != nil {
		t.Fatal(err)
	}
	j.Execute()
	if retry == "" || expire == "" {
		t.Errorf("priority 2 payload omits required retry/expire: %q %q", retry, expire)
	}
}
func TestDockerDefaultStopGrace(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/restart") {
			got = r.URL.Query().Get("t")
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", srv.URL)
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "1.52")
	j, err := CreateInterventionJob(schema.Intervention{Action: "docker", Target: &schema.InterventionTargetDocker{Container: "fixture"}}, ecs.Entity{})
	if err != nil {
		t.Fatal(err)
	}
	if r := j.Execute(); r.Err != nil {
		t.Fatal(r.Err)
	}
	if got == "0" {
		t.Errorf("omitted operation timeout sends t=0, forcing immediate SIGKILL instead of default container grace")
	}
}
