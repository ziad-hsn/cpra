package jobs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPulseRetriesPreserveFullFirstAttemptBudget(t *testing.T) {
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-time.After(100 * time.Millisecond):
			w.WriteHeader(200)
		case <-r.Context().Done():
		}
	}))
	defer target.Close()
	job := &PulseHTTPJob{URL: target.URL, Method: "GET", Timeout: 200 * time.Millisecond, Retries: 3}
	result := job.Execute()
	if result.Err != nil || result.Attempts != 1 || calls.Load() != 1 {
		t.Fatalf("slow successful first attempt was cut short: %+v calls=%d", result, calls.Load())
	}
	if result.AttemptDuration < 100*time.Millisecond || result.RetryDelay != 0 || result.AttemptTimeouts != 0 {
		t.Fatalf("attempt evidence: %+v", result)
	}
}

func TestPulseRetriesTrackAttemptsAndRemainWithinOperationDeadline(t *testing.T) {
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer target.Close()
	job := &PulseHTTPJob{URL: target.URL, Method: "GET", Timeout: time.Second, Retries: 2}
	started := time.Now()
	result := job.Execute()
	elapsed := time.Since(started)
	if result.Err != nil || result.Attempts != 2 || calls.Load() != 2 || result.RetryDelay <= 0 {
		t.Fatalf("transient retry evidence: %+v calls=%d", result, calls.Load())
	}
	if result.AttemptDuration+result.RetryDelay > elapsed {
		t.Fatalf("overlapping attempt/delay metrics: %+v elapsed=%v", result, elapsed)
	}
}

func TestPulseTimeoutDoesNotRetryExpiredBudget(t *testing.T) {
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-r.Context().Done() }))
	defer target.Close()
	job := &PulseHTTPJob{URL: target.URL, Method: "GET", Timeout: 40 * time.Millisecond, Retries: 5}
	started := time.Now()
	result := job.Execute()
	if !errors.Is(result.Err, context.DeadlineExceeded) || result.Attempts != 1 || calls.Load() != 1 || result.AttemptTimeouts != 1 {
		t.Fatalf("expired operation retried: %+v calls=%d", result, calls.Load())
	}
	if time.Since(started) > 300*time.Millisecond {
		t.Fatal("check exceeded total budget")
	}
}

func TestPulseUnsafeMethodAndPermanentFailureNeverRetry(t *testing.T) {
	for _, tc := range []struct {
		method string
		status int
	}{{"POST", 503}, {"PUT", 503}, {"GET", 401}} {
		t.Run(tc.method+http.StatusText(tc.status), func(t *testing.T) {
			var calls atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(tc.status) }))
			defer target.Close()
			result := (&PulseHTTPJob{URL: target.URL, Method: tc.method, Timeout: time.Second, Retries: 5}).Execute()
			if result.Err == nil || result.Attempts != 1 || calls.Load() != 1 || result.RetryDelay != 0 {
				t.Fatalf("unexpected replay: %+v calls=%d", result, calls.Load())
			}
		})
	}
}

func TestPulseRetryCancellationStopsBeforeNextAttempt(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cancel(); w.WriteHeader(503) }))
	defer target.Close()
	job := &PulseHTTPJob{URL: target.URL, Method: "GET", Timeout: time.Second, Retries: 5}
	job.SetContext(parent)
	result := job.Execute()
	if !errors.Is(result.Err, context.Canceled) || result.Attempts != 1 {
		t.Fatalf("retry ignored parent cancellation: %+v", result)
	}
}
