package jobs

import (
	"context"
	"crypto/tls"
	"io"
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
	c := &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	actual, _ := httpClientPool.LoadOrStore(key, c)
	return actual.(*http.Client)
}

func postNotification(ctx context.Context, target, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return effectHTTPClient(10 * time.Second).Do(req)
}
