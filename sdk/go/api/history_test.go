package api

import (
	"encoding/json"
	"testing"
)

func TestHistoryAuditFieldsRoundTrip(t *testing.T) {
	raw := []byte(`{"items":[{"id":"event","monitorID":"monitor","kind":"control_acknowledge","time":"2026-09-14T00:00:00Z","actor":"oncall","note":"Investigating","reason":"Known issue","incidentID":"incident","actionID":"action","actionKind":"code","color":"red","endpoint":0,"outcome":"dismiss","executionRevision":"execution","controlRevision":"control","evidenceRefs":["ticket:123"]}]}`)
	var result EventList
	if err := DecodeResponse(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Note != "Investigating" || result.Items[0].Actor != "oncall" || result.Items[0].IncidentID != "incident" || result.Items[0].ActionID != "action" || result.Items[0].Endpoint == nil || *result.Items[0].Endpoint != 0 || result.Items[0].Outcome != "dismiss" || result.Items[0].ExecutionRevision != "execution" || len(result.Items[0].EvidenceRefs) != 1 || result.Items[0].EvidenceRefs[0] != "ticket:123" {
		t.Fatal("audit fields lost")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EventList
	if err := StrictDecode(encoded, &decoded); err != nil || decoded.Items[0].ID != result.Items[0].ID || decoded.Items[0].Endpoint == nil || *decoded.Items[0].Endpoint != 0 {
		t.Fatal("round trip changed event", err)
	}
}
