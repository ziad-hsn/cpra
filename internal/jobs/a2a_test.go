package jobs

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestA2AClient(t *testing.T, server *httptest.Server) *a2aClient {
	t.Helper()
	client, err := newA2AClient(a2aConnection{URL: server.URL, Timeout: time.Second, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeA2AResponse(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", a2aMediaType)
	_, _ = io.WriteString(w, body)
}

const a2aWorkingTask = `{"id":"task-123","contextId":"context-456","status":{"state":"TASK_STATE_WORKING"}}`

func TestA2ASendAndTask(t *testing.T) {
	var sends, polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("A2A-Version") != "1.0" || r.Header.Get("Authorization") != "Bearer designated-agent-token" {
			t.Errorf("missing protocol or authentication header")
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /agent/message:send":
			sends.Add(1)
			if r.Header.Get("Idempotency-Key") != "" || r.Header.Get("Content-Type") != a2aMediaType {
				t.Errorf("message has unsupported replay or content-type headers")
			}
			var data struct {
				Tenant  string `json:"tenant"`
				Message struct {
					MessageID string    `json:"messageId"`
					Role      string    `json:"role"`
					Parts     []a2aPart `json:"parts"`
				} `json:"message"`
				Configuration struct {
					ReturnImmediately bool `json:"returnImmediately"`
					HistoryLength     *int `json:"historyLength"`
				} `json:"configuration"`
			}
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				t.Error(err)
			}
			if data.Tenant != "operations" || data.Message.MessageID != "committed-action-id" || data.Message.Role != "ROLE_USER" || len(data.Message.Parts) != 2 || !data.Configuration.ReturnImmediately || data.Configuration.HistoryLength == nil || *data.Configuration.HistoryLength != 0 {
				t.Errorf("unexpected message contract: %+v", data)
			}
			writeA2AResponse(w, `{"task":`+a2aWorkingTask+`}`)
		case "GET /agent/tasks/task-123":
			polls.Add(1)
			if r.URL.Query().Get("tenant") != "operations" || r.URL.Query().Get("historyLength") != "0" {
				t.Errorf("task query lost tenant or history bound")
			}
			writeA2AResponse(w, strings.ReplaceAll(a2aWorkingTask, "TASK_STATE_WORKING", "TASK_STATE_COMPLETED"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := newA2AClient(a2aConnection{URL: server.URL + "/agent/", Token: "designated-agent-token", Tenant: "operations", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Send(context.Background(), "committed-action-id", []a2aPart{{Text: "Investigate the confirmed failed intervention."}, {Data: json.RawMessage(`{"incidentId":"incident-123"}`)}})
	if err != nil || receipt.TaskID != "task-123" || receipt.ContextID != "context-456" || receipt.State != "TASK_STATE_WORKING" || receipt.MessageID != "" {
		t.Fatalf("send receipt = %+v, err = %v", receipt, err)
	}
	completed, err := client.Task(context.Background(), receipt)
	if err != nil || completed.State != "TASK_STATE_COMPLETED" || completed.TaskID != receipt.TaskID {
		t.Fatalf("task receipt = %+v, err = %v", completed, err)
	}
	if sends.Load() != 1 || polls.Load() != 1 {
		t.Fatalf("sends=%d, polls=%d", sends.Load(), polls.Load())
	}
}

func TestA2AConnectionValidation(t *testing.T) {
	for name, config := range map[string]a2aConnection{
		"http":             {URL: "http://agent.example"},
		"userinfo":         {URL: "https://token@agent.example"},
		"query":            {URL: "https://agent.example?token=secret"},
		"empty query":      {URL: "https://agent.example?"},
		"fragment":         {URL: "https://agent.example#secret"},
		"other scheme":     {URL: "file:///agent"},
		"token newline":    {URL: "https://agent.example", Token: "token\r\nX-Escaped: yes"},
		"token whitespace": {URL: "https://agent.example", Token: "token value"},
		"long token":       {URL: "https://agent.example", Token: strings.Repeat("s", 8193)},
		"tenant control":   {URL: "https://agent.example", Tenant: "tenant\n"},
		"long tenant":      {URL: "https://agent.example", Tenant: strings.Repeat("t", 257)},
		"long timeout":     {URL: "https://agent.example", Timeout: 6 * time.Minute},
		"negative timeout": {URL: "https://agent.example", Timeout: -time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newA2AClient(config); err == nil {
				t.Fatal("invalid connection accepted")
			}
		})
	}
	client, err := newA2AClient(a2aConnection{URL: "https://agent.example/prefix"})
	if err != nil {
		t.Fatal(err)
	}
	if client.http.Timeout != 10*time.Second || client.http.Jar != nil {
		t.Fatal("unbounded timeout or ambient cookies")
	}
	transport := client.http.Transport.(effectTransport).RoundTripper.(*http.Transport)
	if !transport.DisableKeepAlives || transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("unsafe A2A transport settings")
	}
}

func TestA2ADoesNotReplayLostSendReply(t *testing.T) {
	var effects atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		effects.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	_, err := newTestA2AClient(t, server).Send(context.Background(), "action", []a2aPart{{Text: "Act once"}})
	var delivery *DeliveryError
	if err == nil || errors.As(err, &delivery) || effects.Load() != 1 {
		t.Fatalf("effects=%d, err=%v", effects.Load(), err)
	}
}

func TestA2ARejectsRedirectWithoutForwardingCredentials(t *testing.T) {
	var redirected atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer other.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, status) }))
			defer server.Close()
			client := newTestA2AClient(t, server)
			client.token = "secret-token"
			_, err := client.Send(context.Background(), "action", []a2aPart{{Text: "Investigate"}})
			var response *a2aHTTPError
			if !errors.As(err, &response) || response.Status != status || redirected.Load() != 0 {
				t.Fatalf("redirected=%d, err=%v", redirected.Load(), err)
			}
		})
	}
}

