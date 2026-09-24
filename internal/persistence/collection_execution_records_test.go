package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func executionRecordFixture(t *testing.T) (CollectionPreparedItem, CollectionItemOutcome) {
	t.Helper()
	p := CollectionPreparedItem{
		Binding: CollectionExecutionBinding{OperationID: ledgerTestOperation(1), UploadID: "22222222-2222-4222-8222-222222222222",
			ActivationID: "33333333-3333-4333-8333-333333333333", PlanID: "44444444-4444-4444-8444-444444444444", PlanDigest: strings.Repeat("a", 64)},
		ID: "55555555-5555-4555-8555-555555555555", Ordinal: 2, InputOrdinal: 7,
		RowDigest: strings.Repeat("b", 64), At: catalogDeltaAt, Record: catalogDeltaRecord("Monitor", "payments"),
	}
	o := CollectionItemOutcome{Binding: p.Binding, Ordinal: p.Ordinal, InputOrdinal: p.InputOrdinal, RowDigest: p.RowDigest,
		Key: p.Record.Key, Source: "source.00000000000000000003", SourceDocument: 4, SourceItem: 5,
		Decision: "accepted", PreparedID: p.ID, UID: p.Record.UID, Revision: p.Record.Revision, Generation: p.Record.Generation,
		MutationSequence: 42, CommittedIndex: 12, At: p.At.Add(time.Second)}
	o.Receipt = &OperationReceipt{ID: ledgerTestOperation(2), Key: o.Key, UID: o.UID, NewVersion: o.Revision,
		Generation: o.Generation, CommittedIndex: o.CommittedIndex, Actor: "operator", At: o.At, UpdatedAt: o.At,
		State: "committed", Outcome: "committed"}
	if p.validate() != nil || o.validate() != nil || !p.matchesOutcome(o) {
		t.Fatal("invalid execution-record fixture")
	}
	return p, o
}

func executionRecordTerminal(t *testing.T, accepted CollectionItemOutcome, state, outcome, restore string) CollectionChildObservation {
	t.Helper()
	r := *accepted.Receipt
	r.State, r.Outcome, r.InvalidatedByRestore, r.UpdatedAt = state, outcome, restore, accepted.At.Add(time.Second)
	terminal, err := collectionChildObservationFor(accepted, r)
	if err != nil {
		t.Fatal(err)
	}
	return terminal
}

