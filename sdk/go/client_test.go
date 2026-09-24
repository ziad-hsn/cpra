package cpra

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func fixture(t *testing.T, h http.HandlerFunc, opts func(*Config)) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	cfg := Config{BaseURL: s.URL, AllowInsecureHTTP: true, AuthToken: "test-secret"}
	if opts != nil {
		opts(&cfg)
	}
	c, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func monitor() api.Monitor {
	return api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "m"}, Spec: api.MonitorSpec{Check: api.CheckSpec{Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test","insecureSkipVerify":false}`)}, Interval: "60s", Timeout: "5s"}, Enabled: api.Pointer(false)}}
}
func TestMutationPreconditionsAndPresence(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("auth missing")
		}
		if r.Method == "POST" && r.Header.Get("If-None-Match") != "*" {
			t.Error("absence condition missing")
		}
		if r.Method == "PUT" && r.Header.Get("If-Match") != `"rv-1"` {
			t.Error("CAS missing")
		}
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"enabled":false`) {
			t.Errorf("explicit false lost: %s", raw)
		}
		w.Header().Set("ETag", `"rv-2"`)
		w.Header().Set("X-Request-ID", "req-2")
		_ = json.NewEncoder(w).Encode(monitor())
	}, nil)
	if _, e := c.Monitors.Replace(context.Background(), "m", "", monitor()); e == nil {
		t.Fatal("missing CAS accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request sent")
	}
	r, e := c.Monitors.Create(context.Background(), monitor())
	if e != nil {
		t.Fatal(e)
	}
	if r.ResourceVersion != "rv-2" || r.RequestID != "req-2" {
		t.Fatal("response identity lost")
	}
	if _, e = c.Monitors.Replace(context.Background(), "m", "rv-1", monitor()); e != nil {
		t.Fatal(e)
	}
}
func TestRedirectNeverForwardsCredentials(t *testing.T) {
	var called atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
	defer target.Close()
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}, nil)
	_, e := c.State(context.Background())
	var problem *Error
	if !errors.As(e, &problem) || problem.StatusCode != 307 {
		t.Fatal(e)
	}
	if called.Load() {
		t.Fatal("followed redirect")
	}
}
func TestTLSAndURLPolicy(t *testing.T) {
	for _, url := range []string{"ftp://host", "http://u:secret@host", "http://host?token=secret", "http://host/#fragment"} {
		_, e := New(Config{BaseURL: url})
		if e == nil || strings.Contains(e.Error(), "secret") {
			t.Fatalf("unsafe URL %q %v", url, e)
		}
	}
	if _, e := New(Config{BaseURL: "http://localhost", AuthToken: "secret"}); e == nil {
		t.Fatal("insecure auth accepted")
	}
	if _, e := New(Config{BaseURL: "https://host", MaxResponseBytes: 1 << 62}); e == nil {
		t.Fatal("overflow bound accepted")
	}
}
func TestBoundedResponseAndAmbiguousMutation(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, strings.Repeat("x", 17)) }, func(c *Config) { c.MaxResponseBytes = 16 })
	_, e := c.State(context.Background())
	if !errors.Is(e, ErrResponseTooLarge) {
		t.Fatal(e)
	}
	_, e = c.Monitors.Create(context.Background(), monitor())
	if !errors.Is(e, ErrAmbiguous) || !errors.Is(e, ErrResponseTooLarge) {
		t.Fatal(e)
	}
}
func TestErrorRedactionAndIdentity(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Operation-ID", "op-1")
		w.WriteHeader(412)
		_, _ = io.WriteString(w, `{"detail":"provider-secret","code":"conflict","errors":[{"field":"metadata.resourceVersion","message":"changed"}]}`)
	}, nil)
	_, e := c.Monitors.Delete(context.Background(), "m", "rv-1")
	var problem *Error
	if !errors.As(e, &problem) || !errors.Is(e, ErrConflict) || errors.Is(e, ErrAmbiguous) {
		t.Fatal(e)
	}
	if problem.OperationID != "op-1" || len(problem.Problem.Errors) != 1 {
		t.Fatal("error identity or fields lost")
	}
	if strings.Contains(e.Error(), "provider-secret") {
		t.Fatal("secret printed")
	}
}
func TestReadRetriesOnly(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }, func(c *Config) { c.ReadAttempts = 3 })
	_, _ = c.State(context.Background())
	if calls.Load() != 3 {
		t.Fatalf("read attempts %d", calls.Load())
	}
	calls.Store(0)
	_, e := c.Monitors.Create(context.Background(), monitor())
	if calls.Load() != 1 || !errors.Is(e, ErrAmbiguous) {
		t.Fatalf("mutation retried %d %v", calls.Load(), e)
	}
}
func TestCancellationAndLocalTokenFailure(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }, func(c *Config) {
		c.AuthToken = ""
		c.TokenSource = func(context.Context) (string, error) { return "", errors.New("secret-provider-url") }
	})
	_, e := c.Monitors.Create(context.Background(), monitor())
	if e == nil || errors.Is(e, ErrAmbiguous) || strings.Contains(e.Error(), "secret-provider-url") || calls.Load() != 0 {
		t.Fatal(e)
	}
	c = fixture(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, e = c.State(ctx)
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}
func TestUnknownDriverReadCannotBeReapplied(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		m := monitor()
		m.Spec.Check.Driver.Type = "future-driver"
		m.Spec.Check.Driver.Config = json.RawMessage(`{"newField":17}`)
		_ = json.NewEncoder(w).Encode(m)
	}, nil)
	r, e := c.Monitors.Get(context.Background(), "m")
	if e != nil {
		t.Fatal(e)
	}
	if string(r.Data.Spec.Check.Driver.Config) != `{"newField":17}` {
		t.Fatal("unknown configuration lost")
	}
	_, e = c.Monitors.Replace(context.Background(), "m", "rv-1", r.Data)
	if !errors.Is(e, api.ErrUnsupportedDriver) || calls.Load() != 1 {
		t.Fatal("unsupported read blindly replaced", e)
	}
}
func TestIteratorBoundedAndLazy(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.URL.Query().Get("limit") != "100" {
			t.Error("unbounded default")
		}
		next := "next"
		if n == 2 {
			next = ""
		}
		_ = json.NewEncoder(w).Encode(api.MonitorList{Items: []api.Monitor{monitor()}, NextCursor: next})
	}, nil)
	i := c.Monitors.Iterate(ListOptions{})
	if calls.Load() != 0 {
		t.Fatal("eager iterator")
	}
	count := 0
	for i.Next(context.Background()) {
		count++
	}
	if i.Err() != nil || count != 2 || calls.Load() != 2 {
		t.Fatal(count, i.Err())
	}
}
func TestWaitCancellationDoesNotCancelOperation(t *testing.T) {
	var paths []string
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_ = json.NewEncoder(w).Encode(api.Operation{ID: "op-1", State: "applying"})
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r, e := c.Operations.Wait(ctx, "op-1")
	if !errors.Is(e, context.DeadlineExceeded) || r.Data.ID != "op-1" || len(paths) != 1 {
		t.Fatal(e, paths)
	}
}
func TestPatchRejectsDangerousInputs(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("invalid patch transmitted") }, nil)
	for _, p := range []string{`null`, `{"status":{}}`, `{"spec":{},"spec":{}}`, `{"spec":{}} {}`} {
		if _, e := c.Monitors.Patch(context.Background(), "m", "rv-1", api.MergePatch(p)); e == nil {
			t.Fatal(p)
		}
	}
	if _, e := c.Monitors.Snooze(context.Background(), "m", api.ControlRequest{Revision: "rv\r\ninjected"}); e == nil {
		t.Fatal("invalid header transmitted")
	}
}

func TestRetryAfterSaturatesBeforeArithmetic(t *testing.T) {
	for _, value := range []string{"9223372036854775807", "99999999999999999999999999999999999", "60"} {
		if got := retryDelay(value); got != 30*time.Second {
			t.Fatalf("%s: %s", value, got)
		}
	}
}
func TestReplacementIdentityAndLocalSerialization(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("invalid replacement sent") }, nil)
	if _, err := c.Monitors.Replace(context.Background(), "other", "rv-1", monitor()); err == nil || errors.Is(err, ErrAmbiguous) {
		t.Fatal(err)
	}
	var err error
	_, err = response[api.State](c, nil, &json.MarshalerError{Type: nil, Err: errors.New("invalid JSON")}, true)
	if err == nil || errors.Is(err, ErrAmbiguous) {
		t.Fatal("local serialization classified as uncertain", err)
	}
}
func TestCallerHTTPClientPolicyIsCopied(t *testing.T) {
	hc := &http.Client{Timeout: time.Second}
	c, e := New(Config{BaseURL: "https://host", HTTPClient: hc})
	if e != nil {
		t.Fatal(e)
	}
	defer c.CloseIdleConnections()
	if hc.CheckRedirect != nil || hc.Timeout != time.Second {
		t.Fatal("caller client mutated")
	}
}

func TestMergePatchNullIsTransmitted(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"recovery":null`) {
			t.Fatal("null removed", string(raw))
		}
		_ = json.NewEncoder(w).Encode(monitor())
	}, nil)
	if _, err := c.Monitors.Patch(context.Background(), "m", "rv-1", api.MergePatch(`{"spec":{"recovery":null}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestEmptySuccessIsNeverTypedSuccess(t *testing.T) {
	for _, body := range []string{"", "null", "{}"} {
		t.Run(body, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Operation-ID", "op-1")
				if r.Method == http.MethodPost {
					w.WriteHeader(http.StatusCreated)
				}
				_, _ = io.WriteString(w, body)
			}, nil)
			if _, err := c.State(context.Background()); err == nil {
				t.Fatal("empty read accepted")
			}
			r, err := c.Monitors.Create(context.Background(), monitor())
			if !errors.Is(err, ErrAmbiguous) || r.OperationID != "op-1" {
				t.Fatal("missing mutation receipt accepted", err)
			}
		})
	}
}
func TestIteratorStartsAtRequestedCursor(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "resume" {
			t.Fatal("resume cursor dropped")
		}
		_ = json.NewEncoder(w).Encode(api.MonitorList{Items: []api.Monitor{monitor()}})
	}, nil)
	i := c.Monitors.Iterate(ListOptions{Cursor: "resume"})
	if !i.Next(context.Background()) || i.Err() != nil {
		t.Fatal(i.Err())
	}
}

func TestStrongVersionPreconditionsRejectWireSyntax(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Error("invalid mutation reached transport")
	}, nil)
	for _, version := range []string{"", `"already-quoted"`, `W/"weak"`, "W/revision", "*", `a,b`, "a\\b", "line\nfeed", "space value", "\x7f", strings.Repeat("x", 257)} {
		if _, err := c.Monitors.Replace(context.Background(), "m", version, monitor()); err == nil {
			t.Errorf("version %q: %v", version, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid revisions caused network I/O")
	}
	for _, version := range []string{"revision-1", "rv_ABC.123:abc", "opaque+value=123"} {
		if err := precondition(version); err != nil {
			t.Fatal("valid opaque revision rejected", err)
		}
	}
}
