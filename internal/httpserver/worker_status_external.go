//go:build externaljobs

package httpserver

import (
	"context"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Each view contains at most MaxWorkers scalar rows. Reserve before capture;
// an expired cursor still owns its capacity while a reader is using the view.
const maxWorkerSnapshots = 8
const maxPrincipalWorkerSnapshots = 2

type workerStatusSnapshot struct {
	rows      []api.WorkerObservation
	generated time.Time
	refs      atomic.Int32
	total     *atomic.Int32
	principal *atomic.Int32
}

func (s *workerStatusSnapshot) release() {
	if s != nil && s.refs.Add(-1) == 0 {
		s.total.Add(-1)
		s.principal.Add(-1)
	}
}

// Called only while the cursor metadata mutex is held; capture runs afterward.
func (m *managementHTTP) reserveWorkerSnapshot(principal string) (snapshotExtensions, error) {
	if m.workerPrincipalSnapshots == nil {
		m.workerPrincipalSnapshots = make(map[string]*atomic.Int32)
	}
	for id, count := range m.workerPrincipalSnapshots {
		if count.Load() == 0 {
			delete(m.workerPrincipalSnapshots, id)
		}
	}
	count := m.workerPrincipalSnapshots[principal]
	if count == nil {
		count = &atomic.Int32{}
		m.workerPrincipalSnapshots[principal] = count
	}
	if count.Load() >= maxPrincipalWorkerSnapshots || m.workerSnapshotCount.Load() >= maxWorkerSnapshots {
		return snapshotExtensions{}, managementFailure(429, "snapshotQuota", "Worker observation capacity is reserved. Reuse an existing cursor or wait for expiry.")
	}
	m.workerSnapshotCount.Add(1)
	count.Add(1)
	view := &workerStatusSnapshot{total: &m.workerSnapshotCount, principal: count}
	view.refs.Store(2)
	return snapshotExtensions{workers: view}, nil
}

func (m *managementHTTP) handleWorkerList(w http.ResponseWriter, r *http.Request, store *persistence.Store) {
	managementHeaders(w)
	if _, err := m.auth.Authorize(r, "ListWorkers"); err != nil {
		writeManagementError(w, err)
		return
	}
	query, err := managementListQuery(r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	var result api.WorkerObservationList
	err = m.withSnapshotRead(r, "ListWorkers", "ExternalWorker", query, func(ctx context.Context, read *snapshotRead) error {
		view := read.entry.workers
		if store == nil || view == nil {
			return management.ErrUnavailable
		}
		if read.fresh {
			view.generated = read.observed
			rows, err := store.WorkerStatuses(ctx, view.generated)
			if err != nil {
				// Native storage errors and paths must not enter an API response.
				return management.ErrUnavailable
			}
			view.rows = make([]api.WorkerObservation, 0, len(rows))
			for _, row := range rows {
				item := api.WorkerObservation{WorkerID: row.ID, WorkerUID: row.UID, SessionStatus: row.SessionStatus,
					CapacityAvailable: row.CapacityAvailable, Capacity: int64(row.Capacity), Active: int64(row.Active), UnknownActions: int64(row.UnknownActions)}
				if !row.LastSeenAt.IsZero() {
					at := row.LastSeenAt
					item.LastSeenAt = &at
				}
				view.rows = append(view.rows, item)
			}
		}
		first := sort.Search(len(view.rows), func(i int) bool { return view.rows[i].WorkerID > read.cursor.After })
		end := min(len(view.rows), first+read.entry.limit)
		result.Items = append(make([]api.WorkerObservation, 0, end-first), view.rows[first:end]...)
		result.GeneratedAt = view.generated
		if end < len(view.rows) {
			read.after = view.rows[end-1].WorkerID
			result.NextCursor = m.encodeCursor(managementCursor{View: read.cursor.View, After: read.after})
		}
		return ctx.Err()
	})
	if err != nil {
		writeManagementError(w, err)
		return
	}
	writeManagementJSON(w, http.StatusOK, result)
}
