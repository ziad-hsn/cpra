package jobs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type notificationReadFailure struct{}

func (notificationReadFailure) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }
func (notificationReadFailure) Close() error             { return nil }

func TestNotificationBodyFailurePreservesTimeoutWithoutResponseText(t *testing.T) {
	err := notificationResponseError("telegram", &http.Response{StatusCode: 200, Header: make(http.Header), Body: notificationReadFailure{}})
	var rejection *DeliveryError
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &rejection) || FailureCode(err) != "deadline_exceeded" {
		t.Fatal("body timeout lost cause or authorized repetition", err)
	}
}

func TestNotificationProviderRejectionContract(t *testing.T) {
	for _, tc := range []struct {
		name, provider, body, retryAfter string
		status                           int
		rejected, retryable, success     bool
		delay                            time.Duration
	}{
		{name: "webhook rate status is insufficient", provider: "webhook", status: 429, body: "private-provider-response", retryAfter: "10"},
		{name: "slack rate", provider: "slack", status: 429, retryAfter: "10", rejected: true, retryable: true, delay: 10 * time.Second},
		{name: "slack invalid wait", provider: "slack", status: 429, retryAfter: "invalid", rejected: true},
		{name: "slack permanent restriction", provider: "slack", status: 403, body: "action_prohibited", rejected: true},
		{name: "slack unknown body", provider: "slack", status: 403, body: "private-provider-response"},
		{name: "discord fractional rate", provider: "discord", status: 429, body: `{"retry_after":0.1234}`, rejected: true, retryable: true, delay: 123400000 * time.Nanosecond},
		{name: "discord larger header", provider: "discord", status: 429, body: `{"retry_after":0.1}`, retryAfter: "3", rejected: true, retryable: true, delay: 3 * time.Second},
		{name: "discord contradictory body", provider: "discord", status: 429, body: `{"retry_after":1,"retry_after":0}`},
		{name: "telegram rate", provider: "telegram", status: 429, body: `{"ok":false,"error_code":429,"parameters":{"retry_after":12},"description":"private-provider-response"}`, rejected: true, retryable: true, delay: 12 * time.Second},
		{name: "telegram success", provider: "telegram", status: 200, body: `{"ok":true,"result":{}}`, success: true},
		{name: "telegram body rejection", provider: "telegram", status: 200, body: `{"ok":false,"error_code":400}`, rejected: true},
		{name: "telegram missing outcome", provider: "telegram", status: 200, body: `{}`},
		{name: "pushover quota", provider: "pushover", status: 429, body: `{"status":0,"errors":["private-provider-response"]}`, retryAfter: "1", rejected: true},
		{name: "pushover success", provider: "pushover", status: 200, body: `{"status":1}`, success: true},
		{name: "teams body throttle", provider: "teams", status: 200, body: "Microsoft Teams endpoint returned HTTP error 429", rejected: true, retryable: true},
		{name: "twilio documented throttle", provider: "twilio", status: 429, rejected: true, retryable: true},
		{name: "pagerduty rate guidance", provider: "pagerduty", status: 429, rejected: true, retryable: true, delay: 2 * time.Minute},
		{name: "opsgenie explicit throttle", provider: "opsgenie", status: 429, rejected: true, retryable: true},
		{name: "datadog unknown rejection", provider: "datadog", status: 429, body: "private-provider-response"},
		{name: "mattermost unknown rejection", provider: "mattermost", status: 429, body: "private-provider-response"},
		{name: "victorops unknown rejection", provider: "victorops", status: 429, body: "private-provider-response"},
		{name: "provider server failure", provider: "slack", status: 500, body: "invalid_payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}
			if tc.retryAfter != "" {
				response.Header.Set("Retry-After", tc.retryAfter)
			}
			err := notificationResponseError(tc.provider, response)
			if (err == nil) != tc.success {
				t.Fatal("incorrect success disposition", err)
			}
			var rejection *DeliveryError
			if errors.As(err, &rejection) != tc.rejected {
				t.Fatal("incorrect effect certainty", err)
			}
			if rejection != nil && (rejection.Retryable != tc.retryable || rejection.RetryAfter != tc.delay) {
				t.Fatal("incorrect retry permission/delay", rejection)
			}
			if err != nil && strings.Contains(err.Error(), "private-provider-response") {
				t.Fatal("provider body leaked")
			}
		})
	}
}

func TestNotificationResponseLimitsDoNotAuthorizeReplay(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", maxNotificationResponse+1), `{"ok":false,"error_code":429,"error_code":400}`} {
		err := notificationResponseError("telegram", &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))})
		var rejection *DeliveryError
		if err == nil || errors.As(err, &rejection) {
			t.Fatal("oversized or contradictory reply authorized replay")
		}
	}
}
