package jobs

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestBluePagerDutyUsesIndependentMatchingCorrelation(t *testing.T) {
	var received []map[string]any
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		received = append(received, payload)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer receiver.Close()
	for _, phase := range []string{"health", "open", "close"} {
		color, correlation := "blue", "review-condition"
		var review *ReviewNotification
		if phase == "health" {
			color, correlation = "red", "health-episode"
		} else {
			review = &ReviewNotification{AlertID: correlation, ActionID: "uncertain-action", Phase: phase, Health: "unhealthy"}
			if phase == "close" {
				review.Reason, review.Health = "health_recovered", "healthy"
			}
		}
		job := &CodePagerDutyJob{URL: receiver.URL, RoutingKey: "test-destination", Color: color, Message: "health notice"}
		d := NewDispatch(job, ecs.Entity{}, "code", color, 0, 0)
		d.MonitorID, d.FileIncarnation, d.EndpointID = "service", "incarnation", "endpoint"
		d.NotificationCorrelation, d.ProviderCorrelationVersion, d.ReviewNotification = correlation, 1, review
		if result := d.Execute(); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if len(received) != 3 || received[0]["dedup_key"] == received[1]["dedup_key"] || received[1]["dedup_key"] != received[2]["dedup_key"] || received[2]["event_action"] != "resolve" {
		t.Fatal("review closure changed destination correlation or resolved the health alert")
	}
	if !strings.Contains(received[1]["payload"].(map[string]any)["summary"].(string), "outcome unknown") {
		t.Fatal("unknown effect was not described")
	}
}

func TestBlueOpsgenieClosureWaitsForProviderProcessing(t *testing.T) {
	const requestID = "11111111-1111-4111-8111-111111111111"
	var alias string
	posts, reads := 0, 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			reads++
			_, _ = io.WriteString(w, `{"data":{"success":true,"isSuccess":true}}`)
			return
		}
		posts++
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if posts == 1 {
			alias = payload["alias"]
		} else if r.URL.Path != "/v2/alerts/"+alias+"/close" || r.URL.Query().Get("identifierType") != "alias" {
			t.Error("closure lost original alias")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"requestId":"`+requestID+`"}`)
	}))
	defer receiver.Close()
	for _, phase := range []string{"open", "close"} {
		job := &CodeOpsgenieJob{URL: receiver.URL + "/v2/alerts", APIKey: "fixture", Color: "blue"}
		d := NewDispatch(job, ecs.Entity{}, "code", "blue", 0, 0)
		d.MonitorID, d.FileIncarnation, d.EndpointID, d.NotificationCorrelation = "service", "incarnation", "blue-destination", "review-condition"
		d.ProviderCorrelationVersion = 1
		d.ReviewNotification = &ReviewNotification{AlertID: "review-condition", ActionID: "uncertain-action", Phase: phase, Health: "healthy", Reason: "health_recovered"}
		if result := d.Execute(); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if posts != 2 || reads != 2 || alias == "" {
		t.Fatal("provider acceptance was confused with completed processing", posts, reads)
	}
}

func TestEmailHeadersRetainCommittedDeliveryIdentity(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	messages := make(chan string, 3)
	go func() {
		defer close(messages)
		for range 3 {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				reader := bufio.NewReader(conn)
				_, _ = io.WriteString(conn, "220 fixture\r\n")
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if line != "DATA\r\n" {
						_, _ = io.WriteString(conn, "250 ready\r\n")
						continue
					}
					_, _ = io.WriteString(conn, "354 continue\r\n")
					var body strings.Builder
					for {
						line, err = reader.ReadString('\n')
						if err != nil {
							return
						}
						if line == ".\r\n" {
							break
						}
						body.WriteString(line)
					}
					messages <- body.String()
					_, _ = io.WriteString(conn, "250 accepted\r\n")
					return
				}
			}()
		}
	}()
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.FixedZone("offset", 3*3600))
	var ids []string
	for _, delivery := range []string{"opening", "opening", "closure"} {
		if err = sendEmail(t.Context(), manifest.CodeNotificationEmail{Server: ln.Addr().String(), From: "cpra@example.org", To: "ops@example.org", AllowInsecure: true}, "review notice", notificationIdentity{DeliveryID: delivery, At: at}); err != nil {
			t.Fatal(err)
		}
		raw, ok := <-messages
		if !ok {
			t.Fatal("SMTP receiver did not receive the message")
		}
		message, err := mail.ReadMessage(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		date, err := message.Header.Date()
		if err != nil || !date.Equal(at) || !strings.HasSuffix(message.Header.Get("Date"), "+0000") {
			t.Fatal("email lost committed UTC time", err)
		}
		ids = append(ids, message.Header.Get("Message-ID"))
	}
	if ids[0] == "" || ids[0] != ids[1] || ids[1] == ids[2] {
		t.Fatal("email attempts changed identity or closure reused opening identity")
	}
}
