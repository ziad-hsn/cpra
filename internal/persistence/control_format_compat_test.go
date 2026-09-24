package persistence

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCheckControlFenceWritesVersionTwoAndReplaysLegacyVersionOne(t *testing.T) {
	command := Command{Kind: "pulse", At: time.Now().UTC(), MonitorID: "manifest-check", Revision: "manifest-v1", Generation: 1, Outcome: "success", CheckControlRevision: "control-revision"}
	if got := commandWriteFormat(command); got != CatalogFormatVersion {
		t.Fatalf("new control fence written as format %d", got)
	}
	for _, version := range []int{FormatVersion, CatalogFormatVersion} {
		raw, err := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeEnvelope(raw)
		if err != nil {
			t.Fatalf("retained format %d check rejected: %v", version, err)
		}
		if decoded.Commands[0].CheckControlRevision != command.CheckControlRevision {
			t.Fatal("replay lost control fence")
		}
	}
	command.CheckControlRevision = ""
	if got := commandWriteFormat(command); got != FormatVersion {
		t.Fatalf("unfenced lifecycle compatibility changed to %d", got)
	}
}
