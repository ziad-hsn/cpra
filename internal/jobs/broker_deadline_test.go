//go:build redis && rabbitmq

package jobs

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBrokerTimeoutIsHonored(t *testing.T) {
	for _, protocol := range []string{"redis", "rabbitmq"} {
		t.Run(protocol, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				conn, err := ln.Accept()
				if err == nil {
					accepted <- conn
				}
			}()
			var job Job
			if protocol == "redis" {
				job = &PulseRedisJob{Addr: ln.Addr().String(), Timeout: 25 * time.Millisecond}
			} else {
				job = &PulseRabbitMQJob{URL: "amqp://guest:guest@" + ln.Addr().String() + "/", Timeout: 25 * time.Millisecond}
			}
			done := make(chan Result, 1)
			started := time.Now()
			go func() { done <- job.Execute() }()
			var connection net.Conn
			select {
			case connection = <-accepted:
			case <-time.After(time.Second):
				t.Fatal("no connection")
			}
			expired := false
			select {
			case <-done:
			case <-time.After(200 * time.Millisecond):
				expired = true
			}
			_ = connection.Close()
			_ = ln.Close()
			if expired {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("job did not finish after connection closed")
				}
				t.Errorf("%s check ignored 25ms timeout and remained blocked after 200ms (total cleanup %s)", protocol, time.Since(started))
			}
		})
	}
}

func TestPulseErrorsRedactQueryCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()
	job := &PulseHTTPJob{URL: server.URL + "/?api_key=local-review-secret", Method: "GET", Client: *GetHTTPClient(time.Second)}
	result := job.Execute()
	if result.Error() == nil {
		t.Fatal("expected connection failure")
	}
	if containsReviewSecret(result.Error().Error()) {
		t.Fatalf("pulse error forwarded to logs exposes credential: %v", result.Error())
	}
}

func containsReviewSecret(value string) bool {
	for i := 0; i+len("local-review-secret") <= len(value); i++ {
		if value[i:i+len("local-review-secret")] == "local-review-secret" {
			return true
		}
	}
	return false
}
