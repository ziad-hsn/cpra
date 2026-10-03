package httpserver

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const (
	collectionExecutionPageBytes     = 4 << 20
	collectionExecutionSnapshotBytes = 32 << 10
)

type collectionExecutionReadView interface {
	Identity() persistence.CollectionExecutionResultStatus
	Receipt() persistence.CollectionExecutionReceipt
	Page(context.Context, uint64, int, time.Time) (persistence.CollectionExecutionHistoryPage, error)
	Recheck(context.Context, time.Time) error
}

type collectionExecutionRead struct {
	result      api.Operation
	entry       managementSnapshot
	cursor      managementCursor
	reservation *operationSnapshotReservation
	continued   bool
	observed    time.Time
	started     time.Time
}

func (m *managementHTTP) readOperationDetail(ctx context.Context, id string, query url.Values, authority operationReadAuthority) (read collectionExecutionRead, err error) {
	read.started = time.Now()
	read.observed = m.now().UTC()
	limit := 100
	if value := query.Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 500 {
			return read, managementFailure(400, "invalidLimit", "Page limit must be from 1 to 500.")
		}
	}
	if query.Get("cursor") == "" {
		read.result, err = m.catalog.OperationAs(ctx, id, authority.principal, read.observed, authority.collections)
		if err != nil || read.result.ExecutionResult == nil || read.result.ExecutionResult.State != "ready" {
			return read, err
		}
	}
	if !authority.collections {
		return read, managementFailure(403, "forbidden", "Execution results require access to every supported resource kind.")
	}
	if err := m.lockSnapshots(ctx); err != nil {
		return read, err
	}
	var after uint64
	err = func() error {
		defer m.mu.Unlock()
		if !m.cursorReady {
			return management.ErrUnavailable
		}
		now := m.now().UTC()
		if now.Before(read.observed) {
			return management.ErrUnavailable
		}
		m.pruneSnapshots(now, authority.principal, authority.generation)
		if token := query.Get("cursor"); token != "" {
			cursor, err := m.decodeCursor(token)
			if err != nil {
				return err
			}
			entry, ok := m.snapshots[cursor.View]
			if !ok || entry.principal != authority.principal || entry.generation != authority.generation || entry.kind != "CollectionExecution" || entry.execution == nil {
				return managementFailure(410, "cursorExpired", "The execution-result cursor expired or belongs to another access context.")
			}
			if entry.executionOperation.ID != id || query.Get("limit") != "" && limit != entry.limit {
				return managementFailure(400, "cursorMismatch", "Continue with the original operation and page limit.")
			}
			if now.Before(entry.created) {
				return management.ErrUnavailable
			}
			after, err = strconv.ParseUint(cursor.After, 10, 64)
			if err != nil || after == 0 || strconv.FormatUint(after, 10) != cursor.After {
				return managementFailure(400, "invalidCursor", "The execution-result cursor is invalid.")
			}
			read.entry, read.cursor, read.continued = entry, cursor, true
			return nil
		}
		if err := m.snapshotCountQuota(authority.principal); err != nil {
			return err
		}
		if m.operationSnapshotBudget(authority.principal) < collectionExecutionSnapshotBytes {
			return persistence.ErrOperationSnapshotQuota
		}
		m.operationReservations[authority.principal] += collectionExecutionSnapshotBytes
		read.reservation = &operationSnapshotReservation{principal: authority.principal, bytes: collectionExecutionSnapshotBytes}
		return nil
	}()
	if err != nil {
		return read, err
	}
	if !read.continued {
		view, status, err := m.catalog.CollectionExecutionResultView(ctx, id, authority.principal, read.observed)
		if err != nil {
			return read, err
		}
		if view == nil || status.State != "ready" || status.OperationID != read.result.ID || status.ContentDigest != read.result.ContentDigest || status.IdentityFormat != string(read.result.IdentityFormat) || status.NormalizationProfile != read.result.NormalizationProfile || read.result.ItemCount == nil || status.ItemCount != uint64(*read.result.ItemCount) {
			return read, management.ErrUnavailable
		}
		read.result.Items, read.result.NextCursor = nil, ""
		read.cursor.View = uuid.NewString()
		read.entry = managementSnapshot{execution: view, executionOperation: read.result, operationBytes: collectionExecutionSnapshotBytes,
			principal: authority.principal, generation: authority.generation, kind: "CollectionExecution", limit: limit,
			created: read.observed, expires: read.observed.Add(managementCursorTTL)}
		metadata, err := json.Marshal(struct {
			Operation api.Operation
			Identity  persistence.CollectionExecutionResultStatus
			Receipt   persistence.CollectionExecutionReceipt
		}{read.result, view.Identity(), view.Receipt()})
		if err != nil || 2*len(metadata)+4096 > collectionExecutionSnapshotBytes {
			return read, persistence.ErrOperationSnapshotQuota
		}
	}
	page, err := read.entry.execution.Page(ctx, after, read.entry.limit, read.observed)
	if err != nil {
		return read, err
	}
	read.result = read.entry.executionOperation
	availability := management.CollectionExecutionReceiptResult(page.Receipt)
	read.result.ExecutionResult = &availability
	read.result.Items = make([]api.ApplyResult, 0, len(page.Items))
	for _, item := range page.Items {
		read.result.Items = append(read.result.Items, management.CollectionExecutionItemResult(item))
	}
	if availability.Summary == nil {
		return read, management.ErrUnavailable
	}
	if availability.Summary.ExpiresAt.Before(read.entry.expires) {
		read.entry.expires = availability.Summary.ExpiresAt
	}
	if page.NextAfter != 0 {
		read.result.NextCursor = m.encodeCursor(managementCursor{View: read.cursor.View, After: strconv.FormatUint(page.NextAfter, 10)})
	}
	if err := api.ValidateExecutionResult(read.result); err != nil {
		return read, management.ErrUnavailable
	}
	encoded, err := json.Marshal(read.result)
	if err != nil || len(encoded)+1 > collectionExecutionPageBytes {
		return read, managementFailure(503, "pageUnavailable", "The execution-result page exceeds its response bound. Reduce the page limit.")
	}
	return read, ctx.Err()
}

