package jobs

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const maxNotificationResponse = 64 << 10

// NotificationResponseError retains received response metadata independently
// of the provider's outcome evidence. Only its wrapped DeliveryError, if any,
// can establish rejection; Status is never retry permission.
type NotificationResponseError struct {
	Status int
	cause  error
}

func (e *NotificationResponseError) Error() string { return e.cause.Error() }
func (e *NotificationResponseError) Unwrap() error { return e.cause }

func unknownNotificationResponse(status int) error {
	return fmt.Errorf("notification outcome unknown (HTTP %d)", status)
}

// notificationResponseError interprets only the selected provider's rejection
// contract. It never replays a request or exposes a provider response body.
func notificationResponseError(provider string, response *http.Response) (err error) {
	status := response.StatusCode
	defer func() {
		if err != nil {
			err = &NotificationResponseError{Status: status, cause: err}
		}
	}()
	accepted := status >= 200 && status < 300
	if status >= 500 || status == http.StatusRequestTimeout || status >= 300 && status < 400 {
		return unknownNotificationResponse(status)
	}
	if provider == "webhook" || provider == "victorops" || provider == "datadog" || provider == "mattermost" {
		if accepted {
			return nil
		}
		return unknownNotificationResponse(status)
	}
	if status == http.StatusTooManyRequests && (provider == "pagerduty" || provider == "opsgenie" || provider == "twilio" || provider == "slack") {
		delay, valid := notificationRetryAfter(response.Header, time.Now())
		if len(response.Header.Values("Retry-After")) != 0 && !valid {
			return &DeliveryError{Status: status}
		}
		if provider == "pagerduty" && !valid {
			// PagerDuty recommends a delay of minutes. That cannot fit the
			// ordinary 45-second budget, so do not schedule an immediate retry.
			delay, valid = 2*time.Minute, true
		}
		return &DeliveryError{Status: status, Retryable: valid || provider == "opsgenie" || provider == "twilio", RetryAfter: delay}
	}
	if provider == "opsgenie" || provider == "pagerduty" {
		if accepted {
			return nil
		}
		if status == http.StatusBadRequest || provider == "opsgenie" && (status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusUnprocessableEntity) {
			return &DeliveryError{Status: status}
		}
		return unknownNotificationResponse(status)
	}
	if response.Body == nil || response.ContentLength > maxNotificationResponse {
		return unknownNotificationResponse(status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxNotificationResponse+1))
	if err != nil {
		return safeError{original: err, message: unknownNotificationResponse(status).Error()}
	}
	if len(raw) > maxNotificationResponse {
		return unknownNotificationResponse(status)
	}
	switch provider {
	case "slack":
		if accepted {
			return nil
		}
		switch strings.TrimSpace(string(raw)) {
		case "invalid_payload", "invalid_token", "action_prohibited", "channel_is_archived", "channel_not_found", "user_not_found", "no_active_hooks", "no_service", "no_service_id", "no_team", "no_text", "team_disabled", "posting_to_general_channel_denied", "too_many_attachments":
			return &DeliveryError{Status: status}
		}
	case "teams":
		if strings.Contains(string(raw), "Microsoft Teams endpoint returned HTTP error 429") || status == http.StatusTooManyRequests {
			delay, valid := notificationRetryAfter(response.Header, time.Now())
			if len(response.Header.Values("Retry-After")) != 0 && !valid {
				return &DeliveryError{Status: http.StatusTooManyRequests}
			}
			return &DeliveryError{Status: http.StatusTooManyRequests, Retryable: true, RetryAfter: delay}
		}
		if accepted {
			return nil
		}
	case "telegram", "pushover", "discord", "twilio":
		var result struct {
			OK         *bool    `json:"ok,omitempty"`
			Status     *int     `json:"status,omitempty"`
			ErrorCode  *int     `json:"error_code,omitempty"`
			Code       *int     `json:"code,omitempty"`
			RetryAfter *float64 `json:"retry_after,omitempty"`
			Parameters struct {
				RetryAfter *int64 `json:"retry_after,omitempty"`
			} `json:"parameters,omitempty"`
		}
		if provider == "discord" && accepted || provider == "twilio" && accepted {
			return nil
		}
		if decodeNotificationResponse(raw, &result) != nil {
			return unknownNotificationResponse(status)
		}
		switch provider {
		case "telegram":
			if accepted && result.OK != nil && *result.OK {
				return nil
			}
			if result.OK != nil && !*result.OK && result.ErrorCode != nil && *result.ErrorCode >= 400 && *result.ErrorCode < 500 && (*result.ErrorCode == status || accepted) {
				delay := time.Duration(0)
				if seconds := result.Parameters.RetryAfter; seconds != nil && *seconds >= 0 && *seconds <= 86400 {
					delay = time.Duration(*seconds) * time.Second
				}
				return &DeliveryError{Status: *result.ErrorCode, Retryable: *result.ErrorCode == 429 && result.Parameters.RetryAfter != nil && *result.Parameters.RetryAfter >= 0 && *result.Parameters.RetryAfter <= 86400, RetryAfter: delay}
			}
		case "pushover":
			if accepted && result.Status != nil && *result.Status == 1 {
				return nil
			}
			if result.Status != nil && *result.Status == 0 && (accepted || status >= 400 && status < 500) {
				return &DeliveryError{Status: status}
			}
		case "discord":
			if status == 429 && result.RetryAfter != nil && !math.IsNaN(*result.RetryAfter) && !math.IsInf(*result.RetryAfter, 0) && *result.RetryAfter >= 0 && *result.RetryAfter <= 86400 {
				delay := time.Duration(math.Ceil(*result.RetryAfter * float64(time.Second)))
				header, valid := notificationRetryAfter(response.Header, time.Now())
				if len(response.Header.Values("Retry-After")) != 0 && !valid {
					return &DeliveryError{Status: status}
				}
				if valid {
					delay = max(delay, header)
				}
				return &DeliveryError{Status: status, Retryable: true, RetryAfter: delay}
			}
			if result.Code != nil && (status == 400 || status == 401 || status == 403 || status == 404) {
				switch *result.Code {
				case 10015, 50013, 50027, 50035:
					return &DeliveryError{Status: status}
				}
			}
		case "twilio":
			if result.Code != nil && result.Status != nil && *result.Status == status && status >= 400 && status < 500 && *result.Code >= 20000 && *result.Code < 30000 {
				return &DeliveryError{Status: status}
			}
		}
	}
	return unknownNotificationResponse(status)
}

