package jobs

import (
	"bufio"
	"context"
	"cpra/internal/loader/schema"
	"encoding/json"
	"github.com/mlange-42/ark/ecs"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotificationsReachDestination(t *testing.T) {
	for _, kind := range []string{"slack", "pagerduty", "webhook"} {
		t.Run(kind, func(t *testing.T) {
			var received atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer func() { _ = r.Body.Close() }()
				var body map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if kind == "slack" && !strings.Contains(body["text"].(string), "fixture-monitor") {
					t.Error("missing alert message")
				}
				if kind == "pagerduty" && body["routing_key"] != "fixture-key" {
					t.Error("routing key discarded")
				}
				if kind == "webhook" && (body["color"] != "red" || r.Header.Get("X-Fixture") != "value") {
					t.Error("webhook configuration discarded")
				}
				received.Add(1)
				w.WriteHeader(202)
			}))
			defer server.Close()
			var cfg schema.CodeNotification
			switch kind {
			case "slack":
				cfg = &schema.CodeNotificationSlack{WebHook: server.URL}
			case "pagerduty":
				cfg = &schema.CodeNotificationPagerDuty{URL: server.URL, RoutingKey: "fixture-key"}
			case "webhook":
				cfg = &schema.CodeNotificationWebhook{URL: server.URL, Headers: map[string]string{"X-Fixture": "value"}}
			}
			job, err := CreateCodeJob("fixture-monitor", schema.CodeConfig{Notify: kind, Config: cfg}, ecs.Entity{}, "red")
			if err != nil {
				t.Fatal(err)
			}
			if result := job.Execute(); result.Err != nil {
				t.Fatal(result.Err)
			}
			if received.Load() != 1 {
				t.Fatalf("got %d requests", received.Load())
			}
		})
	}
}
func TestNotificationHonorsOwnerCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
	defer server.Close()
	defer close(release)
	job := &CodeDiscordJob{WebhookURL: server.URL, Message: "fixture"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job.SetContext(ctx)
	done := make(chan Result, 1)
	go func() { done <- job.Execute() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no request")
	}
	cancel()
	select {
	case result := <-done:
		if result.Err == nil {
			t.Fatal("cancelled notification reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("notification ignored owner cancellation")
	}
}
func TestHTTPRedirectCannotReachSecondDestination(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	result := (&CodeDiscordJob{WebhookURL: redirect.URL}).Execute()
	if result.Err == nil || reached.Load() != 0 {
		t.Fatalf("redirect followed: reached=%d error=%v", reached.Load(), result.Err)
	}
}

func TestEmailRequiresActualRelayAcceptance(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan string, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		reader := bufio.NewReader(conn)
		write := func(line string) { _, _ = io.WriteString(conn, line+"\r\n") }
		write("220 fixture SMTP ready")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				write("250 fixture")
			case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
				write("250 accepted")
			case strings.HasPrefix(line, "DATA"):
				write("354 send message")
				var message strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
					message.WriteString(line)
				}
				accepted <- message.String()
				write("250 queued")
			default:
				write("221 bye")
				return
			}
		}
	}()
	cfg := schema.CodeNotificationEmail{Server: ln.Addr().String(), From: "cpra@example.com", To: "ops@example.com", Subject: "fixture", AllowInsecure: true}
	job, err := CreateCodeJob("fixture-monitor", schema.CodeConfig{Notify: "email", Config: &cfg}, ecs.Entity{}, "red")
	if err != nil {
		t.Fatal(err)
	}
	result := job.Execute()
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	select {
	case body := <-accepted:
		if !strings.Contains(body, "fixture-monitor") || !strings.Contains(body, "To: <ops@example.com>") {
			t.Fatalf("incomplete email: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("success without SMTP receipt")
	}
	<-serverDone
}