func TestCollectionExecutionRecordCanonicalRoundTripAndCharge(t *testing.T) {
	p, accepted := executionRecordFixture(t)
	unchanged := accepted.Clone()
	unchanged.Decision, unchanged.PreparedID, unchanged.MutationSequence, unchanged.Receipt = "unchanged", "", 0, nil
	unchanged.OldVersion = unchanged.Revision
	conflict := CollectionItemOutcome{Binding: accepted.Binding, Ordinal: accepted.Ordinal, InputOrdinal: accepted.InputOrdinal,
		RowDigest: accepted.RowDigest, Key: accepted.Key, Source: accepted.Source, SourceDocument: accepted.SourceDocument,
		SourceItem: accepted.SourceItem, Decision: "conflict", PreparedID: p.ID, CommittedIndex: accepted.CommittedIndex, At: accepted.At}
	blocked := conflict.Clone()
	blocked.Decision, blocked.PreparedID = "dependencyBlocked", ""
	applied := executionRecordTerminal(t, accepted, "completed", "applied", "")
	failed := executionRecordTerminal(t, accepted, "failed", "projection_failed", "")
	superseded := executionRecordTerminal(t, accepted, "partial", "superseded", "")
	restored := executionRecordTerminal(t, accepted, "partial", "superseded", "restore-id")
	for _, tc := range []struct {
		name string
		row  collectionExecutionRecord
		slot string
	}{
		{"prepared", collectionExecutionRecord{Version: 1, Prepared: &p}, "prepared"},
		{"accepted", collectionExecutionRecord{Version: 1, Outcome: &accepted}, "outcome/0000000000000002"},
		{"unchanged", collectionExecutionRecord{Version: 1, Outcome: &unchanged}, "outcome/0000000000000002"},
		{"conflict", collectionExecutionRecord{Version: 1, Outcome: &conflict}, "outcome/0000000000000002"},
		{"dependencyBlocked", collectionExecutionRecord{Version: 1, Outcome: &blocked}, "outcome/0000000000000002"},
		{"applied", collectionExecutionRecord{Version: 1, Terminal: &applied}, "terminal/0000000000000002"},
		{"failed", collectionExecutionRecord{Version: 1, Terminal: &failed}, "terminal/0000000000000002"},
		{"superseded", collectionExecutionRecord{Version: 1, Terminal: &superseded}, "terminal/0000000000000002"},
		{"restore", collectionExecutionRecord{Version: 1, Terminal: &restored}, "terminal/0000000000000002"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := collectionExecutionEncoding(tc.row)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decodeCollectionExecutionRecord(raw)
			if err != nil || !reflect.DeepEqual(decoded, tc.row) {
				t.Fatalf("round trip differs: %v", err)
			}
			op, slot, err := decoded.identity()
			if err != nil || op != p.Binding.OperationID || slot != tc.slot {
				t.Fatalf("wrong record identity: %q %q %v", op, slot, err)
			}
			want := int64(len(raw))
			if tc.name == "accepted" {
				want += 4096
			} else if tc.row.Terminal != nil {
				want = 0
			}
			if got := collectionExecutionCharge(tc.row, raw); got != want {
				t.Fatalf("charge=%d, want %d", got, want)
			}
			// Decoder ownership must not depend on the caller's reused buffer.
			clear(raw)
			reencoded, err := collectionExecutionEncoding(decoded)
			original, originalErr := collectionExecutionEncoding(tc.row)
			if err != nil || originalErr != nil || !bytes.Equal(reencoded, original) {
				t.Fatal("decoder retained caller-owned input")
			}
		})
	}
}

func TestCollectionExecutionRecordPreparedEnvelopeRoundTrip(t *testing.T) {
	for _, size := range []int{8192, secureconfig.MaxPlaintext + 16} {
		t.Run(fmt.Sprintf("ciphertext=%d", size), func(t *testing.T) {
			p, _ := executionRecordFixture(t)
			p.Record.Payload.Ciphertext = bytes.Repeat([]byte{0xa5}, size)
			p.Record.Payload.WrappedKey = bytes.Repeat([]byte{0xb6}, secureconfig.MaxWrappedKey)
			// Prepared configuration references are not plan-guard chunks.
			for n := 0; n < 257; n++ {
				p.Record.References = append(p.Record.References, CatalogKey{Kind: "NotificationEndpoint", ID: fmt.Sprintf("endpoint-%03d", n)})
			}
			row := collectionExecutionRecord{Version: 1, Prepared: &p}
			raw, err := collectionExecutionEncoding(row)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) >= collectionExecutionMaxFrame {
				t.Fatal("fixture must fit the execution frame")
			}
			decoded, err := decodeCollectionExecutionRecord(raw)
			if err != nil || !reflect.DeepEqual(decoded, row) {
				t.Fatalf("valid bounded prepared ciphertext failed round trip: %v", err)
			}
		})
	}
}

