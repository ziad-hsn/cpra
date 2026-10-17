package api

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFailureDiagnosticObservationRoundTrips(t *testing.T) {
	raw := []byte(`{"lastFailure":{"code":"dns_failed","message":"The target name could not be resolved."}}`)
	var status MonitorStatus
	if err := DecodeResponse(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status.LastFailure == nil || status.LastFailure.Code != "dns_failed" {
		t.Fatal("safe diagnostic not decoded")
	}
	encoded, err := json.Marshal(status)
	if err != nil || !bytes.Contains(encoded, []byte(`"lastFailure"`)) {
		t.Fatal("safe diagnostic not encoded", err)
	}
	if err := DecodeResponse([]byte(`{"lastFailure":{"code":"dns_failed"}}`), &status); err == nil {
		t.Fatal("incomplete diagnostic accepted")
	}
}

func TestFailureCertificateDetailsRoundTrip(t *testing.T) {
	raw := []byte(`{"lastFailure":{"code":"certificate_expiring","message":"The certificate expires within the configured threshold.","certificateNotAfter":"2026-10-16T12:00:00Z","certificateDaysRemaining":0}}`)
	var status MonitorStatus
	if err := DecodeResponse(raw, &status); err != nil {
		t.Fatal(err)
	}
	d := status.LastFailure
	if d == nil || d.CertificateDaysRemaining == nil || *d.CertificateDaysRemaining != 0 || d.CertificateNotAfter == nil {
		t.Fatal("certificate detail unavailable after decoding")
	}
	encoded, err := json.Marshal(status)
	if err != nil || !bytes.Contains(encoded, []byte(`"certificateDaysRemaining":0`)) || !bytes.Contains(encoded, []byte(`"certificateNotAfter":"2026-10-16T12:00:00Z"`)) {
		t.Fatal("certificate detail did not round trip", err)
	}
}

func TestObservationKnownZeroAndOlderOptionalFields(t *testing.T) {
	for _, value := range []any{Measurement{Available: true}, Percentiles{Available: true}, Operation{ID: "op", State: "committed", Committed: Pointer(int64(0)), Applied: Pointer(int64(0))}} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		switch value.(type) {
		case Measurement:
			if !bytes.Contains(raw, []byte(`"value":0`)) {
				t.Fatal(string(raw))
			}
		case Percentiles:
			if !bytes.Contains(raw, []byte(`"p99Ms":0`)) {
				t.Fatal(string(raw))
			}
		case Operation:
			if !bytes.Contains(raw, []byte(`"applied":0`)) || !bytes.Contains(raw, []byte(`"committed":0`)) {
				t.Fatal(string(raw))
			}
		}
	}
	var queue Queue
	if err := DecodeResponse([]byte(`{"name":"pulse","depth":0,"capacity":10,"saturated":false}`), &queue); err != nil {
		t.Fatal("valid older queue rejected", err)
	}
	if queue.Available {
		t.Fatal("missing availability invented")
	}
	var metric Measurement
	if err := DecodeResponse([]byte(`{"available":false}`), &metric); err != nil {
		t.Fatal("optional emitted zero became required", err)
	}
	if err := DecodeResponse([]byte(`{"value":0}`), &metric); err == nil {
		t.Fatal("missing required availability accepted")
	}
	var operation Operation
	if err := DecodeResponse([]byte(`{"id":"op","state":"committed","contentDigest":"digest"}`), &operation); err != nil {
		t.Fatal("valid older operation rejected", err)
	}
	var action Action
	if err := DecodeResponse([]byte(`{"id":"a","monitorID":"m","state":"unknown"}`), &action); err == nil {
		t.Fatal("genuinely required action fields ignored")
	}
}

func TestMonitorActionCountAvailabilityRoundTrip(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"statusRevision":"observed"}`,
		`{"unknownActions":0,"actionsNeedingReview":0}`,
		`{"unknownActions":1,"actionsNeedingReview":0}`,
		`{"unknownActions":2,"actionsNeedingReview":2}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var status MonitorStatus
			if err := DecodeResponse([]byte(raw), &status); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			var before, after map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &after); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"unknownActions", "actionsNeedingReview"} {
				if !bytes.Equal(before[field], after[field]) {
					t.Fatalf("%s changed availability or count: %s -> %s", field, raw, encoded)
				}
			}
		})
	}
}
