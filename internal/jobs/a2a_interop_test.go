//go:build a2ainterop

package jobs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// The a2ainterop test tag requires the separately built upstream SDK fixture.
// A missing fixture fails this campaign instead of silently skipping it.
func TestA2AUpstreamServer(t *testing.T) {
	program := os.Getenv("CPRA_A2A_AGENT")
	if program == "" {
		t.Fatal("CPRA_A2A_AGENT must point to the compiled testdata/a2aagent fixture")
	}
	for _, path := range []string{"transport", "intervention", "code"} {
		for scenario, expected := range map[string]string{
			"message":        "",
			"completed":      "TASK_STATE_COMPLETED",
			"failed":         "TASK_STATE_FAILED",
			"rejected":       "TASK_STATE_REJECTED",
			"canceled":       "TASK_STATE_CANCELED",
			"input-required": "TASK_STATE_INPUT_REQUIRED",
			"auth-required":  "TASK_STATE_AUTH_REQUIRED",
		} {
			t.Run(path+"/"+scenario, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				t.Cleanup(cancel)
				command := exec.CommandContext(ctx, program, "-scenario", scenario)
				var logs bytes.Buffer
				command.Stderr = &logs
				input, err := command.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				output, err := command.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = input.Close()
					if err := command.Wait(); err != nil {
						t.Errorf("upstream fixture failed: %v\n%s", err, logs.String())
					}
				})
				var endpoint struct {
					URL         string `json:"url"`
					Certificate string `json:"certificate"`
				}
				if err := json.NewDecoder(bufio.NewReader(io.LimitReader(output, 8192))).Decode(&endpoint); err != nil {
					t.Fatal(err)
				}
				roots := x509.NewCertPool()
				if !roots.AppendCertsFromPEM([]byte(endpoint.Certificate)) {
					t.Fatal("fixture certificate is invalid")
				}
				client, err := newA2AClient(a2aConnection{URL: endpoint.URL + "/agent", Token: "local-fixture-token", Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				transport := client.http.Transport.(effectTransport).RoundTripper.(*http.Transport).Clone()
				transport.TLSClientConfig.RootCAs = roots
				client.http.Transport = effectTransport{transport}
				t.Cleanup(transport.CloseIdleConnections)
				identity := "committed-action-" + scenario
				var receipt A2AReceipt
				if path == "transport" {
					receipt, err = client.Send(ctx, identity, []a2aPart{{Text: "Investigate only"}, {Data: json.RawMessage(`{"incidentId":"incident-123"}`)}})
					if err != nil {
						t.Fatal(err)
					}
					if scenario == "message" {
						if receipt.MessageID == "" || receipt.TaskID != "" || receipt.State != "" {
							t.Fatalf("message treated as a completed task: %+v", receipt)
						}
					} else {
						original := receipt
						for receipt.State != expected {
							if receipt.State != "TASK_STATE_SUBMITTED" && receipt.State != "TASK_STATE_WORKING" {
								t.Fatalf("unexpected state: %+v", receipt)
							}
							select {
							case <-ctx.Done():
								t.Fatal(ctx.Err())
							case <-time.After(20 * time.Millisecond):
							}
							receipt, err = client.Task(ctx, original)
							if err != nil {
								t.Fatal(err)
							}
						}
						if receipt.TaskID != original.TaskID || receipt.ContextID != original.ContextID {
							t.Fatal("polling changed task identity")
						}
					}

				} else {
					job, err := NewA2AJob(A2AConfig{URL: endpoint.URL + "/agent", Token: "local-fixture-token", TaskTimeout: 5 * time.Second, PollInterval: time.Second}, path)
					if err != nil {
						t.Fatal(err)
					}
					job.client, job.pollInterval = client, 20*time.Millisecond
					dispatch := NewDispatch(job, ecs.Entity{}, path, "red", 0, 0)
					dispatch.MonitorID, dispatch.Revision, dispatch.ActionID, dispatch.IncidentID, dispatch.Attempt = "reference-monitor", "revision", identity, "incident", 1
					dispatch.SetContext(ctx)
					finished := 0
					dispatch.Authorize = func(context.Context) (ActionExecution, error) {
						return ActionExecution{Finish: func() error { finished++; return nil }, RecordAgentReceipt: func(_ context.Context, r A2AReceipt) error { receipt = r; return nil }}, nil
					}
					result := dispatch.Execute()
					wantSuccess := path == "intervention" && scenario == "completed" || path == "code" && (receipt.MessageID != "" || receipt.State == "TASK_STATE_SUBMITTED" || receipt.State == "TASK_STATE_WORKING" || receipt.State == "TASK_STATE_COMPLETED")
					if wantSuccess != (result.Err == nil) || result.FinalizationErr != nil || finished != 1 || receipt == (A2AReceipt{}) {
						t.Fatal("incorrect upstream execution outcome", result, receipt, finished)
					}
				}
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.URL+"/fixture/effects", nil)
				request.Header.Set("Authorization", "Bearer local-fixture-token")
				response, err := client.http.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = response.Body.Close() }()
				var observed struct {
					Sends, Cancels int
					MessageID      string
				}
				if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&observed); err != nil {
					t.Fatal(err)
				}
				if observed.Sends != 1 || observed.Cancels != 0 || observed.MessageID != identity {
					t.Fatalf("unexpected independent effects: %+v", observed)
				}
				if path != "code" && strings.Contains(receipt.State, "WORKING") {
					t.Fatal("unfinished task reported final")
				}
			})
		}
	}
}
