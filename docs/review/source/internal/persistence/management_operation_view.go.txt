package persistence

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

// OperationObservation contains exactly one detached receipt. A collection whose
// inactive staging deadline elapsed is projected as expired with zero TerminalAt:
// that is a read observation, never a terminal event or an execution grant.
type OperationObservation struct {
	Operation  *OperationReceipt
	Collection *CollectionReceipt
}

// ManagementOperationView captures ordinary shared observations and collection
// observations owned by one actor at a single logical watermark. It owns no
// transaction, encrypted input, provider client or execution authority.
type ManagementOperationView struct {
	ordinary      OperationView
	actor         string
	at            time.Time
	index         uint64
	collections   []CollectionReceipt
	collectionIDs map[string]struct{}
	bytes         int64
	started       time.Time
	history       *HistoryStore
}

func (v ManagementOperationView) EstimatedBytes() int64 { return v.bytes }

// ManagementOperationPage retains only one bounded page's private metadata for
// final response admission. It is request-local and owns no history transaction.
type ManagementOperationPage struct {
	Rows []OperationObservation
	Next string
	view ManagementOperationView
}

func (v ManagementOperationView) ReadPage(ctx context.Context, monitorID, after string, limit int) (ManagementOperationPage, error) {
	rows, next, err := v.Page(ctx, monitorID, after, limit)
	if err != nil {
		return ManagementOperationPage{}, err
	}
	return ManagementOperationPage{Rows: rows, Next: next, view: v}, nil
}

// Recheck performs metadata checks after policy/cursor waits without history I/O.
// A changed availability fails this response instead of publishing stale DTOs.
func (p ManagementOperationPage) Recheck(ctx context.Context, at time.Time) error {
	started := time.Now()
	if at.IsZero() || at.Before(p.view.at) || len(p.Rows) > 500 {
		return ErrOperationReservation
	}
	if err := p.view.healthy(ctx); err != nil {
		return err
	}
	return p.view.executionObservationsAt(ctx, p.Rows, at.Add(time.Since(started)), true)
}

// collectionOperationObservationFor never changes durable state. In particular,
// an elapsed deadline does not synthesize a terminal timestamp or audit event.
func collectionOperationObservationFor(s CollectionState, at time.Time) CollectionReceipt {
	r := collectionReceiptFor(s)
	r.ExecutionObservation = collectionExecutionObservationFor(s, at)
	if collectionInactive(s.Phase) && (!at.Before(s.ExpiresAt) || s.Validation != nil && !s.Validation.HistoryExpiredAt.IsZero()) {
		r.Phase = "expired"
	}
	if s.Validation == nil || !s.Validation.HistorySealed || !s.Validation.HistoryExpiredAt.IsZero() ||
		!at.Before(s.Validation.FinalizedAt.AddDate(0, 0, 30)) {
		r.Validation = nil
	}
	return r
}