func TestA2ASendRespectsCancellationAndRequestTimeout(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelFirst), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				<-r.Context().Done()
			}))
			defer server.Close()
			client := newTestA2AClient(t, server)
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			if cancelFirst {
				cancel()
			}
			_, err := client.Send(ctx, "action", []a2aPart{{Text: "Investigate"}})
			expected := context.DeadlineExceeded
			if cancelFirst {
				expected = context.Canceled
			}
			if !errors.Is(err, expected) {
				t.Fatalf("error=%v; want %v", err, expected)
			}
			if requests.Load() > 1 || (cancelFirst && requests.Load() != 0) {
				t.Fatalf("unexpected calls=%d", requests.Load())
			}
		})
	}
}

func TestA2ARejectsUntrustedTLSCertificate(t *testing.T) {
	var effects atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { effects.Add(1) }))
	defer server.Close()
	_, err := newTestA2AClient(t, server).Send(context.Background(), "action", []a2aPart{{Text: "Investigate"}})
	if err == nil || effects.Load() != 0 {
		t.Fatalf("effects=%d, err=%v", effects.Load(), err)
	}
}

func TestA2ARequestValidationBeforeSending(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := newTestA2AClient(t, server)
	for name, parts := range map[string][]a2aPart{
		"no parts":         nil,
		"empty part":       {{}},
		"ambiguous part":   {{Text: "text", Data: json.RawMessage(`{}`)}},
		"invalid data":     {{Data: json.RawMessage(`{`)}},
		"duplicate data":   {{Data: json.RawMessage(`{"key":1,"key":2}`)}},
		"invalid UTF8":     {{Text: string([]byte{0xff})}},
		"oversize text":    {{Text: strings.Repeat("a", a2aRequestLimit+1)}},
		"encoded oversize": {{Text: strings.Repeat("<", a2aRequestLimit/2)}},
		"too many parts":   {{Text: "1"}, {Text: "2"}, {Text: "3"}, {Text: "4"}, {Text: "5"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Send(context.Background(), "action", parts); err == nil {
				t.Fatal("invalid input sent")
			}
		})
	}
	for _, id := range []string{"", strings.Repeat("a", 257), "action\n"} {
		if _, err := client.Send(context.Background(), id, []a2aPart{{Text: "Investigate"}}); err == nil {
			t.Fatal("invalid identity sent")
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid inputs made %d requests", calls.Load())
	}
}

func TestA2AResponseValidation(t *testing.T) {
	validMessage := `{"message":{"messageId":"reply","role":"ROLE_AGENT","parts":[{"text":"accepted"}]}}`
	for name, body := range map[string]string{
		"empty":                  "",
		"null":                   "null",
		"array":                  "[]",
		"empty object":           "{}",
		"empty task":             `{"task":{}}`,
		"null task":              `{"task":null}`,
		"duplicate task":         `{"task":` + a2aWorkingTask + `,"task":` + a2aWorkingTask + `}`,
		"both variants":          strings.TrimSuffix(validMessage, "}") + `,"task":` + a2aWorkingTask + `}`,
		"error and task":         `{"task":` + a2aWorkingTask + `,"error":{"code":500}}`,
		"duplicate state":        `{"task":{"id":"t","status":{"state":"TASK_STATE_COMPLETED","state":"TASK_STATE_WORKING"}}}`,
		"numeric state":          `{"task":{"id":"t","status":{"state":3}}}`,
		"duplicate alias":        `{"task":{"id":"t","contextId":"a","context_id":"b","status":{"state":"TASK_STATE_COMPLETED"}}}`,
		"wrong role":             strings.ReplaceAll(validMessage, "ROLE_AGENT", "ROLE_USER"),
		"missing parts":          `{"message":{"messageId":"reply","role":"ROLE_AGENT"}}`,
		"null part":              `{"message":{"messageId":"reply","role":"ROLE_AGENT","parts":[null]}}`,
		"nonstring text":         `{"message":{"messageId":"reply","role":"ROLE_AGENT","parts":[{"text":123}]}}`,
		"null text":              `{"message":{"messageId":"reply","role":"ROLE_AGENT","parts":[{"text":null}]}}`,
		"multiple part variants": `{"message":{"messageId":"reply","role":"ROLE_AGENT","parts":[{"text":"x","data":{}}]}}`,
		"long task ID":           strings.Replace(a2aWorkingTask, "task-123", strings.Repeat("x", 257), 1),
		"trailing value":         validMessage + ` {}`,
		"invalid UTF8":           `{"task":` + strings.Replace(a2aWorkingTask, "task-123", string([]byte{0xff}), 1) + `}`,
		"deep metadata":          strings.TrimSuffix(validMessage, "}") + `,"metadata":` + strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34) + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeA2AResponse(w, body) }))
			defer server.Close()
			if _, err := newTestA2AClient(t, server).Send(context.Background(), "action", []a2aPart{{Text: "Investigate"}}); err == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
}

