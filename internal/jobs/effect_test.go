package jobs

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestEffectHTTPRefusalAndTLSRejection(t *testing.T) {
	err := sendJSON(context.Background(), "webhook", http.MethodPost, "http://"+refusedTCPAddress(t), nil, map[string]string{"message": "fixture"})
	if got := ClassifyEffect(SanitizeError(err)); got.Outcome != EffectNotApplied || !got.Retryable {
		t.Fatalf("refused connection lost no-transmission evidence: %+v, %v", got, err)
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	err = sendJSON(context.Background(), "webhook", http.MethodPost, server.URL, nil, struct{}{})
	if got := ClassifyEffect(SanitizeError(err)); got.Outcome != EffectNotApplied || got.Retryable || calls.Load() != 0 {
		t.Fatalf("TLS rejection was retried or treated as an effect: %+v, calls=%d", got, calls.Load())
	}
}

func TestEffectHTTPAfterTransmissionRemainsUnknown(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "500", true: "lost response"}[lost], func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				calls.Add(1)
				if lost {
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.Close()
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			err := sendJSON(context.Background(), "webhook", http.MethodPost, server.URL, map[string]string{"Idempotency-Key": "fixture"}, struct{}{})
			if got := ClassifyEffect(err); got.Outcome != EffectUnknown || got.Retryable || calls.Load() != 1 {
				t.Fatalf("uncertain effect replayed or made definite: %+v calls=%d", got, calls.Load())
			}
		})
	}
}

func TestEffectLocalRejectionAndAuthorizationExpiry(t *testing.T) {
	job := &CodeLogJob{File: "outside.json"}
	if got := ClassifyEffect(job.Execute().Err); got.Outcome != EffectNotApplied || got.Retryable {
		t.Fatalf("unconfigured log root: %+v", got)
	}
	called, finished := 0, 0
	d := NewDispatch(&projectionFixture{called: &called}, ecs.Entity{}, "intervention", "", 1, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.SetContext(ctx)
	d.Authorize = func(context.Context) (ActionExecution, error) {
		cancel()
		return ActionExecution{Finish: func() error { finished++; return nil }}, nil
	}
	r := d.Execute()
	if got := ClassifyEffect(r.Err); got.Outcome != EffectNotApplied || got.Retryable || called != 0 || finished != 1 || !errors.Is(r.Err, context.Canceled) {
		t.Fatalf("authorization expiry invoked provider or lost finalization: %+v called=%d finished=%d", got, called, finished)
	}
}

func TestEffectDockerRefusalRetainsTransportEvidence(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://"+refusedTCPAddress(t))
	t.Setenv("DOCKER_API_VERSION", "1.52")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	job := &InterventionDockerJob{Container: "fixture", Timeout: time.Second}
	result := job.Execute()
	if got := ClassifyEffect(result.Err); got.Outcome != EffectNotApplied || !got.Retryable {
		t.Fatalf("Docker discarded pre-transmission evidence: %+v err=%v", got, result.Err)
	}
}

func TestEffectPartialWriteRemainsUnknown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan int, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- 0
			return
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		n, _ := conn.Read(make([]byte, 512))
		_ = conn.Close()
		done <- n
	}()
	err = sendJSON(t.Context(), "webhook", http.MethodPost, "http://"+ln.Addr().String(), nil, strings.Repeat("x", 1<<20))
	if n := <-done; n == 0 || ClassifyEffect(err).Outcome != EffectUnknown {
		t.Fatalf("partial transmission became definite: received=%d err=%v", n, err)
	}
}

func TestEffectLogWriteFailureIsUnknown(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed-log")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	stdout := os.Stdout
	os.Stdout = f
	result := (&CodeLogJob{Monitor: "fixture"}).Execute()
	os.Stdout = stdout
	if result.Err == nil || ClassifyEffect(result.Err).Outcome != EffectUnknown {
		t.Fatal("write failure made definite", result.Err)
	}
}

func TestEffectRecorderDoesNotReclassifyEarlierMutation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer server.Close()
	tr := &http.Transport{DialContext: dialEffect, DisableKeepAlives: true}
	recorder := &effectRecorder{effectTransport: effectTransport{tr}}
	client := &http.Client{Transport: recorder, Timeout: time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Post(server.URL, "text/plain", strings.NewReader("effect"))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	_, err = client.Get("http://" + refusedTCPAddress(t))
	if err == nil || ClassifyEffect(recorder.restore(errors.New("SDK lost error type"))).Outcome != EffectUnknown {
		t.Fatal("later read made earlier mutation definite")
	}
}

type noEffectTimeoutTransport struct{}

func (noEffectTimeoutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, noEffect(r.Context().Err(), true)
}

func TestEffectPreservesPositiveEvidenceThroughClientTimeout(t *testing.T) {
	client := &http.Client{Transport: noEffectTimeoutTransport{}, Timeout: time.Millisecond}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://unused.example", nil)
	_, err := doEffect(client, req)
	if got := ClassifyEffect(err); got.Outcome != EffectNotApplied || !got.Retryable {
		t.Fatal("Client.Timeout erased the transport's affirmative evidence", got)
	}
}

func TestEffectSMTPAcceptanceBoundary(t *testing.T) {
	for _, reply := range []string{"450 unavailable", "550 rejected", "250 queued", ""} {
		t.Run(map[string]string{"450 unavailable": "temporary", "550 rejected": "permanent", "250 queued": "accepted", "": "lost acceptance"}[reply], func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			var messages atomic.Int32
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				reader := bufio.NewReader(conn)
				_, _ = io.WriteString(conn, "220 fixture\r\n")
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if !strings.HasPrefix(line, "DATA") {
						_, _ = io.WriteString(conn, "250 ready\r\n")
						continue
					}
					_, _ = io.WriteString(conn, "354 continue\r\n")
					for {
						line, err = reader.ReadString('\n')
						if err != nil {
							return
						}
						if line == ".\r\n" {
							break
						}
					}
					if reply == "" || strings.HasPrefix(reply, "250") {
						messages.Add(1)
					}
					if reply != "" {
						_, _ = io.WriteString(conn, reply+"\r\n")
					}
					return // A successful DATA reply remains success when QUIT is impossible.
				}
			}()
			err = sendEmail(context.Background(), manifest.CodeNotificationEmail{Server: ln.Addr().String(), From: "cpra@example.org", To: "ops@example.org", AllowInsecure: true}, "fixture")
			<-done
			got := ClassifyEffect(err)
			want := EffectNotApplied
			if reply == "" {
				want = EffectUnknown
			}
			if strings.HasPrefix(reply, "250") {
				want = EffectSucceeded
			}
			if got.Outcome != want || got.Retryable != strings.HasPrefix(reply, "450") || (messages.Load() == 1) != (want != EffectNotApplied) {
				t.Fatalf("DATA boundary: %+v accepted=%d err=%v", got, messages.Load(), err)
			}
		})
	}
}