// ManagementOperationSnapshot atomically freezes bounded metadata from both
// families. Empty actor deliberately selects only ordinary shared operations.
func (s *Store) ManagementOperationSnapshot(ctx context.Context, actor string, at time.Time, maxBytes int64) (ManagementOperationView, error) {
	started := time.Now()
	empty := ManagementOperationView{}
	if ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 || actor != "" && !catalogIdentifier(actor, 128) {
		return empty, ErrOperationReservation
	}
	if maxBytes < 1024 {
		return empty, ErrOperationSnapshotQuota
	}
	if err := collectionReadLock(ctx, &s.mu); err != nil {
		return empty, err
	}
	defer s.mu.RUnlock()
	if s.fsm == nil {
		return empty, ErrHistoryUnavailable
	}
	if err := collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return empty, err
	}
	defer s.fsm.mu.RUnlock()
	if err := s.collectionCoordinatorHealth(); err != nil {
		return empty, ErrHistoryUnavailable
	}
	h := s.fsm.history
	if err := collectionReadLock(ctx, &h.mu); err != nil {
		return empty, err
	}
	defer h.mu.RUnlock()
	extra := int64(512)
	owned := 0
	if actor != "" {
		if len(s.fsm.image.Collections) > maxCollectionOperations {
			return empty, ErrHistoryUnavailable
		}
		for id, head := range s.fsm.image.Collections {
			if err := ctx.Err(); err != nil {
				return empty, err
			}
			if head.Actor != actor {
				continue
			}
			if head.ID != id || head.validate() != nil {
				return empty, ErrHistoryUnavailable
			}
			epoch, seq, err := ParseOperationHandle(id)
			if err != nil {
				return empty, ErrHistoryUnavailable
			}
			if epoch != s.fsm.image.OperationEpoch {
				continue
			}
			if seq > s.fsm.image.OperationHighWater {
				return empty, ErrHistoryUnavailable
			}
			if at.Before(head.ActivityAt) || head.Activation != nil && at.Before(head.Activation.At) || !head.TerminalAt.IsZero() && at.Before(head.TerminalAt) ||
				head.Execution != nil && at.Before(head.Execution.LastAt) || head.ExecutionResult != nil && at.Before(head.ExecutionResult.Summary.FinalizedAt) {
				return empty, ErrCollectionConflict
			}
			if !collectionLive(head.Phase) && head.Activation == nil {
				continue
			}
			// Two bounded receipts' worth includes nested metadata, slice/map entries,
			// owned strings and safe projection headroom; no resource body is copied.
			extra += 2*maxCollectionReceiptEventBytes + 256
			owned++
		}
	}
	if extra > maxBytes-512 {
		return empty, ErrOperationSnapshotQuota
	}
	ordinary, err := s.operationSnapshotLocked(ctx, at, maxBytes-extra)
	if err != nil {
		return empty, err
	}
	cutoff := at.UTC().AddDate(0, 0, -30)
	if cutoff.After(ordinary.cutoff) {
		ordinary.cutoff = cutoff
	}
	v := ManagementOperationView{ordinary: ordinary, actor: actor, at: at, started: started, history: h, index: s.fsm.image.Index, bytes: ordinary.bytes + extra,
		collections: make([]CollectionReceipt, 0, owned), collectionIDs: make(map[string]struct{}, owned)}
	if actor != "" {
		for id, head := range s.fsm.image.Collections {
			if err := ctx.Err(); err != nil {
				return empty, err
			}
			if head.Actor != actor || !collectionLive(head.Phase) && head.Activation == nil {
				continue
			}
			epoch, _, _ := ParseOperationHandle(id)
			if epoch != ordinary.epoch {
				continue
			}
			if _, collision := ordinary.liveIDs[id]; collision {
				return empty, ErrHistoryUnavailable
			}
			v.collections = append(v.collections, collectionOperationObservationFor(head, at))
			v.collectionIDs[id] = struct{}{}
		}
	}
	slices.SortFunc(v.collections, func(a, b CollectionReceipt) int { return strings.Compare(a.ID, b.ID) })
	return v, ctx.Err()
}

func (v ManagementOperationView) healthy(ctx context.Context) error {
	if ctx == nil {
		return ErrOperationReservation
	}
	if v.ordinary.store == nil {
		return ErrOperationCursorExpired
	}
	s := v.ordinary.store
	if err := collectionReadLock(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if s.fsm == nil {
		return ErrHistoryUnavailable
	}
	if err := collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return err
	}
	defer s.fsm.mu.RUnlock()
	select {
	case <-s.stop:
		return ErrOperationCursorExpired
	default:
	}
	if s.fsm.image.OperationEpoch != v.ordinary.epoch {
		return ErrOperationCursorExpired
	}
	if s.fsm.image.OperationHighWater < v.ordinary.highWater {
		return ErrOperationCursorExpired
	}
	if s.fsm.image.Index < v.index || s.fsm.history != v.history {
		return ErrHistoryUnavailable
	}
	if err := s.collectionCoordinatorHealth(); err != nil {
		return ErrHistoryUnavailable
	}
	return ctx.Err()
}

