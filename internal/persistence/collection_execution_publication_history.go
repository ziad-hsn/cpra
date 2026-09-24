package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// CollectionExecutionHistory carries exactly one item or the final summary.
// Source paths, resource bodies, credentials and arbitrary diagnostics cannot
// be represented. Event IDs still use the committed log position and ordinal.
type CollectionExecutionHistory struct {
	OperationID string                      `json:"operation_id"`
	ResultID    string                      `json:"result_id"`
	FinalizedAt time.Time                   `json:"finalized_at"`
	Item        *CollectionExecutionItem    `json:"item,omitempty"`
	Seal        *CollectionExecutionReceipt `json:"seal,omitempty"`
}

func (v CollectionExecutionHistory) clone() CollectionExecutionHistory {
	if v.Item != nil {
		x := v.Item.Clone()
		v.Item = &x
	}
	if v.Seal != nil {
		x := v.Seal.Clone()
		v.Seal = &x
	}
	return v
}

func (v CollectionExecutionHistory) validate() error {
	if _, _, err := ParseOperationHandle(v.OperationID); err != nil || !validOperationEpoch(v.ResultID) || v.FinalizedAt.IsZero() || (v.Item == nil) == (v.Seal == nil) {
		return ErrHistoryUnavailable
	}
	if v.Item != nil {
		if _, err := collectionExecutionItemEncoding(*v.Item); err != nil || v.Item.Binding.OperationID != v.OperationID || v.Item.Binding.ActivationID != v.ResultID || v.Item.DecidedAt.After(v.FinalizedAt) || v.Item.Child != nil && v.Item.Child.UpdatedAt.After(v.FinalizedAt) {
			return ErrHistoryUnavailable
		}
	} else if v.Seal.validate() != nil || v.Seal.Summary.Binding.OperationID != v.OperationID || v.Seal.Summary.Binding.ActivationID != v.ResultID || !v.Seal.Summary.FinalizedAt.Equal(v.FinalizedAt) {
		return ErrHistoryUnavailable
	}
	return nil
}

func collectionExecutionPublicationEvent(v CollectionExecutionHistory) Event {
	v = v.clone()
	e := Event{MonitorID: "collection-execution/" + v.OperationID, At: v.FinalizedAt, Kind: "Collection", ActionID: v.ResultID, CollectionExecutionHistory: &v}
	if v.Item != nil {
		e.Type = "collection_execution_item"
	} else {
		e.Type = "collection_execution_seal"
		if v.Seal != nil {
			e.Actor = v.Seal.Summary.Actor
			e.Revision = v.Seal.Summary.ContentDigest
			e.Outcome = v.Seal.Summary.Outcome
		}
	}
	return e
}

const maxCollectionExecutionPublicationEventBytes = 32 << 10

func validateCollectionExecutionPublicationEvent(e Event) error {
	if e.CollectionExecutionHistory == nil || e.CollectionExecutionHistory.validate() != nil || !collectionEventPosition(e.ID) {
		return ErrHistoryUnavailable
	}
	want := collectionExecutionPublicationEvent(*e.CollectionExecutionHistory)
	want.ID = e.ID
	if !reflect.DeepEqual(e, want) {
		return ErrHistoryUnavailable
	}
	return nil
}

func decodeCollectionExecutionPublicationEvent(raw []byte) (Event, error) {
	var e Event
	if len(raw) == 0 || len(raw) > maxCollectionExecutionPublicationEventBytes || collectionPlanJSON(context.Background(), raw) != nil {
		return e, ErrHistoryUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || validateCollectionExecutionPublicationEvent(e) != nil {
		return Event{}, ErrHistoryUnavailable
	}
	canonical, _ := json.Marshal(e)
	if !bytes.Equal(raw, canonical) {
		return Event{}, ErrHistoryUnavailable
	}
	return e, nil
}

func collectionExecutionPublicationIdentity(e Event) (string, bool) {
	const prefix = "collection-execution/"
	if !strings.HasPrefix(e.MonitorID, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(e.MonitorID, prefix)
	_, _, err := ParseOperationHandle(id)
	return id, err == nil
}

type collectionExecutionPublicationProgress struct {
	ResultID     string    `json:"result_id"`
	FinalizedAt  time.Time `json:"finalized_at"`
	Count        uint64    `json:"count"`
	Bytes        uint64    `json:"bytes"`
	Digest       string    `json:"digest"`
	LastEvent    string    `json:"last_event"`
	SummaryEvent string    `json:"summary_event,omitempty"`
}

func (p collectionExecutionPublicationProgress) validate() error {
	if !validOperationEpoch(p.ResultID) || p.FinalizedAt.IsZero() || p.Count > CollectionValidationMaxItems || p.Bytes > collectionExecutionResultMaxBytes || !bootstrapHash(p.Digest) || !collectionEventPosition(p.LastEvent) ||
		p.SummaryEvent != "" && (p.SummaryEvent != p.LastEvent || !collectionEventPosition(p.SummaryEvent)) || p.Count == 0 && (p.Bytes != 0 || p.Digest != collectionExecutionResultInitialDigest()) || p.Count > 0 && p.Bytes < 4*p.Count {
		return ErrHistoryUnavailable
	}
	return nil
}

// A nil previous progress is permitted only at the first item (or a zero-item
// summary). Retries are handled by exact event-byte identity before advancing.
func advanceCollectionExecutionPublication(p *collectionExecutionPublicationProgress, e Event) (collectionExecutionPublicationProgress, error) {
	if validateCollectionExecutionPublicationEvent(e) != nil {
		return collectionExecutionPublicationProgress{}, ErrHistoryUnavailable
	}
	v := e.CollectionExecutionHistory
	next := collectionExecutionPublicationProgress{ResultID: v.ResultID, FinalizedAt: v.FinalizedAt, Digest: collectionExecutionResultInitialDigest()}
	if p != nil {
		if p.validate() != nil || p.ResultID != v.ResultID || !p.FinalizedAt.Equal(v.FinalizedAt) || p.SummaryEvent != "" || e.ID <= p.LastEvent {
			return next, ErrHistoryUnavailable
		}
		next = *p
	}
	if v.Item != nil {
		if v.Item.InputOrdinal != next.Count+1 {
			return next, ErrHistoryUnavailable
		}
		digest, n, err := collectionExecutionResultNextDigest(next.Digest, *v.Item)
		if err != nil || n > collectionExecutionResultMaxBytes-next.Bytes {
			return next, ErrHistoryUnavailable
		}
		next.Count++
		next.Bytes += n
		next.Digest = digest
	} else {
		d := v.Seal.Descriptor
		if d.Count != next.Count || d.Bytes != next.Bytes || d.Digest != next.Digest {
			return next, ErrHistoryUnavailable
		}
		next.SummaryEvent = e.ID
	}
	next.LastEvent = e.ID
	return next, nil
}
