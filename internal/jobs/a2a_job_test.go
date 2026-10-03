package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

func testAgentDispatch(t *testing.T, server *httptest.Server, kind string) *Dispatch {
	t.Helper()
	j, err := NewA2AJob(A2AConfig{URL: server.URL, AllowInsecureHTTP: true, RequestTimeout: time.Second, TaskTimeout: time.Second, PollInterval: time.Second}, kind)
	if err != nil {
		t.Fatal(err)
	}
	// Keep network tests short; constructor bounds are exercised separately.
	j.pollInterval = time.Millisecond
	d := NewDispatch(j, ecs.Entity{}, kind, "red", 0, 0)
	d.MonitorID, d.Revision, d.ActionID, d.IncidentID, d.CatalogUID, d.Attempt = "monitor", "revision", "action", "incident", "incarnation", 1
	return d
}

func TestA2AJobRequiresCommittedInvocation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, mode := range []string{"direct", "no start", "no receipt writer", "no finish", "rejected start", "wrong identity", "wrong kind", "wrong driver"} {
		t.Run(mode, func(t *testing.T) {
			d := testAgentDispatch(t, server, "intervention")
			finished := 0
			d.Authorize = func(context.Context) (ActionExecution, error) {
				e := ActionExecution{Finish: func() error { finished++; return nil }, RecordAgentReceipt: func(context.Context, A2AReceipt) error { return nil }}
				if mode == "no finish" {
					e.Finish = nil
				}
				if mode == "no receipt writer" {
					e.RecordAgentReceipt = nil
				}
				if mode == "rejected start" {
					return e, errors.New("start unconfirmed")
				}
				return e, nil
			}
			switch mode {
			case "direct":
				if d.Job.Execute().Err == nil {
					t.Fatal("direct invocation accepted")
				}
				return
			case "no start":
				d.Authorize = nil
			case "wrong identity":
				d.ActionID = ""
			case "wrong kind":
				d.Kind = "pulse"
			case "wrong driver":
				d.Driver = "webhook"
			}
			r := d.Execute()
			if r.Err == nil || !r.ExecutionStart.IsZero() {
				t.Fatal("invalid invocation executed", r)
			}
			if (mode == "no receipt writer" || mode == "rejected start") && finished != 1 {
				t.Fatal("reserved claim not finalized")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("uncommitted action contacted provider")
	}
}

func TestA2AJobOutcomesKeepAcceptanceSeparateFromCompletion(t *testing.T) {
	for _, kind := range []string{"intervention", "code"} {
		for _, state := range []string{"message", "TASK_STATE_COMPLETED", "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED", "TASK_STATE_FUTURE"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				var sends, polls atomic.Int32
				var recorded []A2AReceipt
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						sends.Add(1)
						if state == "message" {
							writeA2AResponse(w, `{"message":{"messageId":"reply","role":"ROLE_AGENT","parts":[{"text":"accepted"}]}}`)
							return
						}
						writeA2AResponse(w, `{"task":{"id":"task","contextId":"context","status":{"state":"`+state+`"}}}`)
						return
					}
					polls.Add(1)
					writeA2AResponse(w, `{"id":"task","contextId":"context","status":{"state":"TASK_STATE_COMPLETED"}}`)
				}))
				defer server.Close()
				d := testAgentDispatch(t, server, kind)
				finished := 0
				d.Authorize = func(context.Context) (ActionExecution, error) {
					return ActionExecution{Finish: func() error { finished++; return nil }, RecordAgentReceipt: func(_ context.Context, r A2AReceipt) error { recorded = append(recorded, r); return nil }}, nil
				}
				r := d.Execute()
				wantSuccess := state == "TASK_STATE_COMPLETED" || state == "TASK_STATE_WORKING" || state == "TASK_STATE_SUBMITTED" || kind == "code" && state == "message"
				if wantSuccess != (r.Err == nil) || r.FinalizationErr != nil || sends.Load() != 1 || finished != 1 || len(recorded) < 1 {
					t.Fatal("wrong action outcome", r, sends.Load(), finished, recorded)
				}
				wantPoll := kind == "intervention" && (state == "TASK_STATE_SUBMITTED" || state == "TASK_STATE_WORKING")
				if wantPoll != (polls.Load() == 1) {
					t.Fatal("notification polled or intervention failed to follow task", polls.Load())
				}
				var rejection *DeliveryError
				if errors.As(r.Err, &rejection) {
					t.Fatal("uncertain task became retryable rejection")
				}
			})
		}
	}
}

func TestA2AJobCommitsBeforePollingAndDoesNotReplayFailedCommits(t *testing.T) {
	for _, failAt := range []int{0, 1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			var sends, polls, committed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					sends.Add(1)
					var request struct {
						Message struct {
							MessageID string    `json:"messageId"`
							Parts     []a2aPart `json:"parts"`
						} `json:"message"`
					}
					if json.NewDecoder(r.Body).Decode(&request) != nil || request.Message.MessageID != "action" || len(request.Message.Parts) != 2 {
						t.Error("lost committed identity or bounded envelope")
					}
					var action agentAction
					if len(request.Message.Parts) == 2 && (json.Unmarshal(request.Message.Parts[1].Data, &action) != nil || action.MonitorID != "monitor" || action.MonitorUID != "incarnation" || action.IncidentID != "incident" || action.Role != "intervention") {
						t.Error("incorrect incident envelope")
					}
					writeA2AResponse(w, `{"task":{"id":"task","status":{"state":"TASK_STATE_WORKING"}}}`)
					return
				}
				if committed.Load() < 1 {
					t.Error("polled before committed task reference")
				}
				polls.Add(1)
				writeA2AResponse(w, `{"id":"task","status":{"state":"TASK_STATE_COMPLETED"}}`)
			}))
			defer server.Close()
			d := testAgentDispatch(t, server, "intervention")
			storageErr := errors.New("private database path")
			finishErr := errors.New("completion failure")
			observations := 0
			d.Authorize = func(context.Context) (ActionExecution, error) {
				return ActionExecution{Finish: func() error {
					if failAt != 0 {
						return finishErr
					}
					return nil
				}, RecordAgentReceipt: func(context.Context, A2AReceipt) error {
					observations++
					if observations == failAt {
						return storageErr
					}
					committed.Add(1)
					return nil
				}}, nil
			}
			r := d.Execute()
			if sends.Load() != 1 || (failAt == 0) != (r.Err == nil) || failAt == 1 && polls.Load() != 0 {
				t.Fatal("commit loss replayed send, polled or succeeded", r)
			}
			if failAt != 0 && (!errors.Is(r.FinalizationErr, finishErr) || strings.Contains(r.Err.Error(), "private")) {
				t.Fatal("lost completion error or exposed storage details", r)
			}
		})
	}
}

