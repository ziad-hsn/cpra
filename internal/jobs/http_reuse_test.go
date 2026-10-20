package jobs

import (
	"go/version"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPStatusChecksReuseSmallResponses(t *testing.T) {
	var connections, requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.ProtoMajor != 1 {
			t.Error("fixture requires HTTP/1")
		}
		_, _ = w.Write([]byte("{\"healthy\":true}\n"))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	defer GetHTTPClient(time.Second).CloseIdleConnections()
	job := &PulseHTTPJob{URL: server.URL, Method: http.MethodGet, Timeout: time.Second}
	for range 12 {
		if result := job.Execute(); result.Err != nil {
			t.Fatal(result.Err)
		}
		// A real check has a cadence. Give the transport's asynchronous drain
		// time to return this small response's connection to its idle pool.
		time.Sleep(10 * time.Millisecond)
	}
	if requests.Load() != 12 {
		t.Fatal("status-only checks changed request count", requests.Load())
	}
	t.Logf("compiler=%s HTTP/1 requests=%d TCP connections=%d", runtime.Version(), requests.Load(), connections.Load())
	// Go 1.26 remains functionally supported but does not promise automatic
	// unread-body draining. The release compiler includes the Go 1.27 behavior.
	if version.Compare(runtime.Version(), "go1.27") >= 0 && connections.Load() >= requests.Load() {
		t.Fatal("status-only checks did not reuse any small-response connection")
	}
}

func TestHTTPStatusCheckDoesNotWaitForSlowResponseBody(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	result := make(chan Result, 1)
	go func() {
		result <- (&PulseHTTPJob{URL: server.URL, Method: http.MethodGet, Timeout: 3 * time.Second}).Execute()
	}()
	select {
	case got := <-result:
		if got.Err != nil {
			t.Fatal(got.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("status check waited for an unused body instead of closing it")
	}
}
