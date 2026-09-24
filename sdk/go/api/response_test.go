package api

import "testing"

func TestResponseDestinationAndRequiredTimestamp(t *testing.T) {
	var pointer *State
	for _, destination := range []any{nil, pointer, State{}} {
		if err := DecodeResponse([]byte(`{}`), destination); err == nil {
			t.Fatal("invalid destination accepted")
		}
	}
	var state State
	if err := DecodeResponse([]byte(`{"generatedAt":null,"live":true,"ready":true,"storage":{"mode":"memory","available":true}}`), &state); err == nil {
		t.Fatal("null required timestamp accepted as a measured zero time")
	}
}
