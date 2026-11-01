package persistence

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

const notificationConditionLimit = 100
const notificationReviewBytes = 1 << 20

// ErrNotificationObservationChanged is transient read contention. It never
// changes storage health, admission, or the underlying notification condition.
var ErrNotificationObservationChanged = errors.New("monitor changed while reading notification conditions")

// NotificationConditionView combines bounded current conditions with one
// current ledger transaction. Sequence and ObservedAt describe that read; they
// are not a historical snapshot token. Identity fields fence a caller's separate
// monitor projection before it attaches these conditions to a detail response.
type NotificationConditionView struct {
	Items                                         []NotificationConditionObservation
	Truncated                                     bool
	ObservedAt                                    time.Time
	Sequence, Generation                          uint64
	IncarnationID, Revision, CatalogRevision      string
	ControlRevision, IncidentID, IncidentRevision string
}

type notificationObservationFence struct {
	incarnation, revision, catalogRevision, controlRevision string
	incident, incidentRevision, adopted                     string
	generation, sequence                                    uint64
	removed                                                 bool
	notifications                                           *NotificationState
	projection                                              *ActionProjection
}

func notificationFence(m Monitor) notificationObservationFence {
	f := notificationObservationFence{incarnation: m.IncarnationID(), revision: m.Revision, catalogRevision: m.CatalogRevision,
		controlRevision: m.ControlRevision, incident: m.IncidentID, incidentRevision: m.IncidentRevision,
		generation: m.Generation, sequence: m.Sequence, removed: m.Removed, notifications: m.Notifications, projection: m.ActionProjection}
	if m.AdoptedFileIncarnation != nil {
		f.adopted = *m.AdoptedFileIncarnation
	}
	return f
}

// pinCurrentObservation retains only generation ownership under the FSM lock.
// It performs no database read. The pin's sequence protects action versions
// from retirement but must never be used to version the mutable review rows.
func (l *collectionLedger) pinCurrentObservation(sequence uint64) (*actionLedgerPin, error) {
	l.pinsMu.Lock()
	defer l.pinsMu.Unlock()
	if l.closing || l.closedNative.Load() {
		return nil, errCollectionLedgerClosed
	}
	l.viewPins.Add(1)
	view := &actionLedgerPin{ledger: l, sequence: sequence}
	if l.actionPins == nil {
		l.actionPins = make(map[*actionLedgerPin]struct{})
	}
	l.actionPins[view] = struct{}{}
	return view, nil
}

// NotificationConditionsContext is a detail-only observation. It visits at
// most 101 current-execution entries and 101 relevant review-index entries,
// returning at most 100 conditions and decoding at most 1 MiB in one View.
// All database I/O occurs outside Store/FSM locks. Fleet/list callers use the
// in-memory Monitor.NotificationConditions projection instead.
func (s *Store) NotificationConditionsContext(ctx context.Context, id string) (NotificationConditionView, bool, error) {
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return NotificationConditionView{}, false, err
	}
	f := s.fsm
	m, found := f.image.Monitors[id]
	if !found || m.Removed {
		unlock()
		return NotificationConditionView{}, false, ctx.Err()
	}
	fence := notificationFence(m)
	items, truncated := m.notificationConditions()
	active, activeTruncated, err := f.notificationReviewExecutions(ctx, m)
	if err != nil {
		unlock()
		return NotificationConditionView{}, false, err
	}
	view := NotificationConditionView{Items: items, Truncated: truncated, Generation: m.Generation,
		IncarnationID: m.IncarnationID(), Revision: m.Revision, CatalogRevision: m.CatalogRevision,
		ControlRevision: m.ControlRevision, IncidentID: m.IncidentID, IncidentRevision: m.IncidentRevision}
	view.Truncated = view.Truncated || activeTruncated
	owner := Monitor{ID: m.ID, Revision: m.Revision, CatalogUID: m.CatalogUID, FileIncarnation: m.FileIncarnation}
	if m.AdoptedFileIncarnation != nil {
		adopted := *m.AdoptedFileIncarnation
		owner.AdoptedFileIncarnation = &adopted
	}
	ledger := f.collections
	var pin *actionLedgerPin
	if ledger != nil {
		var sequence uint64
		if f.image.ActionStorage != nil {
			sequence = f.image.ActionStorage.Sequence
		}
		pin, err = ledger.pinCurrentObservation(sequence)
	} else if m.ActionProjection != nil && m.ActionProjection.ReviewConditions != 0 {
		err = errActionLedger
	}
	unlock()
	if err != nil {
		return NotificationConditionView{}, false, err
	}
	if pin != nil {
		defer pin.Close()
		reviews, e := pin.currentReviewConditions(ctx, owner, active)
		if e != nil {
			return NotificationConditionView{}, false, e
		}
		view.Items = append(view.Items, reviews.Items...)
		view.Truncated = view.Truncated || reviews.Truncated
		view.ObservedAt, view.Sequence = reviews.ObservedAt, reviews.Sequence
	} else {
		view.ObservedAt = time.Now().UTC()
	}
	// Capture/recheck is deliberately per monitor. Other monitors may commit
	// while this read runs without forcing retries or starving the detail page.
	unlock, err = s.lockControllerState(ctx, false)
	if err != nil {
		return NotificationConditionView{}, false, err
	}
	current, found := s.fsm.image.Monitors[id]
	unchanged := s.fsm == f && s.fsm.collections == ledger && found && notificationFence(current) == fence
	unlock()
	if !unchanged {
		return NotificationConditionView{}, false, ErrNotificationObservationChanged
	}
	if err := ctx.Err(); err != nil {
		return NotificationConditionView{}, false, err
	}
	slices.SortFunc(view.Items, func(a, b NotificationConditionObservation) int { return strings.Compare(a.ConditionID, b.ConditionID) })
	if len(view.Items) > notificationConditionLimit {
		view.Items = view.Items[:notificationConditionLimit]
		view.Truncated = true
	}
	return view, true, nil
}

