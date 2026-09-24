package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestActionReviewAndEvidenceRoundTrip(t *testing.T) {
	raw := []byte(`{"id":"action","state":"unknown","held":true,"executorFenced":false,"reviewRevision":"observation","lateEvidence":{"outcome":"accepted","executionStart":"2026-09-14T00:00:00Z","executionEnd":"2026-09-14T00:00:01Z","recordedAt":"2026-09-14T00:00:02Z"},"review":{"revision":"review","resolution":"inconclusive","actor":"oncall","reviewedAt":"2026-09-14T00:00:03Z","reason":"Awaiting evidence","evidenceRefs":["ticket:123"]}}`)
	var action Action
	if err := DecodeResponse(raw, &action); err != nil {
		t.Fatal(err)
	}
	if action.CreatedAt != nil || action.UpdatedAt != nil || action.LateEvidence == nil || action.LateEvidence.Outcome != "accepted" || action.Review == nil || action.Review.Resolution != Inconclusive {
		t.Fatal("action contract fields lost")
	}
	encoded, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "0001-01-01") || strings.Contains(string(encoded), "createdAt") {
		t.Fatal("missing date became fabricated time")
	}
	var result Action
	if err := StrictDecode(encoded, &result); err != nil || result.Review.EvidenceRefs[0] != "ticket:123" {
		t.Fatal("roundtrip", err)
	}
}