func TestCollectionExecutionRecordPreparedLargeResourceReferences(t *testing.T) {
	for _, tc := range []struct {
		name, fill string
		count      int
		nearLimit  bool
	}{
		{name: "3500-long-identities", count: 3500, fill: strings.Repeat("x", 241)},
		{name: "10000-near-resource-limit", count: 10000, fill: strings.Repeat("x", 85), nearLimit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := executionRecordFixture(t)
			p.Record = catalogDeltaRecord("Recipient", "oncall")
			p.Record.UID = "66666666-6666-4666-8666-666666666666"
			p.Record.Revision = "77777777-7777-4777-8777-777777777777"
			refs := make([]string, tc.count)
			for n := range refs {
				refs[n] = fmt.Sprintf("endpoint-%05d-%s", n, tc.fill)
			}
			encodeResource := func() (api.Resource, []byte) {
				spec, err := json.Marshal(api.RecipientSpec{EndpointRefs: refs})
				if err != nil {
					t.Fatal(err)
				}
				r := api.Resource{APIVersion: api.APIVersion, Kind: p.Record.Key.Kind,
					Metadata: api.Metadata{ID: p.Record.Key.ID, UID: p.Record.UID, ResourceVersion: p.Record.Revision, Generation: 1}, Spec: spec}
				raw, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				return r, raw
			}
			resource, plain := encodeResource()
			if tc.nearLimit {
				// ASCII reference characters add exactly one canonical JSON byte.
				// Keep every ID within the existing 256-byte catalog limit.
				remaining := api.MaxResourceBytes - 16 - len(plain)
				if remaining < 0 {
					t.Fatal("base resource already exceeds the intended limit")
				}
				for n := range refs {
					add := min(remaining, 256-len(refs[n]))
					refs[n] += strings.Repeat("x", add)
					remaining -= add
				}
				if remaining != 0 {
					t.Fatal("fixture cannot reach the resource boundary")
				}
				resource, plain = encodeResource()
				if len(plain) != api.MaxResourceBytes-16 {
					t.Fatal("fixture does not exercise near-maximum canonical plaintext")
				}
			}
			if len(plain) > api.MaxResourceBytes {
				t.Fatal("resource fixture exceeds public source quota")
			}
			if err := api.ValidateResource(resource); err != nil {
				t.Fatalf("public resource schema rejects fixture: %v", err)
			}
			if _, err := api.DecodeResource(plain); err != nil {
				t.Fatalf("bounded source decoder rejects fixture: %v", err)
			}
			for _, id := range refs {
				p.Record.References = append(p.Record.References, CatalogKey{Kind: "NotificationEndpoint", ID: id})
			}
			// Ciphertext-shaped bytes match AEAD's plaintext+16 length. This
			// validates storage sizing/serialization, not cryptographic sealing,
			// referent existence, graph validity, authorization or provider I/O.
			p.Record.Payload.Ciphertext = bytes.Repeat([]byte{0x81}, len(plain)+16)
			p.Record.Payload.WrappedKey = bytes.Repeat([]byte{0x92}, secureconfig.MaxWrappedKey)
			row := collectionExecutionRecord{Version: 1, Prepared: &p}
			raw, err := collectionExecutionEncoding(row)
			if err != nil || len(raw) <= 2<<20 || len(raw) > 3<<20 {
				t.Fatalf("prepared frame must cross old ceiling within new bound: bytes=%d error=%v", len(raw), err)
			}
			decoded, err := decodeCollectionExecutionRecord(raw)
			if err != nil || !reflect.DeepEqual(decoded, row) {
				t.Fatalf("bounded large-reference prepared frame did not round trip: %v", err)
			}
			t.Logf("schema-valid resource=%d bytes; references=%d; prepared frame=%d bytes", len(plain), len(refs), len(raw))
		})
	}
}

