package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

func TestCompletedMonitorQueriesReclaimRetainedViews(t *testing.T) {
	f := newManagementFixture(t, true)
	for _, id := range []string{"one", "two"} {
		configureControlMonitor(t, f, id)
	}
	firstCursor := ""
	for i := range 3 * managementPrincipalSnapshots {
		response, raw := f.request(t, "GET", "/api/v2/monitors?limit=1", managementOperatorToken, nil, nil)
		var page struct{ NextCursor string }
		if response.StatusCode != 200 || json.Unmarshal(raw, &page) != nil || page.NextCursor == "" {
			t.Fatal("fresh query", i, response.StatusCode, string(raw))
		}
		if firstCursor == "" {
			firstCursor = page.NextCursor
		}
		path := "/api/v2/monitors?limit=1&cursor=" + url.QueryEscape(page.NextCursor)
		response, terminal := f.request(t, "GET", path, managementOperatorToken, nil, nil)
		if response.StatusCode != 200 {
			t.Fatal("last page", i, response.StatusCode, string(terminal))
		}
		response, repeated := f.request(t, "GET", path, managementOperatorToken, nil, nil)
		if response.StatusCode != 200 || !bytes.Equal(terminal, repeated) {
			t.Fatal("final-page replay changed", i, response.StatusCode)
		}
		m := f.server.managementHTTP
		m.mu.Lock()
		retained := len(m.snapshots)
		m.mu.Unlock()
		if retained > managementPrincipalSnapshots {
			t.Fatal("retained cursor bound exceeded", retained)
		}
	}
	response, raw := f.request(t, "GET", "/api/v2/monitors?limit=1&cursor="+url.QueryEscape(firstCursor), managementOperatorToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("evicted cursor was reused", response.StatusCode, string(raw))
	}
}

func TestCursorReclamationPreservesReadersAndPrefersCompleted(t *testing.T) {
	now := time.Now().UTC()
	m := &managementHTTP{snapshots: make(map[string]managementSnapshot), now: func() time.Time { return now }}
	entry := func(principal string, readers int, complete bool, age time.Duration) managementSnapshot {
		return managementSnapshot{principal: principal, created: now.Add(-age), expires: now.Add(time.Minute),
			activity: &cursorActivity{readers: readers, completed: complete, lastUsed: now.Add(-age)}}
	}
	m.snapshots["reading"] = entry("alice", 1, true, time.Minute)
	m.snapshots["idle"] = entry("alice", 0, false, time.Minute)
	m.snapshots["completed"] = entry("alice", 0, true, time.Second)
	m.snapshots["other"] = entry("bob", 0, true, time.Minute)
	m.mu.Lock()
	if !m.reclaimSnapshot("alice", now) {
		t.Fatal("no completed candidate")
	}
	if _, ok := m.snapshots["completed"]; ok {
		t.Fatal("completed view not preferred")
	}
	if !m.reclaimSnapshot("alice", now) {
		t.Fatal("inactive view not reclaimed")
	}
	if m.reclaimSnapshot("alice", now) {
		t.Fatal("in-flight view reclaimed")
	}
	if _, ok := m.snapshots["reading"]; !ok {
		t.Fatal("reader lost view")
	}
	if _, ok := m.snapshots["other"]; !ok {
		t.Fatal("principal pressure evicted another owner")
	}
	m.unlockSnapshots()
	m.releaseCursorRead("reading", m.snapshots["reading"].activity)
	m.closeSnapshots()
}

func TestExpiredCursorDefersOwnerReleaseUntilReadCompletes(t *testing.T) {
	now := time.Now().UTC()
	activity := &cursorActivity{readers: 1, lastUsed: now}
	m := &managementHTTP{snapshots: map[string]managementSnapshot{"reading": {activity: activity, expires: now}}, now: func() time.Time { return now }}
	m.expireSnapshots()
	if !activity.expired || len(m.snapshots) != 1 {
		t.Fatal("active expiration released ownership")
	}
	m.releaseCursorRead("reading", activity)
	if len(m.snapshots) != 0 {
		t.Fatal("expired view retained after final reader")
	}
}

