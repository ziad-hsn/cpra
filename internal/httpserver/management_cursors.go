package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type snapshotPageView[T any] interface {
	Page(context.Context, string, string, int) ([]T, string, error)
}

type snapshotRead struct {
	entry            managementSnapshot
	cursor           managementCursor
	observed         time.Time
	fresh, published bool
	after            string
}

// withObservationRead keeps detail reads outside the policy lock, just like
// paginated reads, and discards their result if authority changes during I/O.
func (m *managementHTTP) withObservationRead(r *http.Request, operation string, read func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var principal string
	var generation uint64
	if err := m.withReadPolicy(r, operation, func(access api.AccessInfo, current uint64) error {
		principal, generation = access.PrincipalID, current
		return ctx.Err()
	}); err != nil {
		return err
	}
	select {
	case m.snapshotReads <- struct{}{}:
		defer func() { <-m.snapshotReads }()
	default:
		return managementFailure(429, "readBusy", "Snapshot read capacity is busy.")
	}
	err := read(ctx)
	permissionErr := m.withReadPolicy(r, operation, func(access api.AccessInfo, current uint64) error {
		if access.PrincipalID != principal || current != generation {
			return managementFailure(410, "cursorExpired", "Authorization changed. Read the resource again.")
		}
		return ctx.Err()
	})
	if permissionErr != nil {
		return permissionErr
	}
	return err
}

// withSnapshotRead brackets bounded storage work with current authorization.
// Cursor reservations and publication use short metadata critical sections;
// neither the cursor mutex nor the policy lock is held across capture/Page I/O.
func (m *managementHTTP) withSnapshotRead(r *http.Request, operation, kind string, query url.Values, page func(context.Context, *snapshotRead) error) error {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var principal string
	var generation uint64
	if err := m.withReadPolicy(r, operation, func(access api.AccessInfo, current uint64) error {
		principal, generation = access.PrincipalID, current
		return ctx.Err()
	}); err != nil {
		return err
	}
	select {
	case m.snapshotReads <- struct{}{}:
		defer func() { <-m.snapshotReads }()
	default:
		return managementFailure(429, "readBusy", "Snapshot read capacity is busy.")
	}
	read, err := m.beginSnapshotRead(ctx, kind, query, principal, generation)
	if err != nil {
		return err
	}
	defer m.releaseSnapshotRead(read)
	err = page(ctx, read)
	permissionErr := m.withReadPolicy(r, operation, func(access api.AccessInfo, current uint64) error {
		if access.PrincipalID != principal || current != generation {
			return managementFailure(410, "cursorExpired", "Authorization changed. Start a new query.")
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if err != nil {
			return nil
		}
		return m.publishSnapshotRead(ctx, read)
	})
	if permissionErr != nil {
		return permissionErr
	}
	return err
}

func (m *managementHTTP) beginSnapshotRead(ctx context.Context, kind string, query url.Values, principal string, generation uint64) (*snapshotRead, error) {
	limit := 0
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 500 {
			return nil, managementFailure(400, "invalidLimit", "Page limit must be from 1 to 500.")
		}
		limit = parsed
	}
	if err := m.lockSnapshots(ctx); err != nil {
		return nil, err
	}
	defer m.unlockSnapshots()
	if !m.cursorReady {
		return nil, management.ErrUnavailable
	}
	now := m.now().UTC()
	m.pruneSnapshots(now, principal, generation)
	read := &snapshotRead{observed: now}
	if value := query.Get("cursor"); value != "" {
		cursor, err := m.decodeCursor(value)
		if err != nil {
			return nil, err
		}
		entry, ok := m.snapshots[cursor.View]
		if !ok || entry.principal != principal || entry.generation != generation || entry.kind != kind {
			return nil, managementFailure(410, "cursorExpired", "The cursor expired or belongs to another access context.")
		}
		if (limit != 0 && limit != entry.limit) || query.Get("monitorID") != entry.monitorID {
			return nil, managementFailure(400, "cursorMismatch", "Continue with the original page limit and monitor selection.")
		}
		if now.Before(entry.created) {
			return nil, management.ErrUnavailable
		}
		read.entry, read.cursor = entry, cursor
		if entry.actionOwner != nil {
			view, err := entry.actionOwner.Retain()
			if err != nil {
				return nil, management.ErrUnavailable
			}
			read.entry.actions, read.entry.actionOwner = view, &view
		}
		read.entry.snapshotExtensions.retain()
		return read, nil
	}
	if err := m.snapshotCountQuota(principal); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 100
	}
	read.fresh = true
	read.cursor.View = uuid.NewString()
	read.entry = managementSnapshot{monitorID: query.Get("monitorID"), principal: principal, generation: generation, kind: kind, limit: limit, created: now, expires: now.Add(managementCursorTTL)}
	extension, err := m.reserveSnapshotExtension(kind, principal)
	if err != nil {
		return nil, err
	}
	read.entry.snapshotExtensions = extension
	// Reserve before capture so concurrent requests cannot exceed the quota.
	m.snapshots[read.cursor.View] = read.entry
	return read, nil
}