func TestCollectionExecutionRecordTerminalBoundAndExactReconstruction(t *testing.T) {
	_, accepted := executionRecordFixture(t)
	accepted.Binding.OperationID = operationHandle("ffffffff-ffff-4fff-bfff-ffffffffffff", math.MaxUint64-1)
	accepted.Binding.UploadID, accepted.Binding.ActivationID, accepted.Binding.PlanID = "ffffffff-ffff-4fff-bfff-ffffffffffff", "eeeeeeee-eeee-4eee-beee-eeeeeeeeeeee", "dddddddd-dddd-4ddd-bddd-dddddddddddd"
	accepted.Binding.PlanDigest, accepted.RowDigest = strings.Repeat("f", 64), strings.Repeat("e", 64)
	accepted.Ordinal, accepted.InputOrdinal = maxCollectionItems, maxCollectionItems
	accepted.Source, _ = commitment.SourceToken(commitment.MaxSources)
	accepted.SourceDocument, accepted.SourceItem = commitment.MaxItems, commitment.MaxItems
	accepted.At = time.Date(9999, 12, 31, 23, 58, 59, 999999999, time.FixedZone("max", -23*3600-59*60))
	accepted.Key = CatalogKey{Kind: "NotificationEndpoint", ID: strings.Repeat("<", 256)}
	accepted.UID, accepted.Revision, accepted.OldVersion = strings.Repeat("<", 256), strings.Repeat(">", 256), strings.Repeat("&", 256)
	accepted.Generation, accepted.CommittedIndex, accepted.MutationSequence = math.MaxUint64, math.MaxUint64, math.MaxUint64
	accepted.Receipt = &OperationReceipt{ID: operationHandle("ffffffff-ffff-4fff-bfff-ffffffffffff", math.MaxUint64),
		Key: accepted.Key, UID: accepted.UID, NewVersion: accepted.Revision, OldVersion: accepted.OldVersion,
		Generation: accepted.Generation, CommittedIndex: accepted.CommittedIndex, Actor: strings.Repeat("<", 128),
		At: accepted.At, UpdatedAt: accepted.At, State: "committed", Outcome: "committed"}
	if accepted.validate() != nil {
		t.Fatal("maximum supported accepted identities must be valid")
	}
	for _, tc := range []struct{ state, outcome, restore string }{
		{"completed", "applied", ""}, {"failed", "projection_failed", ""},
		{"partial", "superseded", ""}, {"partial", "superseded", strings.Repeat("<", 128)},
	} {
		t.Run(tc.state+tc.restore, func(t *testing.T) {
			terminal := *accepted.Receipt
			terminal.State, terminal.Outcome, terminal.InvalidatedByRestore = tc.state, tc.outcome, tc.restore
			terminal.UpdatedAt = time.Date(9999, 12, 31, 23, 59, 59, 999999999, accepted.At.Location())
			compact, err := collectionChildObservationFor(accepted, terminal)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := collectionExecutionEncoding(collectionExecutionRecord{Version: 1, Terminal: &compact})
			if err != nil || len(raw) > 4096 {
				t.Fatalf("terminal exceeds permanently reserved slot: bytes=%d err=%v", len(raw), err)
			}
			decoded, err := decodeCollectionExecutionRecord(raw)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := decoded.Terminal.receipt(accepted)
			if err != nil || !operationReceiptEqualInstant(restored, terminal) {
				t.Fatalf("compact terminal did not preserve exact receipt: %v", err)
			}
			full, err := json.Marshal(terminal)
			if err != nil || len(full) <= 4096 {
				t.Fatal("fixture must demonstrate why the full receipt cannot be the reserved terminal")
			}
			t.Logf("maximum escaped compact terminal=%d bytes; full receipt=%d", len(raw), len(full))
		})
	}
}

func operationReceiptEqualInstant(a, b OperationReceipt) bool {
	if !a.At.Equal(b.At) || !a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}
	a.At, a.UpdatedAt = b.At, b.UpdatedAt
	return a == b
}

