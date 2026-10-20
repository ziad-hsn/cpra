package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"time"
)

const reviewPolicyPageSize = 256

// A policy is immutable and shared by assessments. Its header has Offset -1.
// Ordinal-only policies need no destination rows, even for older large counts.
// Explicit settings use bounded pages and retain original budgets and identities.
type reviewPolicyPage struct {
	ID           string              `json:"id"`
	Revision     string              `json:"revision,omitempty"`
	Count        int                 `json:"count"`
	Explicit     bool                `json:"explicit,omitempty"`
	Offset       int                 `json:"offset"`
	Destinations []reviewDestination `json:"destinations,omitempty"`
}

func reviewPolicyKey(monitor, id string, offset int) string {
	return reviewPrefix("policy", monitor) + id + "/" + fmt.Sprintf("%010d", offset+1)
}

func reviewPolicyDigest(m Monitor, count int, explicit bool) hash.Hash {
	h := sha256.New()
	_ = json.NewEncoder(h).Encode([]any{m.ID, m.incarnationIdentity(), m.Revision, count, explicit})
	return h
}

func reviewPolicyIdentity(m Monitor) string {
	count := m.Policy.Endpoints["blue"]
	explicit := len(m.Policy.NotificationEndpointIDs["blue"]) != 0 || len(m.Policy.NotificationTimeouts["blue"]) != 0
	h := reviewPolicyDigest(m, count, explicit)
	if explicit {
		for n := 0; n < count; n++ {
			_ = json.NewEncoder(h).Encode(reviewDestination{ID: notificationEndpointIdentity(m, "blue", n), Timeout: m.Policy.notificationTimeout("blue", n)})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (f *machine) captureReviewPolicy(m *Monitor) error {
	count := m.Policy.Endpoints["blue"]
	explicit := len(m.Policy.NotificationEndpointIDs["blue"]) != 0 || len(m.Policy.NotificationTimeouts["blue"]) != 0
	id := reviewPolicyIdentity(*m)
	key := reviewPolicyKey(m.ID, id, -1)
	if row, ok, err := f.readReview(key); err != nil {
		return err
	} else if ok && (row.Policy == nil || row.Policy.Count != count || row.Policy.Explicit != explicit) {
		return errActionLedger
	} else if !ok {
		f.writeReview(reviewRecord{Key: key, MonitorID: m.ID, Policy: &reviewPolicyPage{ID: id, Revision: m.Revision, Count: count, Explicit: explicit, Offset: -1}})
		if explicit {
			for offset := 0; offset < count; offset += reviewPolicyPageSize {
				page := reviewPolicyPage{ID: id, Count: count, Explicit: true, Offset: offset}
				for n := offset; n < min(count, offset+reviewPolicyPageSize); n++ {
					page.Destinations = append(page.Destinations, reviewDestination{ID: notificationEndpointIdentity(*m, "blue", n), Timeout: m.Policy.notificationTimeout("blue", n)})
				}
				f.writeReview(reviewRecord{Key: reviewPolicyKey(m.ID, id, offset), MonitorID: m.ID, Policy: &page})
			}
		}
	}
	m.ReviewPolicyID = id
	m.ReviewRevisionDigest = identity(m.Revision)
	return nil
}

func hydrateReviewRevision(m Monitor, id string, fence uint64, r reviewRecord) (Monitor, error) {
	if r.Policy == nil || r.Key != reviewPolicyKey(m.ID, id, -1) || r.Policy.ID != id || r.Policy.Offset != -1 || r.Policy.Count != m.Policy.Endpoints["blue"] || r.MonitorID != m.ID || r.Sequence > fence || r.Policy.Revision == "" || identity(r.Policy.Revision) != m.ReviewRevisionDigest || m.Revision != "" && m.Revision != r.Policy.Revision {
		return m, errActionLedger
	}
	// The reference saves repeated copies in facts, not the execution binding.
	// Restore the exact original value before eligibility or action construction.
	m.Revision = r.Policy.Revision
	return m, nil
}

func (f *machine) reviewDestination(fact reviewFact, m Monitor, header reviewRecord, endpoint int) (reviewDestination, error) {
	if endpoint < 0 || endpoint >= m.Policy.Endpoints["blue"] {
		return reviewDestination{}, errActionLedger
	}
	if !header.Policy.Explicit {
		return reviewDestination{ID: notificationEndpointIdentity(m, "blue", endpoint), Timeout: NotificationTimeout}, nil
	}
	offset := endpoint / reviewPolicyPageSize * reviewPolicyPageSize
	created := header.Sequence
	r, ok, err := f.readReview(reviewPolicyKey(m.ID, fact.PolicyID, offset))
	if err != nil || !ok || r.Policy == nil || r.Policy.ID != fact.PolicyID || r.Policy.Offset != offset || !r.Policy.Explicit || r.Policy.Count != m.Policy.Endpoints["blue"] || r.MonitorID != m.ID || r.Sequence != created || endpoint-offset >= len(r.Policy.Destinations) {
		return reviewDestination{}, errActionLedger
	}
	return r.Policy.Destinations[endpoint-offset], nil
}

func (f *machine) reviewInput(fact reviewFact, endpoint int) (Monitor, error) {
	m := fact.Context.Clone()
	header, ok, err := f.readReview(reviewPolicyKey(m.ID, fact.PolicyID, -1))
	if err != nil || !ok {
		return m, errActionLedger
	}
	m, err = hydrateReviewRevision(m, fact.PolicyID, fact.Fence, header)
	if err != nil {
		return m, err
	}
	if endpoint >= 0 && endpoint < m.Policy.Endpoints["blue"] {
		destination, err := f.reviewDestination(fact, m, header, endpoint)
		if err != nil {
			return m, err
		}
		m.reviewEndpointIDs = map[int]string{endpoint: destination.ID}
		m.reviewTimeouts = map[int]time.Duration{endpoint: destination.Timeout}
	}
	return m, nil
}

func (v *collectionLedgerView) validateReviewPolicy(m Monitor, id string, fence uint64) error {
	header, ok, err := v.readReviewHead(reviewPolicyKey(m.ID, id, -1))
	if err != nil || !ok {
		return errActionLedger
	}
	m, err = hydrateReviewRevision(m, id, fence, header)
	if err != nil {
		return err
	}
	p := header.Policy
	h := reviewPolicyDigest(m, p.Count, p.Explicit)
	if p.Explicit {
		for offset := 0; offset < p.Count; offset += reviewPolicyPageSize {
			r, ok, err := v.readReviewHead(reviewPolicyKey(m.ID, id, offset))
			if err != nil || !ok || r.Policy == nil || !r.Policy.Explicit || r.Policy.Count != p.Count || r.Sequence != header.Sequence || len(r.Policy.Destinations) != min(reviewPolicyPageSize, p.Count-offset) {
				return errActionLedger
			}
			for _, d := range r.Policy.Destinations {
				_ = json.NewEncoder(h).Encode(d)
			}
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != id {
		return errActionLedger
	}
	return nil
}
