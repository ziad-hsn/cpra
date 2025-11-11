package jobs

import (
	"net/http"
	"sync"
	"time"
)

// httpClientPool stores shared HTTP clients keyed by timeout to promote
// connection reuse and reduce per-entity client allocations.
var httpClientPool sync.Map // map[time.Duration]*http.Client

// GetHTTPClient returns a shared *http.Client for the given timeout.
// Clients share a Transport with sensible connection pooling defaults.
func GetHTTPClient(timeout time.Duration) *http.Client {
	if v, ok := httpClientPool.Load(timeout); ok {
		return v.(*http.Client)
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        512,
			MaxIdleConnsPerHost: 64,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	actual, _ := httpClientPool.LoadOrStore(timeout, client)
	return actual.(*http.Client)
}
