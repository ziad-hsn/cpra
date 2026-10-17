package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func TestFailureCodePreservesTypedCausesWithoutErrorText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{context.DeadlineExceeded, "deadline_exceeded"},
		{context.Canceled, "cancelled"},
		{&net.DNSError{Err: "private-provider-response", Name: "credential-host"}, "dns_failed"},
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("private-provider-response")}, "connection_failed"},
		{&HTTPStatusError{Status: 503}, "unexpected_http_status"},
		{&DeliveryError{Status: 429, Retryable: true}, "rejected"},
		{ErrJobPanic, "job_panicked"},
		{errors.New("password=private-credential " + strings.Repeat("private-provider-response", 1000)), "operation_failed"},
	} {
		var err error
		if tc.err != nil {
			err = SanitizeError(fmt.Errorf("driver failed: %w", tc.err))
		}
		if got := FailureCode(err); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}

func TestNotificationUsesPrivateDiagnosticContext(t *testing.T) {
	var mu sync.Mutex
	var messages []string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		mu.Lock()
		messages = append(messages, input.Text)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	prototype := &CodeSlackJob{Message: "Service is unhealthy", WebhookURL: receiver.URL}
	d := NewDispatch(prototype, ecs.Entity{}, "code", "red", 0, 0)
	d.FailureCode, d.FailureMessage = "dns_failed", "The target name could not be resolved."
	if result := d.Execute(); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := NewDispatch(prototype, ecs.Entity{}, "code", "red", 0, 0).Execute(); result.Err != nil {
		t.Fatal(result.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(messages) != 2 || !strings.Contains(messages[0], "Cause: dns_failed") || strings.Contains(messages[1], "Cause:") {
		t.Fatal("diagnostic context leaked into reusable template", messages)
	}
}

func TestAgentNotificationCarriesOnlySafeDiagnosticFields(t *testing.T) {
	d := &Dispatch{Kind: "code", FailureCode: "rejected", FailureMessage: "The provider explicitly rejected the operation."}
	raw, err := json.Marshal(d.agentAction())
	if err != nil || !strings.Contains(string(raw), `"failure":{"code":"rejected","message":"The provider explicitly rejected the operation."}`) {
		t.Fatal("agent notification lost safe diagnostic", err)
	}
}

func TestFailureDetailsExcludeRemoteText(t *testing.T) {
	err := fmt.Errorf("secret-provider-body: %w", &HTTPStatusError{Status: 502})
	if detail := FailureDetails(SanitizeError(err)); detail.HTTPStatus != 502 || detail.GRPCCode != "" {
		t.Fatal(detail)
	}
	if detail := FailureDetails(errors.New("HTTP 502 with credential-marker")); detail != (FailureDetail{}) {
		t.Fatal("text parsed into trusted detail")
	}
}