func TestCollectionExecutionRecordTerminalRejectsDifferentAcceptedIdentity(t *testing.T) {
	_, accepted := executionRecordFixture(t)
	terminal := *accepted.Receipt
	terminal.State, terminal.Outcome, terminal.UpdatedAt = "completed", "applied", accepted.At.Add(time.Second)
	for _, tc := range []struct {
		name   string
		change func(*OperationReceipt)
	}{
		{"child", func(r *OperationReceipt) { r.ID = ledgerTestOperation(3) }},
		{"key", func(r *OperationReceipt) { r.Key.ID = "other" }},
		{"incarnation", func(r *OperationReceipt) { r.UID += "-other" }},
		{"new-version", func(r *OperationReceipt) { r.NewVersion += "-other" }},
		{"old-version", func(r *OperationReceipt) { r.OldVersion = "old" }},
		{"generation", func(r *OperationReceipt) { r.Generation++ }},
		{"commit", func(r *OperationReceipt) { r.CommittedIndex++ }},
		{"actor", func(r *OperationReceipt) { r.Actor = "other" }},
		{"admission-time", func(r *OperationReceipt) { r.At = r.At.Add(time.Nanosecond) }},
		{"completion-before-acceptance", func(r *OperationReceipt) { r.UpdatedAt = r.At.Add(-time.Nanosecond) }},
		{"removed", func(r *OperationReceipt) { r.Removed = true }},
		{"control", func(r *OperationReceipt) { r.Subject = "control" }},
		{"still-pending", func(r *OperationReceipt) { r.State, r.Outcome = "committed", "committed" }},
		{"restore-on-success", func(r *OperationReceipt) { r.InvalidatedByRestore = "restore" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := terminal
			tc.change(&changed)
			if _, err := collectionChildObservationFor(accepted, changed); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatalf("different terminal identity was accepted: %v", err)
			}
		})
	}
	compact := executionRecordTerminal(t, accepted, "completed", "applied", "")
	for _, tc := range []struct {
		name   string
		change func(*CollectionChildObservation)
	}{
		{"parent", func(r *CollectionChildObservation) { r.Binding.OperationID = ledgerTestOperation(3) }},
		{"activation", func(r *CollectionChildObservation) { r.Binding.ActivationID = "66666666-6666-4666-8666-666666666666" }},
		{"plan", func(r *CollectionChildObservation) { r.Binding.PlanDigest = strings.Repeat("c", 64) }},
		{"row", func(r *CollectionChildObservation) { r.Ordinal++ }},
		{"row-digest", func(r *CollectionChildObservation) { r.RowDigest = strings.Repeat("d", 64) }},
		{"child", func(r *CollectionChildObservation) { r.ChildID = ledgerTestOperation(3) }},
		{"backdated", func(r *CollectionChildObservation) { r.UpdatedAt = accepted.At.Add(-time.Nanosecond) }},
	} {
		t.Run("compact-"+tc.name, func(t *testing.T) {
			changed := compact
			tc.change(&changed)
			if changed.matches(accepted) {
				t.Fatal("different compact identity matched original acceptance")
			}
			if _, err := changed.receipt(accepted); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatalf("different compact identity reconstructed a receipt: %v", err)
			}
		})
	}
	// A wire timestamp may preserve the instant through a different zone.
	equivalent := terminal
	equivalent.At = terminal.At.In(time.FixedZone("half-hour", 19800))
	if _, err := collectionChildObservationFor(accepted, equivalent); err != nil {
		t.Fatalf("equal admission instant rejected: %v", err)
	}
}

func TestCollectionExecutionRecordRejectsNoncanonicalWire(t *testing.T) {
	p, accepted := executionRecordFixture(t)
	terminal := executionRecordTerminal(t, accepted, "completed", "applied", "")
	for _, original := range []collectionExecutionRecord{{Version: 1, Prepared: &p}, {Version: 1, Outcome: &accepted}, {Version: 1, Terminal: &terminal}} {
		raw, err := collectionExecutionEncoding(original)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			wire []byte
		}{
			{"whitespace", append([]byte(" "), raw...)},
			{"trailing-value", append(bytes.Clone(raw), []byte("{}")...)},
			{"unknown-field", bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"unexpected":true`), 1)},
			{"duplicate-field", bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)},
			{"case-alias", bytes.Replace(raw, []byte(`"version":1`), []byte(`"Version":1`), 1)},
			{"explicit-null", bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"outcome":null`), 1)},
			{"null-binding", replaceExecutionJSONField(t, raw, "binding", nil)},
			{"unknown-nested-field", bytes.Replace(raw, []byte(`"activation_id":`), []byte(`"private_payload":"must-not-survive","activation_id":`), 1)},
			{"unsupported-version", bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1)},
		} {
			t.Run(fmt.Sprintf("%t-%t/%s", original.Prepared != nil, original.Outcome != nil, tc.name), func(t *testing.T) {
				decoded, err := decodeCollectionExecutionRecord(tc.wire)
				if !errors.Is(err, errCollectionLedgerCorrupt) || !reflect.DeepEqual(decoded, collectionExecutionRecord{}) {
					t.Fatalf("noncanonical row produced usable data: %v", err)
				}
			})
		}
	}
	deepUnknown := []byte(`{"version":1,"unknown":` + strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001) + "}")
	for _, raw := range [][]byte{nil, []byte("null"), []byte(`{"version":1}`), deepUnknown, bytes.Repeat([]byte(" "), collectionExecutionMaxFrame+1)} {
		if _, err := decodeCollectionExecutionRecord(raw); !errors.Is(err, errCollectionLedgerCorrupt) {
			t.Fatalf("invalid outer frame accepted: %v", err)
		}
	}
	for _, row := range []collectionExecutionRecord{{Version: 1}, {Version: 1, Prepared: &p, Outcome: &accepted}, {Version: 1, Outcome: &accepted, Terminal: &terminal}} {
		if raw, err := collectionExecutionEncoding(row); !errors.Is(err, ErrCollectionInvalid) || raw != nil {
			t.Fatal("ambiguous payload encoded")
		}
	}
}