func (m *managementHTTP) publishCollectionExecutionPage(ctx context.Context, read collectionExecutionRead) error {
	if read.entry.execution == nil {
		return ctx.Err()
	}
	if err := m.lockSnapshots(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	now := m.now().UTC()
	if now.Before(read.observed) || now.Before(read.entry.created) {
		return management.ErrUnavailable
	}
	if elapsed := read.observed.Add(time.Since(read.started)); elapsed.After(now) {
		now = elapsed
	}
	if err := read.entry.execution.Recheck(ctx, now); err != nil {
		return err
	}
	current := m.now().UTC()
	if current.Before(read.observed) {
		return management.ErrUnavailable
	}
	if current.After(now) {
		now = current
	}
	if elapsed := read.observed.Add(time.Since(read.started)); elapsed.After(now) {
		now = elapsed
	}
	if !now.Before(read.result.ExecutionResult.Summary.ExpiresAt) {
		return persistence.ErrOperationExpired
	}
	if !now.Before(read.entry.expires) {
		return managementFailure(410, "cursorExpired", "The execution-result cursor expired. Start a new operation query.")
	}
	m.pruneSnapshots(now, read.entry.principal, read.entry.generation)
	if read.continued {
		current, ok := m.snapshots[read.cursor.View]
		if !ok || current.principal != read.entry.principal || current.generation != read.entry.generation || current.kind != "CollectionExecution" || current.executionOperation.ID != read.entry.executionOperation.ID || current.limit != read.entry.limit {
			return managementFailure(410, "cursorExpired", "The execution-result cursor expired. Start a new operation query.")
		}
	} else {
		m.releaseOperationReservationLocked(read.reservation)
		if read.result.NextCursor != "" {
			if err := m.snapshotCountQuota(read.entry.principal); err != nil {
				return err
			}
			if read.entry.operationBytes > m.operationSnapshotBudget(read.entry.principal) {
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
	return nil
}
