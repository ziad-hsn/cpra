//go:build externaljobs

package cpra

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

var workerProtocolOperations = []string{"WorkerPoll", "WorkerStart", "WorkerHeartbeat", "WorkerResult", "WorkerLateEvidence"}

func workerProtocolRequest(operation string) any {
	switch operation {
	case "WorkerPoll":
		return api.PollRequest{WorkerID: "worker", ServerID: "server", ClientSessionID: "client-session", SessionID: "session", PollSequence: 1, Capacity: 2, Limit: 2,
			Capabilities: []api.WorkerCapability{{JobTypeID: "kind", Version: "v1", Kind: "check"}}}
	case "WorkerStart":
		return api.StartRequest{ServerID: "server", SessionID: "session", ExecutionID: "execution", ExecutionRevision: "revision", LeaseID: "lease", Mode: api.StartModeBegin}
	case "WorkerHeartbeat":
		return api.HeartbeatRequest{WorkerID: "worker", ServerID: "server", SessionID: "session", ExecutionID: "execution", GrantID: "grant"}
	case "WorkerResult":
		return api.Outcome{ServerID: "server", WorkerUID: "worker-uid", ExecutionID: "execution", GrantID: "grant", Kind: "check", Status: "success", Evidence: []string{}, Data: json.RawMessage(`{}`)}
	case "WorkerLateEvidence":
		return api.LateEvidenceRequest{ServerID: "server", WorkerUID: "worker-uid", ExecutionID: "execution", EvidenceID: "evidence", OriginalReceiptID: "original-receipt", Evidence: []string{}, Data: json.RawMessage(`{}`)}
	default:
		panic("unknown fixture operation")
	}
}

func workerProtocolReply(operation string) any {
	deadline := time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC)
	switch operation {
	case "WorkerPoll":
		return api.Assignments{ServerID: "server", ClientSessionID: "client-session", SessionID: "session", PollSequence: 1, WorkerUID: "worker-uid", SessionExpiresAt: deadline,
			Items: []api.Assignment{{ServerID: "server", SessionID: "session", WorkerUID: "worker-uid", ExecutionID: "execution", ExecutionRevision: "revision", LeaseID: "lease", MonitorID: "monitor", IncarnationUID: "monitor-uid", JobTypeID: "kind", JobTypeUID: "kind-uid", JobTypeVersion: "v1", Kind: "check", Parameters: json.RawMessage(`{}`), Deadline: deadline}}}
	case "WorkerStart":
		return api.StartResponse{ServerID: "server", SessionID: "session", WorkerUID: "worker-uid", ExecutionID: "execution", ExecutionRevision: "revision", LeaseID: "lease", GrantID: "grant", Deadline: deadline, Disposition: api.StartDispositionGranted}
	case "WorkerHeartbeat":
		return api.HeartbeatResponse{ServerID: "server", SessionID: "session", WorkerUID: "worker-uid", ExecutionID: "execution", GrantID: "grant", Accepted: true}
	case "WorkerResult", "WorkerLateEvidence":
		return api.Receipt{ServerID: "server", WorkerUID: "worker-uid", ExecutionID: "execution", ReceiptID: "receipt", Disposition: api.ReceiptDispositionAccepted}
	default:
		panic("unknown fixture operation")
	}
}

func workerProtocolCall(ctx context.Context, c *WorkerClient, request any) (any, error) {
	switch r := request.(type) {
	case api.PollRequest:
		return c.Poll(ctx, r)
	case api.StartRequest:
		return c.Start(ctx, r)
	case api.HeartbeatRequest:
		return c.Heartbeat(ctx, r)
	case api.Outcome:
		return c.Result(ctx, r)
	case api.LateEvidenceRequest:
		return c.LateEvidence(ctx, r)
	default:
		panic("unknown fixture request")
	}
}

