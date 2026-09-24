package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCommandMonitorIdentityBound(t *testing.T) {
	at := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	for _, kind := range []string{"pulse", "start", "result", "late_result"} {
		t.Run(kind, func(t *testing.T) {
			c := Command{Kind: kind, At: at, MonitorID: strings.Repeat("m", 256), Revision: "r"}
			if kind == "pulse" {
				c.Generation = 1
			} else {
				c.ActionID = "action"
			}
			if kind == "late_result" {
				c.Outcome = "success"
				c.ExecutionStart = at.Add(-time.Second)
				c.ExecutionEnd = at
			}
			if err := validateCommand(c); err != nil {
				t.Fatal(err)
			}
			c.MonitorID += "m"
			if err := validateCommand(c); err == nil {
				t.Fatal("oversized identity accepted")
			}
			data, _ := json.Marshal(envelope{Version: commandWriteFormat(c), Commands: []Command{c}})
			if _, err := decodeEnvelope(data); err == nil {
				t.Fatal("replay accepted oversized identity")
			}
			c.MonitorID = strings.Repeat("é", 129)
			if err := validateCommand(c); err == nil {
				t.Fatal("identity bounded by runes instead of bytes")
			}
		})
	}
}

func TestCommandFormatHistoricalCatalogCompatibility(t *testing.T) {
	s := openCatalogMemory(t)
	c := reservationCommand(t, s, "wire-contract", time.Now().UTC())
	if commandWriteFormat(c) != CatalogMutationFormatVersion {
		t.Fatal("new catalog writes lost mutation sequence format")
	}
	for _, version := range []int{CatalogFormatVersion, CollectionFormatVersion, LatestFormatVersion} {
		data, _ := json.Marshal(envelope{Version: version, Commands: []Command{c}})
		if _, err := decodeEnvelope(data); err != nil {
			t.Fatalf("historical format %d: %v", version, err)
		}
	}
}

// This freezes the transitive JSON shapes contributing to existing operation
// and authentication digests, including changes hidden behind pointer fields.
func TestDigestV1WireShape(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeFor[AuthenticationCommand](),
		reflect.TypeFor[AuthenticationPrincipal](), reflect.TypeFor[CatalogMutation](),
		reflect.TypeFor[ControlCommand](), reflect.TypeFor[ManualRecoveryCommand](),
		reflect.TypeFor[ActionReviewCommand](), reflect.TypeFor[CatalogGuard](),
	}
	var shape strings.Builder
	seen := make(map[reflect.Type]bool)
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			visit(typ.Elem())
			return
		case reflect.Map:
			visit(typ.Key())
			visit(typ.Elem())
			return
		}
		if typ.Kind() != reflect.Struct || typ == reflect.TypeFor[time.Time]() || seen[typ] {
			return
		}
		seen[typ] = true
		fmt.Fprintf(&shape, "%s{", typ.Name())
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" {
				continue
			}
			fmt.Fprintf(&shape, "%s:%s:%s;", f.Name, f.Type, f.Tag.Get("json"))
			visit(f.Type)
		}
		shape.WriteString("}")
	}
	for _, typ := range types {
		visit(typ)
	}
	hash := sha256.Sum256([]byte(shape.String()))
	const want = "33e16905ccb0b7cc4d5855628eb22219f59204d110893574d2cf046545b449d7"
	if got := hex.EncodeToString(hash[:]); got != want {
		t.Fatalf("digest v1 wire shape changed (%s); preserve the old projection before changing a digest format", got)
	}
}

func TestDigestV1GoldenReplayIdentities(t *testing.T) {
	at := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	a := AuthenticationCommand{Mode: "bootstrap", Epoch: "epoch", Revision: "revision", Actor: "alice", At: at, AnonymousLoopback: true}
	if got := a.digest(); got != "37ee168121c7b52b02bb20e375f1b2257b58a787ca69b64753673fd52d80555e" {
		t.Fatalf("authentication replay digest changed: %s", got)
	}
	c := Command{Kind: "control", MonitorID: "monitor", At: at, Control: &ControlCommand{
		Action: "disable", MonitorUID: "uid", ExpectedRevision: "old", Revision: "new", OperationID: "original", Actor: "alice", Reason: "maintenance",
	}}
	r, _, err := operationDigest(c)
	if err != nil || r.Digest != "823e9c304d28ccbfaeddce44099686bb2404ad89f96475c28be430371d8f3f0b" {
		t.Fatalf("operation reservation digest changed: %s (%v)", r.Digest, err)
	}
	c.At = at.Add(time.Hour)
	c.Control.OperationID = "replacement-reply-handle"
	repeated, _, err := operationDigest(c)
	if err != nil || repeated.Digest != r.Digest {
		t.Fatal("retry timestamp/handle changed original identity", err)
	}
	c.Control.Reason = "different-intent"
	changed, _, err := operationDigest(c)
	if err != nil || changed.Digest == r.Digest {
		t.Fatal("different intent reused reservation", err)
	}
}
