package httpserver

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"
)

func TestManagementActionReaderSurvivesCursorOwnerExpiry(t *testing.T) {
	f := newManagementFixture(t, true)
	for _, id := range []string{"one", "two"} {
		_, monitor, guard := configureControlMonitor(t, f, id)
		controlPulse(t, f, monitor, guard, "failure")
	}
	response, raw := f.request(t, "GET", "/api/v2/actions?limit=1", managementOperatorToken, nil, nil)
	var page struct{ NextCursor string }
	if response.StatusCode != 200 || json.Unmarshal(raw, &page) != nil || page.NextCursor == "" {
		t.Fatal("missing action cursor", response.StatusCode)
	}
	m := f.server.managementHTTP
	cursor, err := m.decodeCursor(page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.snapshots[cursor.View]
	m.mu.Unlock()
	read, err := m.beginSnapshotRead(context.Background(), "Action", url.Values{"cursor": {page.NextCursor}}, entry.principal, entry.generation)
	if err != nil {
		t.Fatal(err)
	}
	defer m.releaseSnapshotRead(read)
	m.mu.Lock()
	m.pruneSnapshots(entry.expires.Add(time.Second), "", 0)
	m.unlockSnapshots()
	items, _, err := read.entry.actions.Page(t.Context(), "", read.cursor.After, 1)
	if err != nil || len(items) != 1 {
		t.Fatal("request lost its retained page handle", err)
	}
	if err := m.publishSnapshotRead(t.Context(), read); err == nil {
		t.Fatal("expired request republished its cursor")
	}
}
