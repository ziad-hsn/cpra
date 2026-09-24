// This program runs from a temporary external module against downloaded SDK
// archives. The HTTP fixture verifies distribution behavior, not server support.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func main() {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer archive-fixture" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "archive-fixture-request")
		switch r.URL.Path {
		case "/api/v2/readyz":
			fmt.Fprint(w, `{"available":true}`)
		case "/api/v2/monitors/one":
			fmt.Fprint(w, `{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"one","configurationVersion":"c1"},"spec":{"check":{"interval":"60s","timeout":"5s","driver":{"type":"http","config":{"url":"https://example.com"}}}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: "archive-fixture", AllowInsecureHTTP: true})
	check(err)
	defer client.CloseIdleConnections()
	ready, err := client.Ready(context.Background())
	check(err)
	if !ready.Data.Available || ready.RequestID != "archive-fixture-request" {
		panic("downloaded SDK lost readiness or request metadata")
	}
	monitor, err := client.Monitors.Get(context.Background(), "one")
	check(err)
	if monitor.Data.Metadata.ID != "one" {
		panic("downloaded SDK lost resource identity")
	}
	encoded, err := json.Marshal(monitor.Data)
	check(err)
	frozen, err := collection.Freeze(context.Background(), []collection.Source{
		collection.Reader("consumer.json", bytes.NewReader(encoded)),
	}, collection.Options{})
	check(err)
	if frozen.Len() != 1 {
		panic("downloaded collection loader lost a resource")
	}
	check(frozen.Close())
	if requests.Load() != 2 {
		panic("downloaded SDK made unexpected requests")
	}
	fmt.Println("downloaded SDK request, response and collection contracts passed")
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
