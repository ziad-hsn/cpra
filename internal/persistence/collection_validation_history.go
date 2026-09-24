package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// CollectionValidationReceipt is the immutable original verdict, not an
// activation grant. It contains only allowlisted decision metadata.
// FinalizedAt fixes the retention cohort for both items and the summary seal.
type CollectionValidationReceipt struct {
	Header      CollectionValidationHeader     `json:"header"`
	Descriptor  CollectionValidationDescriptor `json:"descriptor"`
	FinalizedAt time.Time                      `json:"finalized_at"`
}

func (r CollectionValidationReceipt) validate() error {
	if r.FinalizedAt.IsZero() || (CollectionValidationBegin{Header: r.Header, Descriptor: r.Descriptor}).validate() != nil {
		return ErrHistoryUnavailable
	}
	return nil
}

func collectionValidationReceiptsEqual(a, b CollectionValidationReceipt) bool {
	return a.Header == b.Header && a.Descriptor == b.Descriptor && a.FinalizedAt.Equal(b.FinalizedAt)
}

// CollectionValidationHistory carries exactly one item or the final summary.
// Source paths, resource bodies, credentials and arbitrary diagnostics cannot
// be represented. Event IDs still use the committed log position and ordinal.
type CollectionValidationHistory struct {
	OperationID string                       `json:"operation_id"`
	ResultID    string                       `json:"result_id"`
	FinalizedAt time.Time                    `json:"finalized_at"`
	Item        *CollectionValidationItem    `json:"item,omitempty"`
	Summary     *CollectionValidationReceipt `json:"summary,omitempty"`
}

func (v CollectionValidationHistory) clone() CollectionValidationHistory {
	if v.Item != nil {
		x := *v.Item
		v.Item = &x
	}
	if v.Summary != nil {
		x := *v.Summary
		v.Summary = &x
	}
	return v
}

func (v CollectionValidationHistory) validate() error {
	if _, _, err := ParseOperationHandle(v.OperationID); err != nil || !validOperationEpoch(v.ResultID) || v.FinalizedAt.IsZero() || (v.Item == nil) == (v.Summary == nil) {
		return ErrHistoryUnavailable
	}
	if v.Item != nil {
		if _, err := CollectionValidationItemEncoding(*v.Item); err != nil || v.Item.Ordinal > CollectionValidationMaxItems {
			return ErrHistoryUnavailable
		}
	} else if v.Summary.validate() != nil || v.Summary.Header.OperationID != v.OperationID || v.Summary.Header.ResultID != v.ResultID || !v.Summary.FinalizedAt.Equal(v.FinalizedAt) {
		return ErrHistoryUnavailable
	}
	return nil
}

func collectionValidationHistoryEvent(v CollectionValidationHistory) Event {
	v = v.clone()
	e := Event{MonitorID: "collection-validation/" + v.OperationID, At: v.FinalizedAt, Kind: "Collection", ActionID: v.ResultID, CollectionValidation: &v}
	if v.Item != nil {
		e.Type = "collection_validation_item"
	} else {
		e.Type = "collection_validation_summary"
		if v.Summary != nil {
			e.Actor = v.Summary.Header.Authority.Actor
			e.Revision = v.Summary.Header.InputProgressDigest
			e.Outcome = "rejected"
			if v.Summary.Header.Valid {
				e.Outcome = "validated"
			}
		}
	}
	return e
}

const maxCollectionValidationHistoryEventBytes = 16 << 10

func validateCollectionValidationHistoryEvent(e Event) error {
	if e.CollectionValidation == nil || e.CollectionValidation.validate() != nil || !collectionEventPosition(e.ID) {
		return ErrHistoryUnavailable
	}
	want := collectionValidationHistoryEvent(*e.CollectionValidation)
	want.ID = e.ID
	if !reflect.DeepEqual(e, want) {
		return ErrHistoryUnavailable
	}
	return nil
}

func decodeCollectionValidationHistoryEvent(raw []byte) (Event, error) {
	var e Event
	if len(raw) == 0 || len(raw) > maxCollectionValidationHistoryEventBytes || collectionPlanJSON(context.Background(), raw) != nil {
		return e, ErrHistoryUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || validateCollectionValidationHistoryEvent(e) != nil {
		return Event{}, ErrHistoryUnavailable
	}
	canonical, _ := json.Marshal(e)
	if !bytes.Equal(raw, canonical) {
		return Event{}, ErrHistoryUnavailable
	}
	return e, nil
}

func collectionValidationHistoryIdentity(e Event) (string, bool) {
	const prefix = "collection-validation/"
	if !strings.HasPrefix(e.MonitorID, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(e.MonitorID, prefix)
	_, _, err := ParseOperationHandle(id)
	return id, err == nil
}

type collectionValidationHistoryProgress struct {
	ResultID     string    `json:"result_id"`
	FinalizedAt  time.Time `json:"finalized_at"`
	Count        uint64    `json:"count"`
	Bytes        uint64    `json:"bytes"`
	Digest       string    `json:"digest"`
	LastEvent    string    `json:"last_event"`
	SummaryEvent string    `json:"summary_event,omitempty"`
}

func (p collectionValidationHistoryProgress) validate() error {
	if !validOperationEpoch(p.ResultID) || p.FinalizedAt.IsZero() || p.Count > CollectionValidationMaxItems || p.Bytes > CollectionValidationResultMaxBytes || !bootstrapHash(p.Digest) || !collectionEventPosition(p.LastEvent) ||
		p.SummaryEvent != "" && (p.SummaryEvent != p.LastEvent || !collectionEventPosition(p.SummaryEvent)) || p.Count == 0 && (p.Bytes != 0 || p.Digest != CollectionValidationInitialDigest()) || p.Count > 0 && p.Bytes < 4*p.Count {
		return ErrHistoryUnavailable
	}
	return nil
}

// A nil previous progress is permitted only at the first item (or a zero-item
// summary). Retries are handled by exact event-byte identity before advancing.
func advanceCollectionValidationHistory(p *collectionValidationHistoryProgress, e Event) (collectionValidationHistoryProgress, error) {
	if validateCollectionValidationHistoryEvent(e) != nil {
		return collectionValidationHistoryProgress{}, ErrHistoryUnavailable
	}
	v := e.CollectionValidation
	next := collectionValidationHistoryProgress{ResultID: v.ResultID, FinalizedAt: v.FinalizedAt, Digest: CollectionValidationInitialDigest()}
	if p != nil {
		if p.validate() != nil || p.ResultID != v.ResultID || !p.FinalizedAt.Equal(v.FinalizedAt) || p.SummaryEvent != "" || e.ID <= p.LastEvent {
			return next, ErrHistoryUnavailable
		}
		next = *p
	}
	if v.Item != nil {
		if v.Item.Ordinal != next.Count+1 {
			return next, ErrHistoryUnavailable
		}
		digest, n, err := CollectionValidationNextDigest(next.Digest, *v.Item)
		if err != nil || n > CollectionValidationResultMaxBytes-next.Bytes {
			return next, ErrHistoryUnavailable
		}
		next.Count++
		next.Bytes += n
		next.Digest = digest
	} else {
		d := v.Summary.Descriptor
		if d.Count != next.Count || d.Bytes != next.Bytes || d.Digest != next.Digest {
			return next, ErrHistoryUnavailable
		}
		next.SummaryEvent = e.ID
	}
	next.LastEvent = e.ID
	return next, nil
}