func replaceExecutionJSONField(t *testing.T, raw []byte, field string, value any) []byte {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"prepared", "outcome", "terminal"} {
		if payload := root[kind]; payload != nil {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			changed, _ := json.Marshal(value)
			original := fields[field]
			return bytes.Replace(raw, append([]byte(`"`+field+`":`), original...), append([]byte(`"`+field+`":`), changed...), 1)
		}
	}
	t.Fatal("missing record payload")
	return nil
}

func TestCollectionExecutionRecordRejectsOutcomeMisclassification(t *testing.T) {
	_, accepted := executionRecordFixture(t)
	for _, tc := range []struct {
		name   string
		change func(*CollectionItemOutcome)
	}{
		{"legacy-child", func(o *CollectionItemOutcome) { o.Receipt.ID = o.Revision }},
		{"parent-as-child", func(o *CollectionItemOutcome) { o.Receipt.ID = o.Binding.OperationID }},
		{"foreign-epoch-child", func(o *CollectionItemOutcome) {
			o.Receipt.ID = operationHandle("77777777-7777-4777-8777-777777777777", 1)
		}},
		{"control-child", func(o *CollectionItemOutcome) { o.Receipt.Subject = "control" }},
		{"terminal-child", func(o *CollectionItemOutcome) { o.Receipt.State, o.Receipt.Outcome = "completed", "applied" }},
		{"removed-child", func(o *CollectionItemOutcome) { o.Receipt.Removed = true }},
		{"missing-child", func(o *CollectionItemOutcome) { o.Receipt = nil }},
		{"missing-preparation", func(o *CollectionItemOutcome) { o.PreparedID = "" }},
		{"missing-token", func(o *CollectionItemOutcome) { o.MutationSequence = 0 }},
		{"wrong-receipt-tuple", func(o *CollectionItemOutcome) { o.Receipt.UID += "-wrong" }},
		{"wrong-receipt-time", func(o *CollectionItemOutcome) { o.Receipt.UpdatedAt = o.Receipt.UpdatedAt.Add(time.Second) }},
		{"unchanged-child", func(o *CollectionItemOutcome) { o.Decision = "unchanged" }},
		{"conflict-with-accepted-version", func(o *CollectionItemOutcome) { o.Decision, o.Receipt = "conflict", nil }},
		{"blocked-with-accepted-version", func(o *CollectionItemOutcome) { o.Decision, o.Receipt = "dependencyBlocked", nil }},
		{"unknown-decision", func(o *CollectionItemOutcome) { o.Decision = "controllerApplied" }},
		{"unknown-kind", func(o *CollectionItemOutcome) { o.Key.Kind, o.Receipt.Key.Kind = "Custom", "Custom" }},
		{"zero-input", func(o *CollectionItemOutcome) { o.InputOrdinal = 0 }},
		{"past-input-bound", func(o *CollectionItemOutcome) { o.InputOrdinal = maxCollectionItems + 1 }},
		{"zero-execution-ordinal", func(o *CollectionItemOutcome) { o.Ordinal = 0 }},
		{"past-execution-bound", func(o *CollectionItemOutcome) { o.Ordinal = maxCollectionItems + 1 }},
		{"source-path", func(o *CollectionItemOutcome) { o.Source = "/private/monitor.yaml" }},
		{"source-noncanonical", func(o *CollectionItemOutcome) { o.Source = "source.3" }},
		{"zero-document", func(o *CollectionItemOutcome) { o.SourceDocument = 0 }},
		{"past-source-item", func(o *CollectionItemOutcome) { o.SourceItem = commitment.MaxItems + 1 }},
		{"zero-commit", func(o *CollectionItemOutcome) { o.CommittedIndex, o.Receipt.CommittedIndex = 0, 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := accepted.Clone()
			tc.change(&o)
			if raw, err := collectionExecutionEncoding(collectionExecutionRecord{Version: 1, Outcome: &o}); !errors.Is(err, ErrCollectionInvalid) || raw != nil {
				t.Fatalf("invalid outcome encoded: %v", err)
			}
		})
	}
}