func (m *managementHTTP) publishSnapshotRead(ctx context.Context, read *snapshotRead) error {
	if err := m.lockSnapshots(ctx); err != nil {
		return err
	}
	defer m.unlockSnapshots()
	now := m.now().UTC()
	if now.Before(read.observed) {
		return management.ErrUnavailable
	}
	if !now.Before(read.entry.expires) {
		return managementFailure(410, "cursorExpired", "The cursor expired. Start a new query.")
	}
	entry, ok := m.snapshots[read.cursor.View]
	if !ok || entry.principal != read.entry.principal || entry.generation != read.entry.generation {
		return managementFailure(410, "cursorExpired", "The cursor expired. Start a new query.")
	}
	if read.after != "" {
		if read.fresh {
			published := read.entry
			if published.actionOwner != nil {
				view, err := published.actionOwner.Retain()
				if err != nil {
					return management.ErrUnavailable
				}
				published.actions, published.actionOwner = view, &view
			}
			m.snapshots[read.cursor.View] = published
		}
		read.published = true
	}
	// Existing terminal pages retain their immutable view for retry until TTL.
	return nil
}

func (m *managementHTTP) releaseSnapshotRead(read *snapshotRead) {
	if read == nil {
		return
	}
	defer read.entry.snapshotExtensions.release()
	if read.entry.actionOwner != nil {
		defer read.entry.actionOwner.Close()
	}
	if !read.fresh || read.published {
		return
	}
	m.mu.Lock()
	if entry, ok := m.snapshots[read.cursor.View]; ok {
		entry.snapshotExtensions.release()
		m.queueActionClose(entry)
	}
	delete(m.snapshots, read.cursor.View)
	m.unlockSnapshots()
}

func boundSnapshotPage[T any](items []T, after string, id func(T) string) ([]T, string, error) {
	size := 1024
	for i, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, "", management.ErrUnavailable
		}
		if size+len(raw)+1 > managementMaxPageBytes {
			if i == 0 {
				return nil, "", management.ErrUnavailable
			}
			return items[:i], id(items[i-1]), nil
		}
		size += len(raw) + 1
	}
	return items, after, nil
}

// lockSnapshots makes waiting for shared cursor metadata cancelable.
// Callers release it before history I/O, decoding or response serialization.
func (m *managementHTTP) lockSnapshots(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.mu.TryLock() {
			return nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *managementHTTP) pruneSnapshots(now time.Time, principal string, generation uint64) {
	for key, entry := range m.snapshots {
		// An older in-flight request must never delete a newer generation's cursor.
		if !now.Before(entry.expires) || entry.principal == principal && entry.generation < generation {
			entry.snapshotExtensions.release()
			m.queueActionClose(entry)
			delete(m.snapshots, key)
		}
	}
}

func (m *managementHTTP) queueActionClose(entry managementSnapshot) {
	if entry.actionOwner != nil {
		m.actionCloses = append(m.actionCloses, entry.actionOwner)
	}
}

// Releasing a retired generation can close its database. Do that outside the
// cursor lock, including on expiry without any subsequent HTTP traffic.
func (m *managementHTTP) unlockSnapshots() {
	closing := m.actionCloses
	m.actionCloses = nil
	m.mu.Unlock()
	for _, view := range closing {
		_ = view.Close()
	}
}

func (m *managementHTTP) expireSnapshots() {
	m.mu.Lock()
	m.pruneSnapshots(m.now().UTC(), "", 0)
	m.unlockSnapshots()
}

func (m *managementHTTP) closeSnapshots() {
	if m == nil {
		return
	}
	m.mu.Lock()
	for key, entry := range m.snapshots {
		entry.snapshotExtensions.release()
		m.queueActionClose(entry)
		delete(m.snapshots, key)
	}
	m.unlockSnapshots()
}

func (m *managementHTTP) snapshotCountQuota(principal string) error {
	owned := 0
	for _, entry := range m.snapshots {
		if entry.principal == principal {
			owned++
		}
	}
	if len(m.snapshots) >= managementMaxSnapshots || owned >= managementPrincipalSnapshots {
		return managementFailure(429, "snapshotQuota", "Too many cursors are retained. Reuse an existing cursor or wait for expiry.")
	}
	return nil
}
