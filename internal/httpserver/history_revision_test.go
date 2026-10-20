package httpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	bolt "go.etcd.io/bbolt"
)

func TestHistoryHTTPPreservesLargeHistoricalRevision(t *testing.T) {
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = t.TempDir()
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	index := store.Status().CommittedIndex
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Seed an old writer's canonical ordinary-event row while the store is
	// stopped. The separate persistence lifecycle regression produces this
	// representation through real historical commands and replay.
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	event := persistence.Event{ID: fmt.Sprintf("%020d:%08d", index, 0), MonitorID: "historical", Revision: strings.Repeat("r", (2<<20)+1024), At: now, Type: "action_unknown", ActionID: "original-action", Kind: "intervention"}
	dir := filepath.Join(cfg.Storage.Directory, "history")
	db, err := bolt.Open(filepath.Join(dir, day+".db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("events"))
		if err != nil {
			return err
		}
		return b.Put([]byte(event.MonitorID+"\x00"+event.ID), raw)
	})
	if closeErr := db.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	catalogPath := filepath.Join(dir, "catalog.json")
	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]any
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	catalog["segments"].(map[string]any)[day] = true
	data, err = json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	store, err = persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal("historical fixture could not reopen", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := newTestServer(t)
	s.cfg.Store, s.cfg.AuthToken = store, managementReaderToken
	server := httptest.NewTLSServer(s.authMiddleware(apiMux(s)))
	t.Cleanup(server.Close)
	client, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: managementReaderToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	page, err := client.History(t.Context(), cpra.ListOptions{MonitorID: event.MonitorID, Limit: 100})
	if err != nil || len(page.Data.Items) != 1 {
		t.Fatal("v2 history unavailable", err)
	}
	if page.Data.Items[0].ExecutionRevision != event.Revision {
		t.Fatal("v2 execution revision changed")
	}
	encoded, err := json.Marshal(page.Data)
	if err != nil || len(encoded) > managementMaxPageBytes {
		t.Fatal("public page exceeded its existing limit", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/history?monitor_id="+event.MonitorID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+managementReaderToken)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, managementMaxPageBytes+1))
	var v1 persistence.HistoryPage
	if err != nil || response.StatusCode != 200 || len(data) > managementMaxPageBytes || json.Unmarshal(data, &v1) != nil || len(v1.Events) != 1 {
		t.Fatal("v1 history unavailable", response.StatusCode, err)
	}
	if v1.Events[0].Revision != event.Revision {
		t.Fatal("v1 execution revision changed")
	}
}