// A closure with a committed first start no longer needs routine assessment,
// but a proven no-effect rejection can leave an executable retry. Capture its
// identity from the existing active index instead of expanding the assessment
// index to completed history. The caller holds FSM.mu; no payload is copied.
func (f *machine) notificationReviewExecutions(ctx context.Context, m Monitor) (ids []string, truncated bool, err error) {
	if f.actionsByMonitor == nil {
		return nil, len(m.Actions) != 0, ctx.Err()
	}
	prefix := m.ID + "\x00"
	visited := 0
	seen := make(map[string]struct{})
	f.actionsByMonitor.AscendGreaterOrEqual(actionItem{key: prefix}, func(item actionItem) bool {
		if !strings.HasPrefix(item.key, prefix) {
			return false
		}
		if err = ctx.Err(); err != nil {
			return false
		}
		if visited == notificationConditionLimit {
			truncated = true
			return false
		}
		visited++
		a := item.action
		if a.ReviewNotice != nil && a.ReviewNotice.Phase == "close" && a.Kind == "code" && a.Color == "blue" && (a.State == Queued || a.State == Started) {
			if _, duplicate := seen[a.ReviewNotice.AlertID]; !duplicate {
				seen[a.ReviewNotice.AlertID] = struct{}{}
				ids = append(ids, a.ReviewNotice.AlertID)
			}
		}
		return true
	})
	slices.Sort(ids)
	return ids, truncated, err
}

