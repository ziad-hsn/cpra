package api

import (
	"bytes"
	"encoding/json"
	"testing"
)

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
