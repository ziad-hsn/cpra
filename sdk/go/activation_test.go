package cpra

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestActivatePathOnlyOriginalHandleAndUncertainResponses(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		id, header string
		ambiguous  bool
	}{
		{"admitted", 200, "original", "original", false},
		{"wrong-body", 200, "other", "", true},
		{"wrong-header", 200, "original", "other", true},
		{"wrong-status", 202, "original", "", true},
		{"uncertain", 503, "", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || r.URL.Path != "/api/v2/operations/original/activate" || len(body) != 0 || r.URL.RawQuery != "" {
					t.Error("activation carried input or changed handle")
				}
				w.Header().Set("X-Operation-ID", test.header)
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(api.Operation{ID: test.id, State: "applying"})
			}, func(c *Config) { c.ReadAttempts = 3 })
			r, err := c.Operations.Activate(context.Background(), "original")
			if errors.Is(err, ErrAmbiguous) != test.ambiguous || calls.Load() != 1 {
				t.Fatal("wrong admission classification or implicit mutation retry", err, calls.Load())
			}
			if !test.ambiguous && (err != nil || r.OperationID != "original") {
				t.Fatal(r, err)
			}
			if test.ambiguous {
				var uncertain *AmbiguousError
				if !errors.As(err, &uncertain) || uncertain.OperationID != "original" {
					t.Fatal("uncertain response lost original handle", err)
				}
			}
		})
	}
}

func TestWaitExecutionResultPreservesLastReceiptOnReadFailure(t *testing.T) {
	op := executionOperationFixture()
	op.ExecutionResult = &api.ExecutionResultAvailability{State: "pending"}
	op.Items = nil
	op.State = "applying"
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("wait mutated")
		}
		if calls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(op)
			return
		}
		w.WriteHeader(503)
	}, func(c *Config) { c.ReadAttempts = 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	r, err := c.Operations.WaitExecutionResult(ctx, op.ID)
	if !errors.Is(err, ErrUnavailable) || calls.Load() != 2 || r.Data.ID != op.ID || r.Data.Committed == nil || *r.Data.Committed != 1 || r.Data.ExecutionResult.State != "pending" {
		t.Fatal("read failure erased confirmed partial counts", r, err)
	}
}
