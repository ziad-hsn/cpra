package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const (
	managementOperationSnapshotBytes  = 64 << 20
	managementPrincipalOperationBytes = 16 << 20
)

// A retained view is immutable. Its bounded Page read owns no HTTP authority,
// policy lock or cursor lock across disk access.
type managementOperationReadView interface {
	EstimatedBytes() int64
	Page(context.Context, string, string, int) ([]api.Operation, string, error)
	ReadPage(context.Context, string, string, int) (management.ManagementOperationPage, error)
}

type operationReadAuthority struct {
	principal   string
	generation  uint64
	collections bool
}

func operationAuthority(access api.AccessInfo, generation uint64) operationReadAuthority {
	return operationReadAuthority{principal: access.PrincipalID, generation: generation,
		collections: collectionValidationPermissions(access, false) == nil}
}

func (m *managementHTTP) handleOperations(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var authority operationReadAuthority
	err := m.auth.WithPolicyAdmission(r, "ListOperations", func(access api.AccessInfo, generation uint64) error {
		authority = operationAuthority(access, generation)
		return ctx.Err()
	})
	if err != nil {
		writeManagementError(w, err)
		return
	}
	query, err := operationListQuery(r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	select {
	case m.operationReads <- struct{}{}:
		defer func() { <-m.operationReads }()
	default:
		w.Header().Set("Retry-After", "1")
		writeManagementError(w, managementFailure(429, "readBusy", "Operation read capacity is busy."))
		return
	}
	read, err := m.readOperations(ctx, query, authority)
	defer m.releaseOperationReservation(read.reservation)
	defer m.releaseCursorRead(read.cursor.View, read.entry.activity)
	permissionErr := m.auth.WithPolicyAdmission(r, "ListOperations", func(access api.AccessInfo, generation uint64) error {
		if operationAuthority(access, generation) != authority {
			return managementFailure(410, "cursorExpired", "Authorization changed. Start a new operation query.")
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if err != nil {
			return nil
		}
		return m.publishOperationPage(ctx, read)
	})
	if permissionErr != nil {
		err = permissionErr
	}
	if err != nil {
		switch {
		case errors.Is(err, persistence.ErrOperationSnapshotQuota):
			w.Header().Set("Retry-After", "5")
			err = managementFailure(429, "snapshotByteQuota", "Operation snapshots exceed the retained byte budget. Reuse an existing cursor or wait for expiry.")
		case errors.Is(err, persistence.ErrOperationCursorExpired):
			err = managementFailure(410, "cursorExpired", "The retained operation history changed. Start a new operation query.")
		}
		writeManagementError(w, err)
		return
	}
	writeManagementJSON(w, http.StatusOK, read.result)
}

// Detail reads share ordinary receipt visibility, but a collection belongs to
// its original actor. Current HTTP authority is observed before and after I/O.
func (m *managementHTTP) handleOperation(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var authority operationReadAuthority
	err := m.auth.WithPolicyAdmission(r, "GetOperation", func(access api.AccessInfo, generation uint64) error {
		authority = operationAuthority(access, generation)
		return ctx.Err()
	})
	if err != nil {
		writeManagementError(w, err)
		return
	}
	query, err := operationListQuery(r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	if query.Get("monitorID") != "" {
		writeManagementError(w, managementFailure(400, "invalidQuery", "An operation detail read accepts no monitor filter."))
		return
	}
	if limit := query.Get("limit"); limit != "" {
		n, parseErr := strconv.Atoi(limit)
		if parseErr != nil || n < 1 || n > 500 {
			writeManagementError(w, managementFailure(400, "invalidLimit", "Page limit must be from 1 to 500."))
			return
		}
	}
	select {
	case m.operationReads <- struct{}{}:
		defer func() { <-m.operationReads }()
	default:
		w.Header().Set("Retry-After", "1")
		writeManagementError(w, managementFailure(429, "readBusy", "Operation read capacity is busy."))
		return
	}
	read, err := m.readOperationDetail(ctx, r.PathValue("id"), query, authority)
	defer m.releaseOperationReservation(read.reservation)
	defer m.releaseCursorRead(read.cursor.View, read.entry.activity)
	permissionErr := m.auth.WithPolicyAdmission(r, "GetOperation", func(access api.AccessInfo, generation uint64) error {
		if operationAuthority(access, generation) != authority {
			return managementFailure(410, "cursorExpired", "Authorization changed. Read the operation again.")
		}
		if current := m.now().UTC(); current.Before(read.observed) {
			return management.ErrUnavailable
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if err != nil {
			return nil
		}
		return m.publishCollectionExecutionPage(ctx, read)
	})
	if permissionErr != nil {
		err = permissionErr
	}
	if err != nil {
		if errors.Is(err, persistence.ErrOperationSnapshotQuota) {
			w.Header().Set("Retry-After", "5")
			err = managementFailure(429, "snapshotByteQuota", "Operation snapshots exceed the retained byte budget. Reuse an existing cursor or wait for expiry.")
		}
		writeManagementError(w, err)
		return
	}
	if read.result.ExecutionResult != nil && read.result.ExecutionResult.State == "pending" {
		w.Header().Set("Retry-After", "5")
	}
	w.Header().Set("X-Operation-ID", read.result.ID)
	writeManagementJSON(w, http.StatusOK, read.result)
}

func operationListQuery(r *http.Request) (url.Values, error) {
	if len(r.URL.RawQuery) > 4096 {
		return nil, managementFailure(400, "invalidQuery", "The query is too large.")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, managementFailure(400, "invalidQuery", "The query is invalid.")
	}
	for key, values := range query {
		if len(values) != 1 {
			return nil, managementFailure(400, "invalidQuery", "Duplicate query parameters are not accepted.")
		}
		switch key {
		case "cursor", "limit", "monitorID":
		case "selector":
			if values[0] != "" {
				return nil, managementFailure(501, "featureUnavailable", "Operation label filtering is not enabled. Use monitorID for exact monitor selection.")
			}
		default:
			return nil, managementFailure(400, "invalidQuery", "The query contains an unsupported parameter.")
		}
	}
	return query, nil
}

// operationSnapshotBudget accounts for retained copies and in-flight capture
// reservations. Callers hold m.mu; reservations are charged before allocation.
func (m *managementHTTP) operationSnapshotBudget(principal string) int64 {
	total, owned := m.operationSnapshotBudgets(principal)
	return min(total, owned)
}

func (m *managementHTTP) operationSnapshotBudgets(principal string) (int64, int64) {
	var total, owned int64
	for _, entry := range m.snapshots {
		if entry.operationBytes < 0 || entry.operationBytes > managementOperationSnapshotBytes {
			return 0, 0
		}
		total += entry.operationBytes
		if entry.principal == principal {
			owned += entry.operationBytes
		}
	}
	for actor, reserved := range m.operationReservations {
		if reserved < 0 || reserved > managementPrincipalOperationBytes {
			return 0, 0
		}
		total += reserved
		if actor == principal {
			owned += reserved
		}
	}
	return max(0, int64(managementOperationSnapshotBytes)-total), max(0, int64(managementPrincipalOperationBytes)-owned)
}

// Reclaim only when the requested charge cannot fit. Completed and idle views
// can free bytes; active captures and readers keep their reservations.
func (m *managementHTTP) availableOperationSnapshotBudget(principal string, minimum int64) int64 {
	for {
		total, owned := m.operationSnapshotBudgets(principal)
		budget := min(total, owned)
		if budget >= minimum {
			return budget
		}
		owner := ""
		if owned < minimum {
			owner = principal
		}
		if !m.reclaimSnapshotMatching(owner, m.now().UTC(), func(entry managementSnapshot) bool { return entry.operationBytes > 0 }) {
			return budget
		}
	}
}

type operationSnapshotReservation struct {
	principal string
	bytes     int64
	released  bool // protected by m.mu
}

func (m *managementHTTP) releaseOperationReservation(reservation *operationSnapshotReservation) {
	if reservation == nil {
		return
	}
	m.mu.Lock()
	defer m.unlockSnapshots()
	m.releaseOperationReservationLocked(reservation)
}
func (m *managementHTTP) releaseOperationReservationLocked(reservation *operationSnapshotReservation) {
	if reservation == nil || reservation.released {
		return
	}
	m.operationReservations[reservation.principal] -= reservation.bytes
	if m.operationReservations[reservation.principal] == 0 {
		delete(m.operationReservations, reservation.principal)
	}
	reservation.released = true
}

// A capture reports quota pressure only after measuring the actual immutable
// view. Retry that read after reclaiming completed/inactive views; a positive
// but insufficient remaining byte budget must not wedge later browsing.
func (m *managementHTTP) growOperationReservation(ctx context.Context, reservation *operationSnapshotReservation) (bool, error) {
	if reservation == nil || reservation.bytes >= managementPrincipalOperationBytes {
		return false, nil
	}
	if err := m.lockSnapshots(ctx); err != nil {
		return false, err
	}
	defer m.unlockSnapshots()
	previous := reservation.bytes
	m.releaseOperationReservationLocked(reservation)
	budget := m.availableOperationSnapshotBudget(reservation.principal, previous+1)
	if budget <= previous {
		return false, nil
	}
	m.operationReservations[reservation.principal] += budget
	reservation.bytes, reservation.released = budget, false
	return true, nil
}

type operationPageRead struct {
	result      api.OperationList
	entry       managementSnapshot
	cursor      managementCursor
	reservation *operationSnapshotReservation
	continued   bool
	observed    time.Time
	started     time.Time
	page        management.ManagementOperationPage
}

func (m *managementHTTP) readOperations(ctx context.Context, query url.Values, authority operationReadAuthority) (read operationPageRead, err error) {
	read.started = time.Now()
	// Release the capture reservation on every failed construction/read. The
	// successful caller owns it until publication or cancellation.
	handedOff := false
	defer func() {
		if !handedOff {
			m.releaseOperationReservation(read.reservation)
		}
	}()
	limit := 0
	if value := query.Get("limit"); value != "" {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed < 1 || parsed > 500 {
			return read, managementFailure(400, "invalidLimit", "Page limit must be from 1 to 500.")
		}
		limit = parsed
	}
	if err = m.lockSnapshots(ctx); err != nil {
		return read, err
	}
	err = func() error {
		defer m.unlockSnapshots()
		if !m.cursorReady {
			return management.ErrUnavailable
		}
		read.observed = m.now().UTC()
		read.started = time.Now()
		m.pruneSnapshots(read.observed, authority.principal, authority.generation)
		if token := query.Get("cursor"); token != "" {
			cursor, decodeErr := m.decodeCursor(token)
			if decodeErr != nil {
				return decodeErr
			}
			entry, ok := m.snapshots[cursor.View]
			if !ok || cursorExpired(entry) || entry.operations == nil || entry.principal != authority.principal || entry.generation != authority.generation || entry.kind != "Operation" {
				return managementFailure(410, "cursorExpired", "The operation cursor expired or belongs to another access context.")
			}
			if (limit != 0 && limit != entry.limit) || query.Get("monitorID") != entry.monitorID {
				return managementFailure(400, "cursorMismatch", "Continue with the original page limit and monitor selection.")
			}
			if read.observed.Before(entry.created) {
				return management.ErrUnavailable
			}
			read.entry, read.cursor, read.continued = entry, cursor, true
			read.entry.activity = m.retainCursorRead(cursor.View, entry, read.observed)
			return nil
		}
		budget := m.availableOperationSnapshotBudget(authority.principal, 1)
		if budget <= 0 {
			return persistence.ErrOperationSnapshotQuota
		}
		if m.operationReservations == nil {
			m.operationReservations = make(map[string]int64)
		}
		m.operationReservations[authority.principal] += budget
		read.reservation = &operationSnapshotReservation{principal: authority.principal, bytes: budget}
		return nil
	}()
	if err != nil {
		return read, err
	}
	if !read.continued {
		if limit == 0 {
			limit = 100
		}
		actor := ""
		if authority.collections {
			actor = authority.principal
		}
		view, captureErr := m.catalog.ManagementOperationSnapshot(ctx, actor, read.observed, read.reservation.bytes)
		for attempt := 0; errors.Is(captureErr, persistence.ErrOperationSnapshotQuota) && attempt < managementMaxSnapshots; attempt++ {
			grown, growErr := m.growOperationReservation(ctx, read.reservation)
			if growErr != nil {
				return read, growErr
			}
			if !grown {
				break
			}
			view, captureErr = m.catalog.ManagementOperationSnapshot(ctx, actor, read.observed, read.reservation.bytes)
		}
		if captureErr != nil {
			return read, captureErr
		}
		cost := view.EstimatedBytes()
		if cost < 0 || cost > read.reservation.bytes {
			return read, persistence.ErrOperationSnapshotQuota
		}
		read.cursor.View = uuid.NewString()
		read.entry = managementSnapshot{activity: &cursorActivity{readers: 1, lastUsed: read.observed}, operations: view, operationBytes: cost, monitorID: query.Get("monitorID"),
			principal: authority.principal, generation: authority.generation, kind: "Operation", limit: limit,
			created: read.observed, expires: read.observed.Add(managementCursorTTL)}
	}
	page, pageErr := read.entry.operations.ReadPage(ctx, read.entry.monitorID, read.cursor.After, read.entry.limit)
	if pageErr != nil {
		return read, pageErr
	}
	read.page = page
	after := page.Next
	read.result = api.OperationList{Items: page.Items, GeneratedAt: read.entry.created, Snapshot: read.cursor.View}
	if after != "" {
		if after == read.cursor.After {
			return read, management.ErrUnavailable
		}
		read.result.NextCursor = m.encodeCursor(managementCursor{View: read.cursor.View, After: after})
	}
	encoded, encodeErr := json.Marshal(read.result)
	if encodeErr != nil || len(encoded)+1 > managementMaxPageBytes {
		return read, managementFailure(503, "pageUnavailable", "The operation page exceeds its supported response bound. Reduce the page limit.")
	}
	if err = ctx.Err(); err != nil {
		return read, err
	}
	handedOff = true
	return read, nil
}

// Publication is a short current-policy callback. A revoked or expired detached
// read can never publish a new cursor, and quotas are checked again after I/O.
func (m *managementHTTP) publishOperationPage(ctx context.Context, read operationPageRead) error {
	if err := m.lockSnapshots(ctx); err != nil {
		return err
	}
	defer m.unlockSnapshots()
	now := m.now().UTC()
	if now.Before(read.observed) || now.Before(read.entry.created) {
		return management.ErrUnavailable
	}
	if elapsed := read.observed.Add(time.Since(read.started)); elapsed.After(now) {
		now = elapsed
	}
	if err := read.page.Recheck(ctx, now); err != nil {
		return err
	}
	// Metadata locks may wait too. Account for that elapsed time before exposing
	// the page or a cursor; no history read runs under these admission locks.
	if current := m.now().UTC(); current.Before(read.observed) || current.Before(read.entry.created) {
		return management.ErrUnavailable
	} else if current.After(now) {
		now = current
	}
	if elapsed := read.observed.Add(time.Since(read.started)); elapsed.After(now) {
		now = elapsed
	}
	if !now.Before(read.entry.expires) {
		return persistence.ErrOperationCursorExpired
	}
	m.pruneSnapshots(now, read.entry.principal, read.entry.generation)
	if read.continued {
		current, ok := m.snapshots[read.cursor.View]
		if !ok || cursorExpired(current) || current.principal != read.entry.principal || current.generation != read.entry.generation || current.kind != "Operation" || current.limit != read.entry.limit || current.monitorID != read.entry.monitorID {
			return persistence.ErrOperationCursorExpired
		}
	} else {
		m.releaseOperationReservationLocked(read.reservation)
		if read.result.NextCursor != "" {
			if err := m.snapshotCountQuota(read.entry.principal); err != nil {
				return err
			}
			if read.entry.operationBytes > m.availableOperationSnapshotBudget(read.entry.principal, read.entry.operationBytes) {
				return persistence.ErrOperationSnapshotQuota
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if read.result.NextCursor != "" {
		m.snapshots[read.cursor.View] = read.entry
	}
	m.recordCursorPage(read.entry.activity, now, read.result.NextCursor == "")
	return nil
}