// Page traverses ordinary phases, frozen owned collections, then retained
// collection history, filling successive exhausted phases in the same page.
// Empty pages carry a cursor only when bounded inspection exhausts its budget.
// Exact monitor selection excludes collection-wide receipts. Calls never decrypt.
func (v ManagementOperationView) Page(ctx context.Context, monitorID, after string, limit int) ([]OperationObservation, string, error) {
	if ctx == nil || limit < 1 || limit > 500 || monitorID != "" && (!catalogIdentifier(monitorID, 256) || strings.ContainsRune(monitorID, 0)) {
		return nil, "", ErrOperationReservation
	}
	if err := v.healthy(ctx); err != nil {
		return nil, "", err
	}
	phase, position := "o", ""
	if after != "" {
		if len(after) > 260 || len(after) < 2 || after[1] != ':' {
			return nil, "", ErrOperationReservation
		}
		phase, position = after[:1], after[2:]
		switch phase {
		case "o":
			if len(position) < 2 || position[1] != ':' || position[:1] != "l" && position[:1] != "h" {
				return nil, "", ErrOperationReservation
			}
			value := position[2:]
			if position[:1] == "h" && value != "" && !validHistoryPosition(value) || position[:1] == "l" && !catalogIdentifier(value, 256) {
				return nil, "", ErrOperationReservation
			}
		case "l", "c":
			if v.actor == "" || monitorID != "" {
				return nil, "", ErrOperationReservation
			}
			if position != "" {
				epoch, seq, err := ParseOperationHandle(position)
				if err != nil || epoch != v.ordinary.epoch || seq > v.ordinary.highWater {
					return nil, "", ErrOperationReservation
				}
			}
		default:
			return nil, "", ErrOperationReservation
		}
	}
	h := v.history
	if err := collectionReadLock(ctx, &h.mu); err != nil {
		return nil, "", err
	}
	rows, next, err := v.pageLocked(ctx, h, monitorID, phase, position, limit)
	h.mu.RUnlock()
	if err != nil {
		return nil, "", err
	}
	if err := v.healthy(ctx); err != nil {
		return nil, "", err
	}
	if err := v.executionObservationsCurrent(ctx, rows); err != nil {
		return nil, "", err
	}
	return rows, next, nil
}

func (v ManagementOperationView) pageLocked(ctx context.Context, h *HistoryStore, monitorID, phase, position string, limit int) ([]OperationObservation, string, error) {
	if h.closed {
		return nil, "", ErrOperationCursorExpired
	}
	if h.err != nil || h.catalog.Index < v.index {
		return nil, "", ErrHistoryUnavailable
	}
	if h.retentionGeneration != v.ordinary.retentionGeneration || h.operationGeneration != v.ordinary.operationGeneration {
		return nil, "", ErrOperationCursorExpired
	}
	if err := v.checkSegments(h); err != nil {
		return nil, "", err
	}
	rows := make([]OperationObservation, 0, limit)
	budget := &operationReadBudget{remaining: maxOperationInspectedKeys}
	if phase == "o" {
		ordinaryPhase, ordinaryPosition := "l", ""
		if position != "" {
			ordinaryPhase, ordinaryPosition = position[:1], position[2:]
		}
		receipts, next, err := v.ordinary.pageLockedBudget(ctx, h, monitorID, ordinaryPhase, ordinaryPosition, limit, budget)
		if err != nil {
			return nil, "", err
		}
		for _, receipt := range receipts {
			r := receipt
			rows = append(rows, OperationObservation{Operation: &r})
		}
		if next != "" {
			return rows, "o:" + next, nil
		}
		if v.actor == "" || monitorID != "" {
			return rows, "", nil
		}
		phase, position = "l", ""
	}
	if phase == "l" {
		frontier := position
		for _, receipt := range v.collections {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
			if receipt.ID <= position {
				continue
			}
			cost := 1
			if receipt.Phase == "validated" || receipt.Phase == "rejected" || receipt.ExecutionObservation != nil && receipt.ExecutionObservation.Summary != nil {
				// Summary verification checks the current retained catalog, including
				// segments appended after this observation snapshot was captured.
				if len(h.catalog.Segments) > maxOperationSegments {
					return nil, "", ErrHistoryUnavailable
				}
				cost += 8 * (len(h.catalog.Segments) + 1)
			}
			if len(rows) == limit || !budget.take(cost) {
				return rows, "l:" + frontier, nil
			}
			if receipt.Phase == "validated" || receipt.Phase == "rejected" {
				if receipt.Validation == nil {
					return nil, "", ErrHistoryUnavailable
				}
				if _, err := h.collectionValidationPageLocked(ctx, *receipt.Validation, v.index, receipt.Validation.Descriptor.Count, 1, v.at); err != nil {
					if errors.Is(err, ErrOperationExpired) {
						return nil, "", ErrOperationCursorExpired
					}
					return nil, "", err
				}
			}
			r := receipt.Clone()
			if err := h.verifyCollectionExecutionObservationLocked(ctx, r.ExecutionObservation, v.index, v.at.Add(time.Since(v.started))); err != nil {
				return nil, "", err
			}
			frontier = r.ID
			rows = append(rows, OperationObservation{Collection: &r})
		}
		position = ""
	}
	retained, next, err := v.collectionHistoryPageBudget(ctx, h, position, limit-len(rows), budget)
	if err != nil {
		return nil, "", err
	}
	return append(rows, retained...), next, nil
}