func TestA2AReceiptKeepsProtocolStateWithoutAgentOutput(t *testing.T) {
	for name, body := range map[string]string{
		"task":           `{"task":` + strings.TrimSuffix(a2aWorkingTask, "}") + `,"artifacts":[{"parts":[{"url":"https://secret.invalid/do-not-fetch"}]}],"metadata":{"secret":"not retained"}}}`,
		"message":        `{"message":{"messageId":"reply","role":"ROLE_AGENT","contextId":"","parts":[{"url":"https://secret.invalid/do-not-fetch"}]}}`,
		"proto spelling": `{"task":{"id":"task-123","context_id":"context-456","status":{"state":"TASK_STATE_WORKING"}}}`,
		"future state":   `{"task":{"id":"task-123","status":{"state":"TASK_STATE_FUTURE"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeA2AResponse(w, body) }))
			defer server.Close()
			receipt, err := newTestA2AClient(t, server).Send(context.Background(), "action", []a2aPart{{Text: "Investigate"}})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(receipt)
			if strings.Contains(string(data), "secret") || strings.Contains(string(data), "not retained") {
				t.Fatal("untrusted agent output entered receipt")
			}
			if name == "message" && (receipt.MessageID != "reply" || receipt.TaskID != "" || receipt.State != "") {
				t.Fatalf("message claimed task completion: %+v", receipt)
			}
			if name == "future state" && receipt.State != "TASK_STATE_FUTURE" {
				t.Fatalf("future state was replaced: %+v", receipt)
			}
		})
	}
}

func TestA2ATaskIdentityCannotChange(t *testing.T) {
	for name, body := range map[string]string{
		"task":            strings.ReplaceAll(a2aWorkingTask, "task-123", "another-task"),
		"context":         strings.ReplaceAll(a2aWorkingTask, "context-456", "another-context"),
		"missing context": `{"id":"task-123","status":{"state":"TASK_STATE_COMPLETED"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeA2AResponse(w, body) }))
			defer server.Close()
			_, err := newTestA2AClient(t, server).Task(context.Background(), A2AReceipt{TaskID: "task-123", ContextID: "context-456"})
			if err == nil {
				t.Fatal("changed task identity accepted")
			}
		})
	}
}