func TestA2AJobCancellationDoesNotCancelOrRepeatRemoteTask(t *testing.T) {
	var sends, polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sends.Add(1)
			writeA2AResponse(w, `{"task":{"id":"task","status":{"state":"TASK_STATE_WORKING"}}}`)
			return
		}
		polls.Add(1)
	}))
	defer server.Close()
	d := testAgentDispatch(t, server, "intervention")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.SetContext(ctx)
	d.Authorize = func(context.Context) (ActionExecution, error) {
		return ActionExecution{
			Finish: func() error { return nil }, RecordAgentReceipt: func(context.Context, A2AReceipt) error { cancel(); return nil },
		}, nil
	}
	r := d.Execute()
	if !errors.Is(r.Err, context.Canceled) || sends.Load() != 1 || polls.Load() != 0 {
		t.Fatal("cancelled action performed more network work", r)
	}
}

func TestA2AJobConfigurationBounds(t *testing.T) {
	for name, config := range map[string]A2AConfig{
		"negative timeout": {TaskTimeout: -time.Second}, "long timeout": {TaskTimeout: 6 * time.Minute},
		"short polling": {PollInterval: time.Millisecond}, "long polling": {PollInterval: time.Minute},
		"poll after deadline": {TaskTimeout: time.Second, PollInterval: 2 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			config.URL = "https://agent.example"
			if _, err := NewA2AJob(config, "intervention"); err == nil {
				t.Fatal("unbounded configuration accepted")
			}
		})
	}
	if _, err := NewA2AJob(A2AConfig{URL: "https://agent.example"}, "pulse"); err == nil {
		t.Fatal("A2A check accepted")
	}
}

func TestA2AJobDeadlineBoundsUnfinishedTask(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sends.Add(1)
			writeA2AResponse(w, `{"task":{"id":"task","status":{"state":"TASK_STATE_WORKING"}}}`)
			return
		}
		writeA2AResponse(w, `{"id":"task","status":{"state":"TASK_STATE_WORKING"}}`)
	}))
	defer server.Close()
	d := testAgentDispatch(t, server, "intervention")
	d.Job.(*A2AJob).pollInterval = time.Second
	observations := 0
	d.Authorize = func(context.Context) (ActionExecution, error) {
		return ActionExecution{
			Finish: func() error { return nil }, RecordAgentReceipt: func(context.Context, A2AReceipt) error { observations++; return nil },
		}, nil
	}
	r := d.Execute()
	if !errors.Is(r.Err, context.DeadlineExceeded) || sends.Load() != 1 || observations != 1 {
		t.Fatal("unfinished task escaped its deadline or repeated observations", r, observations)
	}
}

func TestA2AJobRecordsChangesWithoutRetainingEveryPoll(t *testing.T) {
	var sends, polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sends.Add(1)
			writeA2AResponse(w, `{"task":{"id":"task","status":{"state":"TASK_STATE_WORKING"}}}`)
			return
		}
		state := "TASK_STATE_WORKING"
		if polls.Add(1) >= 3 {
			state = "TASK_STATE_COMPLETED"
		}
		writeA2AResponse(w, `{"id":"task","status":{"state":"`+state+`"}}`)
	}))
	defer server.Close()
	d := testAgentDispatch(t, server, "intervention")
	var recorded []A2AReceipt
	d.Authorize = func(context.Context) (ActionExecution, error) {
		return ActionExecution{
			Finish: func() error { return nil }, RecordAgentReceipt: func(_ context.Context, r A2AReceipt) error { recorded = append(recorded, r); return nil },
		}, nil
	}
	r := d.Execute()
	if r.Err != nil || sends.Load() != 1 || polls.Load() != 3 || len(recorded) != 2 || recorded[1].State != "TASK_STATE_COMPLETED" {
		t.Fatal("polling repeated sends or retained unchanged observations", r, recorded)
	}
}

func TestA2AJobReceiptPanicStillFinalizesLocalInvocation(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		writeA2AResponse(w, `{"task":{"id":"task","status":{"state":"TASK_STATE_COMPLETED"}}}`)
	}))
	defer server.Close()
	d := testAgentDispatch(t, server, "intervention")
	finished := 0
	d.Authorize = func(context.Context) (ActionExecution, error) {
		return ActionExecution{
			Finish: func() error { finished++; return nil }, RecordAgentReceipt: func(context.Context, A2AReceipt) error { panic("private storage diagnostic") },
		}, nil
	}
	r := d.Execute()
	if r.Err == nil || strings.Contains(r.Err.Error(), "private") || finished != 1 || sends.Load() != 1 || r.ExecutionStart.IsZero() {
		t.Fatal("panicking observation repeated or leaked an action", r)
	}
}