func TestWorkerProtocolBoundRequestsBeforeHTTP(t *testing.T) {
	poll := func(edit func(*api.PollRequest)) any {
		r := workerProtocolRequest("WorkerPoll").(api.PollRequest)
		edit(&r)
		return r
	}
	start := func(edit func(*api.StartRequest)) any {
		r := workerProtocolRequest("WorkerStart").(api.StartRequest)
		edit(&r)
		return r
	}
	outcome := func(edit func(*api.Outcome)) any {
		r := workerProtocolRequest("WorkerResult").(api.Outcome)
		edit(&r)
		return r
	}
	evidence := workerProtocolRequest("WorkerLateEvidence").(api.LateEvidenceRequest)
	evidence.Evidence = nil
	requests := []any{
		poll(func(r *api.PollRequest) { r.ServerID = "" }),
		poll(func(r *api.PollRequest) { r.ClientSessionID = "\xff" }),
		poll(func(r *api.PollRequest) { r.SessionID = ""; r.PollSequence = 2 }),
		poll(func(r *api.PollRequest) { r.PollSequence = 0 }),
		poll(func(r *api.PollRequest) { r.PollSequence = -1 }),
		poll(func(r *api.PollRequest) { r.Capacity = 101 }),
		poll(func(r *api.PollRequest) { r.Limit = 101 }),
		poll(func(r *api.PollRequest) { r.WaitSeconds = 26 }),
		poll(func(r *api.PollRequest) { r.Capabilities = nil }),
		poll(func(r *api.PollRequest) { r.Capabilities = append(r.Capabilities, r.Capabilities[0]) }),
		poll(func(r *api.PollRequest) { r.Capabilities = make([]api.WorkerCapability, 65) }),
		poll(func(r *api.PollRequest) { r.Capabilities[0].Kind = "execute" }),
		start(func(r *api.StartRequest) { r.Mode = "" }),
		start(func(r *api.StartRequest) { r.Mode = "retry" }),
		start(func(r *api.StartRequest) { r.LeaseID = "bad\nlease" }),
		start(func(r *api.StartRequest) { r.SessionID = strings.Repeat("s", 257) }),
		outcome(func(r *api.Outcome) { r.WorkerUID = "" }),
		outcome(func(r *api.Outcome) { r.Evidence = nil }),
		evidence,
		outcome(func(r *api.Outcome) { r.Status = "delivered" }),
		outcome(func(r *api.Outcome) { r.Diagnostic = strings.Repeat("x", 65537) }),
		outcome(func(r *api.Outcome) { r.Evidence = make([]string, 65) }),
		outcome(func(r *api.Outcome) { r.Data = json.RawMessage(strings.Repeat(" ", workerOutcomeBytes+1)) }),
		outcome(func(r *api.Outcome) { r.Data = json.RawMessage(`{"malformed"`) }),
		outcome(func(r *api.Outcome) { r.Diagnostic = strings.Repeat("<", 32<<10) }),
		api.HeartbeatRequest{}, api.LateEvidenceRequest{},
	}
	var calls atomic.Int32
	c := &WorkerClient{fixture(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) }, nil)}
	for n, request := range requests {
		if _, err := workerProtocolCall(context.Background(), c, request); !errors.Is(err, ErrInvalid) || errors.Is(err, ErrAmbiguous) {
			t.Fatalf("request %d: %v", n, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached HTTP")
	}
}

func TestWorkerProtocolContextAndNoMutationRetries(t *testing.T) {
	for _, operation := range workerProtocolOperations {
		t.Run(operation, func(t *testing.T) {
			var calls atomic.Int32
			c := &WorkerClient{fixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(503) }, func(c *Config) { c.ReadAttempts = 3 })}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := workerProtocolCall(ctx, c, workerProtocolRequest(operation)); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if _, err := workerProtocolCall(nil, c, workerProtocolRequest(operation)); !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
			if calls.Load() != 0 {
				t.Fatal("canceled request sent")
			}
			_, err := workerProtocolCall(context.Background(), c, workerProtocolRequest(operation))
			if err == nil || calls.Load() != 1 || (operation != "WorkerPoll" && !errors.Is(err, ErrAmbiguous)) {
				t.Fatal(calls.Load(), err)
			}
		})
	}
}

