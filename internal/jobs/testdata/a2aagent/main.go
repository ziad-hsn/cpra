// This local interoperability fixture uses the upstream A2A server. It does
// not replace CPRa's HTTP transport or make calls to an external provider.
package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"flag"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

type agent struct {
	mu        sync.Mutex
	scenario  string
	sends     int
	cancels   int
	messageID string
}

func (a *agent) Execute(ctx context.Context, execution *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		a.mu.Lock()
		a.sends++
		a.messageID = execution.Message.ID
		a.mu.Unlock()
		if a.scenario == "message" {
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("Incident received")), nil)
			return
		}
		if !yield(a2a.NewSubmittedTask(execution, execution.Message), nil) {
			return
		}
		if !yield(a2a.NewStatusUpdateEvent(execution, a2a.TaskStateWorking, nil), nil) {
			return
		}
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		state := map[string]a2a.TaskState{
			"completed":      a2a.TaskStateCompleted,
			"failed":         a2a.TaskStateFailed,
			"rejected":       a2a.TaskStateRejected,
			"canceled":       a2a.TaskStateCanceled,
			"input-required": a2a.TaskStateInputRequired,
			"auth-required":  a2a.TaskStateAuthRequired,
		}[a.scenario]
		yield(a2a.NewStatusUpdateEvent(execution, state, nil), nil)
	}
}

func (a *agent) Cancel(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(func(a2a.Event, error) bool) {
		a.mu.Lock()
		a.cancels++
		a.mu.Unlock()
	}
}

func main() {
	scenario := flag.String("scenario", "completed", "fixed local response scenario")
	flag.Parse()
	valid := map[string]bool{"message": true, "completed": true, "failed": true, "rejected": true, "canceled": true, "input-required": true, "auth-required": true}
	if !valid[*scenario] {
		os.Exit(2)
	}
	agent := &agent{scenario: *scenario}
	protocol := a2asrv.NewRESTHandler(a2asrv.NewHandler(agent))
	mux := http.NewServeMux()
	mux.Handle("/agent/", http.StripPrefix("/agent", protocol))
	mux.HandleFunc("GET /fixture/effects", func(w http.ResponseWriter, r *http.Request) {
		agent.mu.Lock()
		defer agent.mu.Unlock()
		_ = json.NewEncoder(w).Encode(struct {
			Sends     int    `json:"sends"`
			Cancels   int    `json:"cancels"`
			MessageID string `json:"messageId"`
		}{agent.sends, agent.cancels, agent.messageID})
	})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-fixture-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if json.NewEncoder(os.Stdout).Encode(struct {
		URL         string `json:"url"`
		Certificate string `json:"certificate"`
	}{server.URL, string(cert)}) != nil {
		os.Exit(1)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}