func decodeNotificationResponse(raw []byte, out any) error {
	var fields map[string]json.RawMessage
	if err := api.DecodeResponse(raw, &fields); err != nil {
		return err
	}
	// encoding/json binds struct fields case-insensitively. These providers use
	// exact wire names; aliases must not overwrite outcome or retry evidence.
	if !notificationExactKeys(fields, "ok", "status", "error_code", "code", "retry_after", "parameters") {
		return fmt.Errorf("notification response has ambiguous fields")
	}
	if parameters, exists := fields["parameters"]; exists {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(parameters, &nested); err != nil || !notificationExactKeys(nested, "retry_after") {
			return fmt.Errorf("notification response has ambiguous parameters")
		}
	}
	return json.Unmarshal(raw, out)
}

func notificationExactKeys(fields map[string]json.RawMessage, known ...string) bool {
	for key := range fields {
		for _, name := range known {
			if key != name && strings.EqualFold(key, name) {
				return false
			}
		}
	}
	return true
}

func notificationRetryAfter(headers http.Header, now time.Time) (time.Duration, bool) {
	values := headers.Values("Retry-After")
	if len(values) != 1 || len(values[0]) > 128 {
		return 0, false
	}
	value := strings.TrimSpace(values[0])
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil && seconds <= 86400 {
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		delay := max(time.Duration(0), at.Sub(now))
		if delay <= 24*time.Hour {
			return delay, true
		}
	}
	return 0, false
}
