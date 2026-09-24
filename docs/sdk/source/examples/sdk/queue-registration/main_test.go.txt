package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/examples/sdk/internal/cprafixture"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

func testRegistration() registration {
	return registration{EventID: "event-1", ServiceID: "payments", Name: "Payments API", HealthURL: "https://payments.example.test/health"}
}

func registrationJSON(t *testing.T, e registration) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func testClient(t *testing.T, handler http.Handler) *cpra.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: cprafixture.Token, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func TestLoadQueueValidatesWholeInputAndBoundsActualBytes(t *testing.T) {
	valid := registrationJSON(t, testRegistration())
	for name, input := range map[string]string{
		"malformed-final":    valid + `{"eventID":`,
		"unknown-field":      strings.TrimSuffix(valid, "\n")[:len(valid)-2] + `,"token":"secret"}`,
		"duplicate-field":    strings.Replace(valid, `"serviceID":"payments"`, `"serviceID":"other","serviceID":"payments"`, 1),
		"trailing-json":      strings.TrimSpace(valid) + ` {}`,
		"too-many":           strings.Repeat(valid, 1001),
		"long-line":          strings.Repeat(" ", 64<<10) + "\n",
		"CRLF-byte-overflow": strings.Repeat(" \r\n", (4<<20)/3+10),
	} {
		t.Run(name, func(t *testing.T) {
			q, err := loadQueue(strings.NewReader(input))
			if err == nil || q != nil {
				t.Fatalf("invalid input admitted: messages=%v err=%v", q, err)
			}
		})
	}
	q, err := loadQueue(strings.NewReader(valid + valid))
	if err != nil || len(q.messages) != 2 {
		t.Fatalf("identical replay rejected: %v", err)
	}
}

func TestChangedPayloadCannotReuseEventIdentity(t *testing.T) {
	e := testRegistration()
	first := registrationJSON(t, e)
	e.ServiceID = "another-service"
	q, err := loadQueue(strings.NewReader(first + registrationJSON(t, e)))
	if err == nil || q != nil {
		t.Fatal("same EventID with different payload was accepted")
	}
}

func TestRegistrationRejectsCredentialsAndUnsafeSchemesWithoutEchoingThem(t *testing.T) {
	for _, url := range []string{"file:///etc/passwd", "https://user:secret@example.test/health", "https://example.test/?token=secret", "https://example.test/#secret", "http://"} {
		e := testRegistration()
		e.HealthURL = url
		if err := validateRegistration(e); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("URL error=%v", err)
		}
	}
}

func TestQueueKeepsPendingUntilMatchingAcknowledgment(t *testing.T) {
	e := testRegistration()
	q := &mockQueue{messages: []registration{e}}
	got, err := q.Next(context.Background())
	if err != nil || got != e {
		t.Fatalf("next=%v err=%v", got, err)
	}
	if err := q.Ack("wrong-event"); err == nil {
		t.Fatal("mismatched acknowledgment succeeded")
	}
	again, err := q.Next(context.Background())
	if err != nil || again != e || q.acknowledged != 0 {
		t.Fatal("pending delivery advanced")
	}
	if err := q.Ack(e.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("next err=%v", err)
	}
}

func TestDuplicateRegistrationDoesNotReenableOrOverwriteMonitor(t *testing.T) {
	h := cprafixture.NewHandler()
	writes := 0
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
		}
		h.ServeHTTP(w, r)
	}))
	e := testRegistration()
	state, err := reconcile(context.Background(), c, e)
	if err != nil || state != "created" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	got, err := c.Monitors.Get(context.Background(), "service-payments")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Monitors.Disable(context.Background(), got.Data.Metadata.ID, got.ResourceVersion); err != nil {
		t.Fatal(err)
	}
	before := writes
	q := &mockQueue{messages: []registration{e, e}}
	if err := consume(context.Background(), c, q, io.Discard); err != nil {
		t.Fatal(err)
	}
	if q.acknowledged != 2 || writes != before || len(h.Snapshot()) != 1 {
		t.Fatal("duplicate caused a write or was not acknowledged")
	}
	got, err = c.Monitors.Get(context.Background(), "service-payments")
	if err != nil || got.Data.Spec.Enabled == nil || *got.Data.Spec.Enabled {
		t.Fatal("operator disable state changed")
	}
}

func TestLostCreateResponseIsReadBackBeforeAcknowledgment(t *testing.T) {
	h := cprafixture.NewHandler()
	creates, reads := 0, 0
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reads++
		}
		if r.Method == http.MethodPost {
			creates++
			committed := httptest.NewRecorder()
			h.ServeHTTP(committed, r)
			if committed.Code != 201 {
				t.Errorf("fixture create=%d", committed.Code)
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		h.ServeHTTP(w, r)
	}))
	q := &mockQueue{messages: []registration{testRegistration()}}
	if err := consume(context.Background(), c, q, io.Discard); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || reads != 2 || q.acknowledged != 1 || len(h.Snapshot()) != 1 {
		t.Fatalf("creates=%d reads=%d ack=%d", creates, reads, q.acknowledged)
	}
}

func TestUncertainCreateWithoutAReadbackMatchStaysPending(t *testing.T) {
	posts, gets := 0, 0
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			posts++
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"status":503}`)
			return
		}
		gets++
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"status":404}`)
	}))
	q := &mockQueue{messages: []registration{testRegistration()}}
	if err := consume(context.Background(), c, q, io.Discard); err == nil {
		t.Fatal("uncertain create without a matching resource was acknowledged")
	}
	if posts != 1 || gets != 2 || q.acknowledged != 0 || q.pending == nil {
		t.Fatalf("posts=%d gets=%d ack=%d pending=%v", posts, gets, q.acknowledged, q.pending)
	}
}

func TestFailedOrForeignRegistrationsStayPending(t *testing.T) {
	for _, variant := range []string{"foreign-owner", "changed-check", "wrong-returned-ID", "server-failure"} {
		t.Run(variant, func(t *testing.T) {
			e := testRegistration()
			monitor, err := desiredMonitor(e)
			if err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "foreign-owner":
				(*monitor.Metadata.Labels)["examples.cpra.io/owner"] = "other-controller"
			case "changed-check":
				monitor.Spec.Check.Interval = "120s"
			case "wrong-returned-ID":
				monitor.Metadata.ID = "different-resource"
			}
			writes := 0
			c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					writes++
					t.Error("unexpected mutation")
				}
				if variant == "server-failure" {
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"status":503}`)
					return
				}
				_ = json.NewEncoder(w).Encode(monitor)
			}))
			q := &mockQueue{messages: []registration{e}}
			if err := consume(context.Background(), c, q, io.Discard); err == nil {
				t.Fatal("unsafe delivery acknowledged")
			}
			if q.acknowledged != 0 || q.pending == nil || writes != 0 {
				t.Fatal("failed delivery advanced or mutated")
			}
		})
	}
}

func TestDemoShowsRepeatedDeliveryWithOneMonitor(t *testing.T) {
	var output bytes.Buffer
	if err := demo(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "messages_acknowledged=2 monitors=1") || !strings.Contains(output.String(), "already-present") {
		t.Fatalf("demo output %s", output.String())
	}
}
