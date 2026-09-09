package jobs

import (
	"cpra/internal/loader/schema"
	"github.com/mlange-42/ark/ecs"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotificationsRequireConfiguredDestination(t *testing.T) {
	world := ecs.NewWorld()
	entity := world.NewEntity()
	for _, channel := range []string{"slack", "pagerduty", "email", "webhook"} {
		t.Run(channel, func(t *testing.T) {
			job, err := CreateCodeJob("review", schema.CodeConfig{Notify: channel, Dispatch: true}, entity, "red")
			if err != nil {
				return
			}
			result := job.Execute()
			if result.Error() == nil {
				t.Errorf("%s reported successful delivery with no configured destination or transport", channel)
			}
		})
	}
}

func TestSSRFProtectionCoversDiscord(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(200)
	}))
	defer server.Close()
	previous := SSRFProtect
	SSRFProtect = true
	defer func() { SSRFProtect = previous }()
	if validateTargetURL(server.URL) == nil {
		t.Fatal("test setup: loopback must be rejected")
	}
	result := (&CodeDiscordJob{WebhookURL: server.URL, Message: "local review fixture", Color: "red"}).Execute()
	if requests.Load() != 0 || result.Error() == nil {
		t.Fatalf("SSRF protection enabled, but Discord made %d loopback requests and returned %v", requests.Load(), result.Error())
	}
}

func TestWebhookDoesNotReplayAnUnknownEffect(t *testing.T) {
	var effects atomic.Int32
	var identity atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		effects.Add(1)
		identity.Store(r.Header.Get("Idempotency-Key"))
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()
	job := &InterventionWebhookJob{URL: server.URL, Method: "POST", Timeout: 100 * time.Millisecond, Retries: 2}
	result := job.Execute()
	if effects.Load() != 1 {
		t.Fatalf("remote committed %d effects after lost responses, idempotency key=%q, result=%v", effects.Load(), identity.Load(), result.Error())
	}
}

func TestUDPMustFailWithoutResponse(t *testing.T) {
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	addr := listener.LocalAddr().(*net.UDPAddr)
	result := (&PulseUDPJob{Host: "127.0.0.1", Port: addr.Port, SendPayload: "ping", Timeout: 25 * time.Millisecond}).Execute()
	if result.Error() == nil {
		t.Fatal("UDP server never replied, but timed-out probe reported success")
	}
}

func TestWebhookDoesNotReplayOnAReusedConnection(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			var effects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/prime" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				effects.Add(1)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}))
			defer server.Close()
			timeout := time.Second
			response, err := GetHTTPClient(timeout).Get(server.URL + "/prime")
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			job := &InterventionWebhookJob{URL: server.URL + "/act", Method: method, Timeout: timeout, Headers: map[string]string{"Idempotency-Key": "fixture-key"}}
			result := job.Execute()
			if result.Err == nil || effects.Load() != 1 {
				t.Fatalf("lost response caused %d effects; error=%v", effects.Load(), result.Err)
			}
		})
	}
}
