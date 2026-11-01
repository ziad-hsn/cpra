package jobs

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

var httpClientPool sync.Map

type httpClientKey struct {
	timeout                          time.Duration
	protect, insecure, singleAttempt bool
}

func GetHTTPClient(timeout time.Duration) *http.Client { return httpClient(timeout, false) }
func httpClient(timeout time.Duration, insecure bool) *http.Client {
	return httpClientWithPolicy(timeout, insecure, false)
}

// Effects use a fresh connection so net/http cannot transparently replay a
// GET or Idempotency-Key request after a lost response on a reused connection.
func effectHTTPClient(timeout time.Duration) *http.Client {
	return httpClientWithPolicy(timeout, false, true)
}

// Keep affirmative transport evidence across http.Client's timeout decoration,
// which can replace a wrapped error with a new error containing only its text.
func doEffect(client *http.Client, request *http.Request) (*http.Response, error) {
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	recorder := &effectRecorder{effectTransport: effectTransport{transport}}
	copy := *client
	copy.Transport = recorder
	response, err := copy.Do(request)
	observeNotificationRate(request.Context(), response)
	return response, recorder.restore(err)
}

func httpClientWithPolicy(timeout time.Duration, insecure, singleAttempt bool) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	key := httpClientKey{timeout, SSRFProtect, insecure, singleAttempt}
	if v, ok := httpClientPool.Load(key); ok {
		return v.(*http.Client)
	}
	tr := &http.Transport{
		DisableKeepAlives: singleAttempt,
		MaxIdleConns:      512, MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure},
	}
	if key.protect {
		tr.DialContext = protectedDial
	}
	var transport http.RoundTripper = tr
	if singleAttempt {
		tr.DialContext = effectDial(tr.DialContext)
		transport = effectTransport{tr}
	}
	c := &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	actual, _ := httpClientPool.LoadOrStore(key, c)
	return actual.(*http.Client)
}

type effectTransport struct{ http.RoundTripper }

func effectDial(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	if dial == nil {
		return dialEffect
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		return conn, noEffect(err, transientBeforeEffect(err))
	}
}

func dialEffect(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	return conn, noEffect(err, transientBeforeEffect(err))
}

func (t effectTransport) CloseIdleConnections() {
	if closer, ok := t.RoundTripper.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t effectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, noEffect(err, false)
	}
	resp, err := t.RoundTripper.RoundTrip(req)
	// Go verifies the peer during the TLS handshake, before sending the HTTP
	// request. Missing tracing callbacks are deliberately not used as evidence.
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		err = noEffect(err, false)
	}
	return resp, err
}

func postNotification(ctx context.Context, target, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return nil, noEffect(err, false)
	}
	req.Header.Set("Content-Type", contentType)
	return doEffect(effectHTTPClient(10*time.Second), req)
}
