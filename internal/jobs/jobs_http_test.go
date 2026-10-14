package jobs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestPulseHTTPJobHeadersBodyAndStatus(t *testing.T) {
	var gotHeader, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Custom")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated) // 201
	}))
	defer srv.Close()

	job := &PulseHTTPJob{
		ID:             uuid.New(),
		URL:            srv.URL,
		Method:         http.MethodPost,
		Headers:        map[string]string{"X-Custom": "hello"},
		Body:           "payload",
		ExpectedStatus: []int{201},
		Retries:        0,
		Client:         *GetHTTPClient(time.Second),
		payload:        map[string]interface{}{},
	}

	res := job.Execute()
	if res.Err != nil {
		t.Fatalf("expected success, got %v", res.Err)
	}
	if gotHeader != "hello" {
		t.Fatalf("expected header 'hello', got %q", gotHeader)
	}
	if gotBody != "payload" {
		t.Fatalf("expected body 'payload', got %q", gotBody)
	}
}

func TestPulseHTTPJobStatusOK(t *testing.T) {
	job := &PulseHTTPJob{}
	if !job.statusOK(200) {
		t.Fatal("200 should be OK by default")
	}
	if job.statusOK(404) {
		t.Fatal("404 should not be OK by default")
	}

	job.ExpectedStatus = []int{200, 404}
	if !job.statusOK(404) {
		t.Fatal("404 should be OK when expected")
	}
	if job.statusOK(500) {
		t.Fatal("500 should not be OK when expected is [200,404]")
	}
}

func TestPulseHTTPJobExpectedStatusDoesNotFollowRedirect(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		expected []int
		healthy  bool
	}{
		{name: "default no content", status: http.StatusNoContent, healthy: true},
		{name: "default last 2xx", status: 299, healthy: true},
		{name: "empty expected list", status: http.StatusCreated, expected: []int{}, healthy: true},
		{name: "default found", status: http.StatusFound},
		{name: "default temporary redirect", status: http.StatusTemporaryRedirect},
		{name: "configured found", status: http.StatusFound, expected: []int{http.StatusFound}, healthy: true},
		{name: "configured temporary redirect", status: http.StatusTemporaryRedirect, expected: []int{http.StatusTemporaryRedirect}, healthy: true},
		{name: "different redirect", status: http.StatusTemporaryRedirect, expected: []int{http.StatusFound}},
		{name: "configured list replaces default", status: http.StatusOK, expected: []int{http.StatusFound}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var destinationCalls, originCalls atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				destinationCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer destination.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				originCalls.Add(1)
				w.Header().Set("Location", destination.URL)
				w.WriteHeader(tc.status)
			}))
			defer origin.Close()

			job, err := CreatePulseJob(manifest.Pulse{
				Type: "http", Timeout: time.Second,
				Config: &manifest.PulseHTTPConfig{Url: origin.URL, ExpectedStatus: tc.expected},
			}, ecs.Entity{})
			if err != nil {
				t.Fatal(err)
			}
			result := job.Execute()
			if healthy := result.Err == nil; healthy != tc.healthy {
				t.Errorf("healthy = %t, want %t; error = %v", healthy, tc.healthy, result.Err)
			}
			if calls := originCalls.Load(); calls != 1 {
				t.Errorf("origin received %d requests, want 1", calls)
			}
			if calls := destinationCalls.Load(); calls != 0 {
				t.Errorf("redirect destination received %d requests, want 0", calls)
			}
		})
	}
}