func TestWorkerProtocolRejectsMismatchedSuccess(t *testing.T) {
	for _, operation := range workerProtocolOperations {
		raw, _ := json.Marshal(workerProtocolReply(operation))
		var original map[string]any
		if err := json.Unmarshal(raw, &original); err != nil {
			t.Fatal(err)
		}
		fields := []string{"serverID", "workerUID"}
		switch operation {
		case "WorkerPoll":
			fields = append(fields, "clientSessionID", "sessionID", "pollSequence", "sessionExpiresAt")
		case "WorkerStart":
			fields = append(fields, "executionID", "executionRevision", "leaseID", "sessionID", "disposition", "grantID", "deadline")
		case "WorkerHeartbeat":
			fields = append(fields, "executionID", "grantID", "sessionID")
		default:
			fields = append(fields, "executionID", "receiptID", "disposition")
		}
		for _, field := range fields {
			t.Run(operation+"/"+field, func(t *testing.T) {
				response := make(map[string]any, len(original))
				for k, v := range original {
					response[k] = v
				}
				response[field] = ""
				if field == "pollSequence" {
					response[field] = 2
				}
				c := &WorkerClient{fixture(t, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(response) }, nil)}
				_, err := workerProtocolCall(context.Background(), c, workerProtocolRequest(operation))
				if err == nil || (operation != "WorkerPoll" && !errors.Is(err, ErrAmbiguous)) {
					t.Fatal(err)
				}
			})
		}
	}
	for _, operation := range []string{"WorkerResult", "WorkerLateEvidence"} {
		t.Run(operation+"/other-owner", func(t *testing.T) {
			r := workerProtocolReply(operation).(api.Receipt)
			r.WorkerUID = "other-worker"
			c := &WorkerClient{fixture(t, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(r) }, nil)}
			if _, err := workerProtocolCall(context.Background(), c, workerProtocolRequest(operation)); !errors.Is(err, ErrAmbiguous) {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerProtocolStartDispositions(t *testing.T) {
	for _, mode := range []api.StartMode{api.StartModeBegin, api.StartModeReconcile} {
		for _, disposition := range []api.StartDisposition{api.StartDispositionGranted, api.StartDispositionStarted, api.StartDispositionPending, api.StartDispositionUnknown, api.StartDispositionTerminal, api.StartDispositionRejected} {
			t.Run(string(mode)+"/"+string(disposition), func(t *testing.T) {
				r := workerProtocolReply("WorkerStart").(api.StartResponse)
				r.Disposition = disposition
				if disposition == api.StartDispositionPending || disposition == api.StartDispositionRejected || disposition == api.StartDispositionTerminal {
					r.GrantID = ""
					r.Deadline = time.Time{}
				}
				if disposition == api.StartDispositionTerminal || disposition == api.StartDispositionUnknown {
					r.ReceiptID = "receipt"
				}
				var calls atomic.Int32
				c := &WorkerClient{fixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _ = json.NewEncoder(w).Encode(r) }, nil)}
				request := workerProtocolRequest("WorkerStart").(api.StartRequest)
				request.Mode = mode
				got, err := c.Start(context.Background(), request)
				if mode == api.StartModeReconcile && disposition == api.StartDispositionGranted {
					if got != nil || !errors.Is(err, ErrAmbiguous) {
						t.Fatal("reconcile accepted executable grant", got, err)
					}
				} else if err != nil || got.Disposition != disposition {
					t.Fatal(got, err)
				}
				if calls.Load() != 1 {
					t.Fatal("start repeated", calls.Load())
				}
			})
		}
	}
	for _, disposition := range []api.StartDisposition{api.StartDispositionPending, api.StartDispositionRejected, api.StartDispositionTerminal} {
		t.Run(string(disposition)+"/stale-grant", func(t *testing.T) {
			r := workerProtocolReply("WorkerStart").(api.StartResponse)
			r.Disposition = disposition
			r.ReceiptID = "receipt"
			c := &WorkerClient{fixture(t, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(r) }, nil)}
			if _, err := c.Start(context.Background(), workerProtocolRequest("WorkerStart").(api.StartRequest)); !errors.Is(err, ErrAmbiguous) {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerProtocolPollOwnershipAndBounds(t *testing.T) {
	mutations := map[string]func(*api.Assignments){
		"other-worker":         func(r *api.Assignments) { r.Items[0].WorkerUID = "other" },
		"other-session":        func(r *api.Assignments) { r.Items[0].SessionID = "other" },
		"other-server":         func(r *api.Assignments) { r.Items[0].ServerID = "other" },
		"type-uid":             func(r *api.Assignments) { r.Items[0].JobTypeUID = "" },
		"unadvertised-version": func(r *api.Assignments) { r.Items[0].JobTypeVersion = "v2" },
		"unadvertised-kind":    func(r *api.Assignments) { r.Items[0].Kind = "recovery" },
		"duplicate-execution":  func(r *api.Assignments) { r.Items = append(r.Items, r.Items[0]); r.Items[1].LeaseID = "lease-2" },
		"duplicate-lease": func(r *api.Assignments) {
			r.Items = append(r.Items, r.Items[0])
			r.Items[1].ExecutionID = "execution-2"
		},
		"over-capacity": func(r *api.Assignments) { r.Items = append(r.Items, r.Items[0], r.Items[0]) },
		"parameter-bound": func(r *api.Assignments) {
			r.Items[0].Parameters = json.RawMessage(`"` + strings.Repeat("x", workerOutcomeBytes) + `"`)
		},
	}
	for name, edit := range mutations {
		t.Run(name, func(t *testing.T) {
			r := workerProtocolReply("WorkerPoll").(api.Assignments)
			edit(&r)
			c := &WorkerClient{fixture(t, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(r) }, nil)}
			if _, err := c.Poll(context.Background(), workerProtocolRequest("WorkerPoll").(api.PollRequest)); err == nil {
				t.Fatal("invalid assignment accepted")
			}
		})
	}
	for _, sequence := range []int64{1, math.MaxInt64} {
		t.Run("sequence", func(t *testing.T) {
			req := workerProtocolRequest("WorkerPoll").(api.PollRequest)
			req.PollSequence = sequence
			if sequence == 1 {
				req.SessionID = ""
			}
			r := workerProtocolReply("WorkerPoll").(api.Assignments)
			r.PollSequence = sequence
			c := &WorkerClient{fixture(t, func(w http.ResponseWriter, request *http.Request) {
				var actual api.PollRequest
				_ = json.NewDecoder(request.Body).Decode(&actual)
				if !reflect.DeepEqual(actual, req) {
					t.Error("request identity changed")
				}
				_ = json.NewEncoder(w).Encode(r)
			}, nil)}
			if _, err := c.Poll(context.Background(), req); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerProtocolSessionExpiryIsExplicit(t *testing.T) {
	for _, tc := range []struct {
		status  int
		code    string
		expired bool
	}{{409, "workerSessionExpired", true}, {410, "workerSessionExpired", false}, {409, "workerSessionConflict", false}, {503, "workerSessionExpired", false}} {
		t.Run(tc.code+http.StatusText(tc.status), func(t *testing.T) {
			var calls atomic.Int32
			c := &WorkerClient{fixture(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(api.Problem{Code: tc.code})
			}, nil)}
			_, err := c.Poll(context.Background(), workerProtocolRequest("WorkerPoll").(api.PollRequest))
			var problem *Error
			if errors.Is(err, ErrWorkerSessionExpired) != tc.expired || !errors.As(err, &problem) || problem.StatusCode != tc.status || calls.Load() != 1 {
				t.Fatal(err, calls.Load())
			}
		})
	}
}

func TestWorkerProtocolResponseLimitsAndRequiredBindings(t *testing.T) {
	for _, operation := range workerProtocolOperations {
		t.Run(operation, func(t *testing.T) {
			for _, mode := range []string{"missing-server", "null-worker", "body-limit", "status"} {
				t.Run(mode, func(t *testing.T) {
					c := &WorkerClient{fixture(t, func(w http.ResponseWriter, _ *http.Request) {
						if mode == "body-limit" {
							_, _ = io.WriteString(w, strings.Repeat(" ", 1025))
							return
						}
						if mode == "status" {
							w.WriteHeader(202)
						}
						raw, _ := json.Marshal(workerProtocolReply(operation))
						var response map[string]any
						_ = json.Unmarshal(raw, &response)
						if mode == "missing-server" {
							delete(response, "serverID")
						}
						if mode == "null-worker" {
							response["workerUID"] = nil
						}
						_ = json.NewEncoder(w).Encode(response)
					}, func(c *Config) {
						if mode == "body-limit" {
							c.MaxResponseBytes = 1024
						}
					})}
					_, err := workerProtocolCall(context.Background(), c, workerProtocolRequest(operation))
					if err == nil || operation != "WorkerPoll" && !errors.Is(err, ErrAmbiguous) {
						t.Fatal(err)
					}
					if mode == "body-limit" && !errors.Is(err, ErrResponseTooLarge) {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

type workerReplyTransport struct {
	reply  []byte
	cancel context.CancelFunc
}

func (r workerReplyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req,
		Body: &workerReplyBody{Reader: strings.NewReader(string(r.reply)), cancel: r.cancel}}, nil
}

type workerReplyBody struct {
	*strings.Reader
	cancel context.CancelFunc
}

func (r *workerReplyBody) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.cancel()
	}
	return n, err
}
func (*workerReplyBody) Close() error { return nil }

func TestWorkerProtocolCancelledReplyPublishesNoGrantOrReceipt(t *testing.T) {
	for _, operation := range workerProtocolOperations {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw, err := json.Marshal(workerProtocolReply(operation))
			if err != nil {
				t.Fatal(err)
			}
			c, err := NewWorkerClient(Config{BaseURL: "https://worker.example", AuthToken: "worker-token",
				HTTPClient: &http.Client{Transport: workerReplyTransport{raw, cancel}}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := workerProtocolCall(ctx, c, workerProtocolRequest(operation))
			if !errors.Is(err, context.Canceled) || (operation != "WorkerPoll" && !errors.Is(err, ErrAmbiguous)) {
				t.Fatalf("canceled completed reply: %v", err)
			}
			if result != nil && !reflect.ValueOf(result).IsNil() {
				t.Fatal("canceled reply returned executable authority or receipt")
			}
		})
	}
}
