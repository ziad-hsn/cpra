package httpserver

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

type operationCompletedRead struct {
	managementOperationReadView
	readDone chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (v *operationCompletedRead) ReadPage(ctx context.Context, monitor, after string, limit int) (management.ManagementOperationPage, error) {
	page, err := v.managementOperationReadView.ReadPage(ctx, monitor, after, limit)
	close(v.readDone)
	defer close(v.returned)
	select {
	case <-v.release:
		return page, err
	case <-ctx.Done():
		return management.ManagementOperationPage{}, ctx.Err()
	}
}

func TestManagementOperationReadsRecheckStorageAfterCursorWait(t *testing.T) {
	f := newManagementFixture(t, true)
	operationListCredential(t, f, "one")
	operationListCredential(t, f, "two")
	first, err := f.sdk.Operations.List(t.Context(), cpra.ListOptions{Limit: 1})
	if err != nil || first.Data.NextCursor == "" {
		t.Fatal("missing initial cursor", err)
	}
	m := f.server.managementHTTP
	cursor, err := m.decodeCursor(first.Data.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.snapshots[cursor.View]
	gate := &operationCompletedRead{managementOperationReadView: entry.operations, readDone: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	entry.operations = gate
	m.snapshots[cursor.View] = entry
	m.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	request := validationAsyncRead(f, t.Context(), "/api/v2/operations?cursor="+url.QueryEscape(first.Data.NextCursor))
	select {
	case <-gate.readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("protected operation read did not complete")
	}
	m.mu.Lock()
	release()
	select {
	case <-gate.returned:
	case <-time.After(time.Second):
		m.mu.Unlock()
		t.Fatal("completed operation page did not return")
	}
	err = f.server.cfg.Store.Close()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	response := validationReadWait(t, request)
	if response.err != nil || response.status != http.StatusGone || response.problem.Code != "cursorExpired" {
		t.Fatal("post-read storage failure disclosed a stale operation page", response)
	}
}