func TestA2ATaskPathEscapesIdentity(t *testing.T) {
	id := "task ?#✓"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/tasks/"+id || r.URL.RawQuery != "historyLength=0" {
			t.Errorf("task ID changed URL structure: %s", r.URL)
		}
		data, _ := json.Marshal(map[string]any{"id": id, "status": map[string]string{"state": "TASK_STATE_COMPLETED"}})
		writeA2AResponse(w, string(data))
	}))
	defer server.Close()
	client := newTestA2AClient(t, server)
	client.endpoint.Path = "/agent"
	if _, err := client.Task(context.Background(), A2AReceipt{TaskID: id}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{".", "..", "../task", "task/path", "%2e%2e", "task\\path", "task:subscribe", "task:cancel"} {
		if _, err := client.Task(context.Background(), A2AReceipt{TaskID: id}); err == nil {
			t.Fatalf("unsafe task ID accepted: %q", id)
		}
	}
}

func TestA2ABoundsResponsesAndRedactsErrors(t *testing.T) {
	secret := "private-credential-never-log"
	for _, mode := range []string{"declared size", "chunked size", "partial body", "status", "content type", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", a2aMediaType)
				switch mode {
				case "declared size":
					w.Header().Set("Content-Length", fmt.Sprint(a2aResponseLimit+1))
					_, _ = io.WriteString(w, strings.Repeat("x", a2aResponseLimit+1))
				case "chunked size":
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					_, _ = io.WriteString(w, strings.Repeat("x", a2aResponseLimit+1))
				case "partial body":
					w.Header().Set("Content-Length", "999")
					_, _ = io.WriteString(w, `{"secret":"`+secret+`"`)
				case "status":
					w.WriteHeader(503)
					_, _ = io.WriteString(w, secret)
				case "content type":
					w.Header().Set("Content-Type", secret)
					_, _ = io.WriteString(w, secret)
				case "malformed":
					_, _ = io.WriteString(w, `{"`+secret+`":`)
				}
			}))
			defer server.Close()
			client := newTestA2AClient(t, server)
			client.token = secret
			client.endpoint.Path = "/" + secret
			_, err := client.Send(context.Background(), "action", []a2aPart{{Text: "Investigate"}})
			var delivery *DeliveryError
			if err == nil || strings.Contains(err.Error(), secret) || errors.As(err, &delivery) {
				t.Fatalf("unsafe response handling: %v", err)
			}
		})
	}
}

func TestA2ARespectsSSRFProtection(t *testing.T) {
	previous := SSRFProtect
	SSRFProtect = true
	t.Cleanup(func() { SSRFProtect = previous })
	if _, err := newA2AClient(a2aConnection{URL: "http://127.0.0.1:1234", AllowInsecureHTTP: true}); err == nil {
		t.Fatal("private literal passed SSRF protection")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	client, err := newA2AClient(a2aConnection{URL: strings.Replace(server.URL, "127.0.0.1", "localhost", 1), AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Send(context.Background(), "action", []a2aPart{{Text: "Investigate"}})
	if err == nil || calls.Load() != 0 {
		t.Fatalf("resolved private destination reached server: calls=%d, err=%v", calls.Load(), err)
	}
}
