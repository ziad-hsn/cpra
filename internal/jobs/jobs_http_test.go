package jobs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
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
