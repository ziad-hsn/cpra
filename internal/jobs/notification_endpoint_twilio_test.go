//go:build twilio

package jobs

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"cpra/internal/loader/schema"
	"github.com/mlange-42/ark/ecs"
)

func TestTwilioCustomEndpoint(t *testing.T) {
	testNotificationEndpoint(t, endpointCase{"twilio", func(target string) schema.CodeNotification {
		return &schema.CodeNotificationTwilio{URL: target, AccountSID: "fixture-sid", AuthToken: "fixture-token", From: "+15005550006", To: "+15005550001"}
	}, func(t *testing.T, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "fixture-sid" || password != "fixture-token" {
			t.Error("missing Twilio Basic auth")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("From") != "+15005550006" || r.Form.Get("To") != "+15005550001" || !strings.Contains(r.Form.Get("Body"), "fixture") {
			t.Error("unexpected Twilio SMS form")
		}
	}})
}

func TestTwilioDefaultEndpoint(t *testing.T) {
	var destination string
	notificationTestHTTP(t, func(r *http.Request) (*http.Response, error) {
		destination = r.URL.String()
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})
	job, err := CreateCodeJob("fixture", schema.CodeConfig{Notify: "twilio", Config: &schema.CodeNotificationTwilio{AccountSID: "ACfixture", AuthToken: "fixture"}}, ecs.Entity{}, "red")
	if err != nil {
		t.Fatal(err)
	}
	if r := job.Execute(); r.Err != nil {
		t.Fatal(r.Err)
	}
	if destination != "https://api.twilio.com/2010-04-01/Accounts/ACfixture/Messages.json" {
		t.Errorf("unexpected endpoint: %s", destination)
	}
}