func TestCollectionExecutionRecordPreparedBindingAndClones(t *testing.T) {
	p, accepted := executionRecordFixture(t)
	p.Record.References = []CatalogKey{{Kind: "NotificationEndpoint", ID: "endpoint"}}
	copy := p.Clone()
	copy.Record.Payload.WrappedKey[0] ^= 0xff
	copy.Record.Payload.Nonce[0] ^= 0xff
	copy.Record.Payload.Ciphertext[0] ^= 0xff
	copy.Record.References[0].ID = "replacement"
	if bytes.Equal(copy.Record.Payload.WrappedKey, p.Record.Payload.WrappedKey) || bytes.Equal(copy.Record.Payload.Nonce, p.Record.Payload.Nonce) ||
		bytes.Equal(copy.Record.Payload.Ciphertext, p.Record.Payload.Ciphertext) || p.Record.References[0].ID != "endpoint" {
		t.Fatal("prepared clone aliases borrowed mutable memory")
	}
	clone := accepted.Clone()
	clone.Receipt.Actor = "replacement"
	if accepted.Receipt.Actor != "operator" {
		t.Fatal("outcome clone aliases original receipt")
	}
	for _, tc := range []struct {
		name   string
		change func(*CollectionItemOutcome)
	}{
		{"prepared-id", func(o *CollectionItemOutcome) { o.PreparedID = "66666666-6666-4666-8666-666666666666" }},
		{"upload", func(o *CollectionItemOutcome) { o.Binding.UploadID = "66666666-6666-4666-8666-666666666666" }},
		{"ordinal", func(o *CollectionItemOutcome) { o.Ordinal++ }},
		{"input-ordinal", func(o *CollectionItemOutcome) { o.InputOrdinal++ }},
		{"row-digest", func(o *CollectionItemOutcome) { o.RowDigest = strings.Repeat("c", 64) }},
		{"key", func(o *CollectionItemOutcome) { o.Key.ID, o.Receipt.Key.ID = "other", "other" }},
		{"incarnation", func(o *CollectionItemOutcome) { o.UID, o.Receipt.UID = "other", "other" }},
		{"version", func(o *CollectionItemOutcome) { o.Revision, o.Receipt.NewVersion = "other", "other" }},
		{"generation", func(o *CollectionItemOutcome) { o.Generation++; o.Receipt.Generation++ }},
		{"before-prepared", func(o *CollectionItemOutcome) {
			o.At = p.At.Add(-time.Nanosecond)
			o.Receipt.At, o.Receipt.UpdatedAt = o.At, o.At
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := accepted.Clone()
			tc.change(&o)
			if o.validate() != nil {
				t.Fatal("mismatch fixture should remain structurally valid")
			}
			if p.matchesOutcome(o) {
				t.Fatal("different prepared identity matched")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*CollectionPreparedItem)
	}{
		{"committed-candidate", func(p *CollectionPreparedItem) { p.Record.CommittedIndex = 1 }},
		{"reverse-token", func(p *CollectionPreparedItem) { p.Record.DependentsVersion = 1 }},
		{"future-candidate", func(p *CollectionPreparedItem) { p.Record.UpdatedAt = p.At.Add(time.Second) }},
		{"tombstone", func(p *CollectionPreparedItem) {
			p.Record.Removed, p.Record.Payload, p.Record.References = true, secureconfig.Envelope{}, nil
		}},
		{"bad-ciphertext", func(p *CollectionPreparedItem) { p.Record.Payload.Ciphertext = nil }},
		{"bad-binding", func(p *CollectionPreparedItem) { p.Binding.PlanDigest = strings.Repeat("A", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := p.Clone()
			tc.change(&changed)
			if changed.validate() == nil {
				t.Fatal("invalid prepared record accepted")
			}
		})
	}
}