func TestOperationBytePressureReclaimsCompletedOwnerViews(t *testing.T) {
	now := time.Now().UTC()
	m := &managementHTTP{snapshots: make(map[string]managementSnapshot), now: func() time.Time { return now }}
	for _, principal := range []string{"alice", "bob"} {
		m.snapshots[principal] = managementSnapshot{principal: principal, operationBytes: managementPrincipalOperationBytes,
			activity: &cursorActivity{lastUsed: now, completed: true}, expires: now.Add(time.Minute)}
	}
	m.mu.Lock()
	if budget := m.availableOperationSnapshotBudget("alice", collectionExecutionSnapshotBytes); budget != managementPrincipalOperationBytes {
		t.Fatal("completed operation bytes remained charged", budget)
	}
	if _, ok := m.snapshots["bob"]; !ok {
		t.Fatal("principal byte pressure reclaimed another principal")
	}
	m.snapshots["alice-active"] = managementSnapshot{principal: "alice", operationBytes: managementPrincipalOperationBytes,
		activity: &cursorActivity{lastUsed: now, completed: true, readers: 1}, expires: now.Add(time.Minute)}
	if budget := m.availableOperationSnapshotBudget("alice", 1); budget != 0 {
		t.Fatal("active operation byte reservation released", budget)
	}
	m.unlockSnapshots()
	m.releaseCursorRead("alice-active", m.snapshots["alice-active"].activity)
	m.closeSnapshots()
}

func TestOperationBytePressureGrowsPartiallyFreeCapture(t *testing.T) {
	now := time.Now().UTC()
	const used = managementPrincipalOperationBytes - 1<<20
	m := &managementHTTP{snapshots: map[string]managementSnapshot{
		"completed": {principal: "alice", operationBytes: used, activity: &cursorActivity{lastUsed: now, completed: true}, expires: now.Add(time.Minute)},
		"other":     {principal: "bob", operationBytes: used, activity: &cursorActivity{lastUsed: now, completed: true}, expires: now.Add(time.Minute)},
	}, operationReservations: map[string]int64{"alice": 1 << 20}, now: func() time.Time { return now }}
	reservation := &operationSnapshotReservation{principal: "alice", bytes: 1 << 20}
	grown, err := m.growOperationReservation(context.Background(), reservation)
	if err != nil || !grown || reservation.bytes != managementPrincipalOperationBytes || reservation.released {
		t.Fatal("partial free bytes blocked reclamation", grown, reservation, err)
	}
	if _, ok := m.snapshots["completed"]; ok {
		t.Fatal("completed owner bytes remained charged")
	}
	if _, ok := m.snapshots["other"]; !ok {
		t.Fatal("partial principal pressure reclaimed another principal")
	}
	m.releaseOperationReservation(reservation)
	m.closeSnapshots()
}

func TestOperationBytePressureReclaimsDuringRealCapture(t *testing.T) {
	f := newManagementFixture(t, true)
	operationListCredential(t, f, "first")
	operationListCredential(t, f, "second")
	page, err := f.sdk.Operations.List(t.Context(), cpra.ListOptions{Limit: 1})
	if err != nil || page.Data.NextCursor == "" {
		t.Fatal("initial cursor", page, err)
	}
	cursor := page.Data.NextCursor
	last := page
	for pages := 0; last.Data.NextCursor != ""; pages++ {
		if pages == 10 {
			t.Fatal("fixture did not finish its bounded operation list")
		}
		last, err = f.sdk.Operations.List(t.Context(), cpra.ListOptions{Limit: 1, Cursor: last.Data.NextCursor})
		if err != nil {
			t.Fatal("completed cursor", err)
		}
	}
	m := f.server.managementHTTP
	m.mu.Lock()
	entry := m.snapshots[page.Data.Snapshot]
	if entry.activity == nil || !entry.activity.completed {
		m.mu.Unlock()
		t.Fatal("completed query was not retained")
	}
	// Leave a positive budget smaller than even the two-record capture. This
	// exercises the capture-quota path, not just pre-capture zero-free pressure.
	entry.operationBytes = managementPrincipalOperationBytes - 1
	m.snapshots[page.Data.Snapshot] = entry
	m.mu.Unlock()
	read, err := f.sdk.Operations.List(t.Context(), cpra.ListOptions{})
	if err != nil || len(read.Data.Items) != 2 {
		t.Fatal("completed bytes prevented a new bounded capture", read, err)
	}
	response, raw := f.request(t, "GET", "/api/v2/operations?limit=1&cursor="+url.QueryEscape(cursor), managementOperatorToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("reclaimed cursor did not expire", response.StatusCode, string(raw))
	}
}
