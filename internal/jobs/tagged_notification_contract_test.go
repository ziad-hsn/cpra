//go:build teams && twilio

package jobs

import (
	"cpra/internal/loader/schema"
	"errors"
	"github.com/mlange-42/ark/ecs"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTaggedNotificationRejections(t *testing.T) {
	notificationTestHTTP(t, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Status: "429 Too Many Requests", Body: io.NopCloser(strings.NewReader("rejected")), Header: make(http.Header)}, nil
	})
	cases := []struct {
		name   string
		config schema.CodeNotification
	}{{"teams", &schema.CodeNotificationTeams{WebhookURL: "https://fixture.invalid"}}, {"twilio", &schema.CodeNotificationTwilio{AccountSID: "fixture", AuthToken: "fixture", From: "fixture", To: "fixture"}}}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			j, err := CreateCodeJob("fixture", schema.CodeConfig{Notify: tt.name, Config: tt.config}, ecs.Entity{}, "red")
			if err != nil {
				t.Fatal(err)
			}
			r := j.Execute()
			var d *DeliveryError
			if !errors.As(r.Err, &d) || !d.Retryable {
				t.Errorf("HTTP 429 will not retry: %v", r.Err)
			}
		})
	}
}