func (v *actionLedgerPin) currentReviewConditions(ctx context.Context, owner Monitor, active []string) (result NotificationConditionView, err error) {
	if len(active) > notificationConditionLimit {
		return result, errActionLedger
	}
	err = v.read(ctx, func(tx *bolt.Tx, memory *actionLedgerMemory) error {
		var progress actionLedgerProgress
		if tx != nil {
			count := actionBucketCount(tx)
			if count == 0 && v.sequence == 0 && len(active) == 0 {
				result.ObservedAt = time.Now().UTC()
				return nil
			}
			if count != len(actionBuckets()) {
				return errActionLedger
			}
			var e error
			progress, e = actionDiskProgress(tx)
			if e != nil {
				return e
			}
		} else if memory != nil {
			progress = memory.progress
		}
		if progress.Sequence < v.sequence {
			return errActionLedger
		}
		result.Sequence, result.ObservedAt = progress.Sequence, time.Now().UTC()
		prefix := "monitor/" + identity(owner.ID) + "/"
		decoded, visited := 0, 0
		seen := make(map[string]struct{})
		get := func(bucket []byte, key string) []byte {
			if tx != nil {
				return tx.Bucket(bucket).Get([]byte(key))
			}
			if memory == nil {
				return nil
			}
			if string(bucket) == string(reviewRowsBucket) {
				return memory.reviews[key]
			}
			return memory.current[key]
		}
		consume := func(data []byte) bool {
			if len(data) > notificationReviewBytes-decoded {
				result.Truncated = true
				return false
			}
			decoded += len(data)
			return true
		}
		read := func(key string, indexed bool) (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if _, duplicate := seen[key]; duplicate {
				return true, nil
			}
			if len(result.Items) == notificationConditionLimit {
				result.Truncated = true
				return false, nil
			}
			seen[key] = struct{}{}
			data := get(reviewRowsBucket, key)
			if !consume(data) {
				return false, nil
			}
			r, e := decodeReviewRecord(data)
			if e != nil || r.Key != key || r.MonitorID != owner.ID || r.Sequence > progress.Sequence || r.Alert == nil ||
				indexed && !slices.Contains(reviewConditionKeys(r), prefix+key) {
				return false, errActionLedger
			}
			alert := *r.Alert
			phase, delivery := "open", alert.OpeningDelivery
			if !alert.ClosedAt.IsZero() {
				phase, delivery = "close", alert.ClosingDelivery
			}
			var action Action
			found := false
			if delivery != "" {
				for attempt := 3; attempt >= 1; attempt-- {
					if err := ctx.Err(); err != nil {
						return false, err
					}
					id := delivery
					if attempt > 1 {
						id = identity(fmt.Sprintf("%s/attempt/%d", delivery, attempt))
					}
					data := get(actionCurrentBucket, id)
					if data == nil {
						continue
					}
					if !consume(data) {
						return false, nil
					}
					row, e := decodeActionRow(data)
					if e != nil || row.Action.ID != id || row.Sequence > progress.Sequence || row.MonitorID != owner.ID || row.Deleted {
						return false, errActionLedger
					}
					action, found = row.Action, true
					break
				}
				if !found || !reviewObservationDelivery(alert, action, phase, delivery) {
					return false, errActionLedger
				}
			}
			result.Items = append(result.Items, reviewConditionObservation(owner, alert, action, found, phase))
			return true, nil
		}
		for _, id := range active {
			more, e := read(reviewAlertKey(owner.ID, id), false)
			if e != nil || !more {
				return e
			}
		}
		visit := func(index, key string) (bool, error) {
			if !strings.HasPrefix(index, prefix) {
				return false, nil
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if visited == notificationConditionLimit {
				result.Truncated = true
				return false, nil
			}
			visited++
			if prefix+key != index {
				return false, errActionLedger
			}
			return read(key, true)
		}
		if tx != nil {
			c := tx.Bucket(reviewConditionBucket).Cursor()
			for key, value := c.Seek([]byte(prefix)); key != nil; key, value = c.Next() {
				more, e := visit(string(key), string(value))
				if e != nil || !more {
					return e
				}
			}
		} else if memory != nil {
			var e error
			memory.reviewConditions.AscendGreaterOrEqual(prefix, func(key string) bool {
				more, visitErr := visit(key, strings.TrimPrefix(key, prefix))
				e = visitErr
				return more && e == nil
			})
			return e
		}
		return nil
	})
	return result, err
}

func reviewObservationDelivery(alert ReviewAlert, a Action, phase, delivery string) bool {
	n := a.ReviewNotice
	return n != nil && a.Kind == "code" && a.Color == "blue" && n.AlertID == alert.ID && n.ActionID == alert.ActionID && n.Phase == phase &&
		a.DeliveryID == delivery && a.Revision == alert.Revision && a.IncidentID == alert.IncidentID && a.EndpointID == alert.EndpointID && a.Endpoint == alert.Endpoint &&
		a.NotificationEpisode == alert.ID && a.NotificationCorrelation == alert.ID && a.ProviderCorrelationVersion == alert.ProviderCorrelationVersion &&
		(alert.Owner == nil || a.CatalogUID == alert.Owner.CatalogUID && a.FileIncarnation == alert.Owner.FileIncarnation)
}

func reviewConditionObservation(owner Monitor, alert ReviewAlert, delivery Action, found bool, phase string) NotificationConditionObservation {
	v := NotificationConditionObservation{ConditionID: alert.ID + "/" + phase, Kind: "review_opening", Color: "blue", State: "pending",
		OriginalActionID: alert.ActionID, PendingAt: alert.OpenedAt.UTC(), RecoveredAt: alert.RecoveredAt.UTC()}
	if phase == "close" {
		v.Kind, v.PendingAt, v.Reason = "review_closure", alert.ClosedAt.UTC(), alert.CloseReason
	}
	if found {
		v.State = string(delivery.State)
	}
	if phase == "close" && alert.ClosureSuppressed && (!found || delivery.State == Queued || delivery.State == Cancelled) {
		v.State, v.Reason = "suppressed", "suppressed"
	}
	if alert.ClosureBlocked != "" {
		v.State, v.Reason = "blocked", alert.ClosureBlocked
	}
	if alert.Revision != owner.Revision && !(phase == "close" && alert.OpeningStarted && owner.reviewAlertAuthorized(alert)) {
		v.State, v.Reason = "blocked", "configuration_changed"
	}
	if alert.Owner != nil && !owner.OwnsAction(Action{CatalogUID: alert.Owner.CatalogUID, FileIncarnation: alert.Owner.FileIncarnation}) {
		v.State, v.Reason = "blocked", "incarnation_changed"
	}
	return v
}
