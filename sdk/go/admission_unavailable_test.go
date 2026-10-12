package cpra_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

const unavailableAdmissionProblem = `{"type":"about:blank","title":"Service Unavailable","status":503,"code":"admissionUnavailable","detail":"This request was not admitted."}`

func TestConsumerRecognizesOnlyCompleteAdmissionUnavailableContract(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(http.Header)
		body   string
		proof  bool
	}{
		{name: "complete", proof: true},
		{name: "original handle", change: func(h http.Header) { h.Set("X-Operation-ID", consumerHandle) }, proof: true},
		{name: "different original", change: func(h http.Header) {
			h.Set("X-Operation-ID", strings.Replace(consumerHandle, "00000000000000000001", "00000000000000000002", 1))
		}},
		{name: "malformed handle", change: func(h http.Header) { h.Set("X-Operation-ID", "unsafe-handle") }},
		{name: "missing marker", change: func(h http.Header) { h.Del("X-CPRa-Admission") }},
		{name: "missing retry", change: func(h http.Header) { h.Del("Retry-After") }},
		{name: "unbounded retry", change: func(h http.Header) { h.Set("Retry-After", "86400") }},
		{name: "duplicate retry", change: func(h http.Header) { h.Add("Retry-After", "5") }},
		{name: "truncated", body: strings.TrimSuffix(unavailableAdmissionProblem, "}")},
		{name: "post admission", body: strings.ReplaceAll(unavailableAdmissionProblem, "admissionUnavailable", "outcomeUnconfirmed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client := consumerClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/problem+json")
				w.Header().Set("X-CPRa-Admission", "not-submitted")
				w.Header().Set("Retry-After", "5")
				if test.change != nil {
					test.change(w.Header())
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				body := test.body
				if body == "" {
					body = unavailableAdmissionProblem
				}
				_, _ = io.WriteString(w, body)
			})
			_, err := client.Operations.Validate(context.Background(), consumerHandle)
			if errors.Is(err, cpra.ErrNotAdmitted) != test.proof || errors.Is(err, cpra.ErrAmbiguous) == test.proof || calls.Load() != 1 {
				t.Fatal("incorrect classification or automatic mutation retry", err)
			}
		})
	}
}
