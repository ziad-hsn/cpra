package commitment_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func key() []byte { return bytes.Repeat([]byte{0x71}, commitment.KeyBytes) }
func position() commitment.Position {
	token, _ := commitment.SourceToken(1)
	return commitment.Position{Ordinal: 1, ID: "Monitor/service-api", Source: commitment.SourcePosition{Token: token, Document: 1, Item: 1}}
}
func newItems(t *testing.T, count uint64) *commitment.Accumulator {
	t.Helper()
	a, err := commitment.NewAccumulator(key(), count, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func newSources(t *testing.T, count, quota uint64) *commitment.SourceAccumulator {
	t.Helper()
	s, err := commitment.NewSourceAccumulator(key(), count, quota)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFixedVectors(t *testing.T) {
	var vector struct {
		Format, KeyHex, SourceFingerprintHex, InventoryHex, EmptySourcesHex, EmptyInventoryHex string
		Sources                                                                                []struct{ Token, BytesHex string }
		Items                                                                                  []struct {
			Position         commitment.Position
			ResourceBytesHex string
			MAC              string
		}
	}
	// The vectors are generated independently with Python's hmac/hashlib, not by
	// recording outputs from this Go implementation.
	raw, err := os.ReadFile("testdata/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if vector.Format != commitment.Format {
		t.Fatal("unexpected vector format")
	}
	k, err := hex.DecodeString(vector.KeyHex)
	if err != nil {
		t.Fatal(err)
	}
	s, err := commitment.NewSourceAccumulator(k, uint64(len(vector.Sources)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range vector.Sources {
		data, err := hex.DecodeString(src.BytesHex)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Begin(src.Token); err != nil {
			t.Fatal(err)
		}
		for _, b := range data {
			if _, err = s.Write([]byte{b}); err != nil {
				t.Fatal(err)
			}
		}
		if err = s.End(); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(fingerprint[:]) != vector.SourceFingerprintHex {
		t.Fatal("source fingerprint vector mismatch")
	}
	a, err := commitment.NewAccumulator(k, uint64(len(vector.Items)), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range vector.Items {
		data, err := hex.DecodeString(item.ResourceBytesHex)
		if err != nil {
			t.Fatal(err)
		}
		mac, err := commitment.ItemMAC(k, item.Position, data)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(mac[:]) != item.MAC {
			t.Fatal("item MAC vector mismatch")
		}
		if err = commitment.VerifyItem(k, item.Position, data, mac[:]); err != nil {
			t.Fatal(err)
		}
		if err = a.Add(item.Position, mac); err != nil {
			t.Fatal(err)
		}
	}
	want, err := hex.DecodeString(vector.InventoryHex)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Verify(want); err != nil {
		t.Fatal(err)
	}
	emptySources, _ := commitment.NewSourceAccumulator(k, 0, 0)
	emptyFP, err := emptySources.Finish()
	if err != nil || hex.EncodeToString(emptyFP[:]) != vector.EmptySourcesHex {
		t.Fatal("empty source vector mismatch", err)
	}
	emptyItems, _ := commitment.NewAccumulator(k, 0, emptyFP)
	empty, err := emptyItems.Finish()
	if err != nil || hex.EncodeToString(empty[:]) != vector.EmptyInventoryHex {
		t.Fatal("empty item vector mismatch", err)
	}
}

func TestItemBoundsIdentityAndSecretSafeErrors(t *testing.T) {
	p := position()
	raw := []byte(`{"secret":"do-not-print-this-value"}`)
	for _, size := range []int{0, 1, 31, 33, 64} {
		bad := bytes.Repeat([]byte{'x'}, size)
		if _, err := commitment.ItemMAC(bad, p, raw); !errors.Is(err, commitment.ErrKey) {
			t.Fatal("wrong key length accepted")
		}
		if _, err := commitment.NewAccumulator(bad, 1, [32]byte{}); !errors.Is(err, commitment.ErrKey) {
			t.Fatal("wrong accumulator key accepted")
		}
		if _, err := commitment.NewSourceAccumulator(bad, 1, 10); !errors.Is(err, commitment.ErrKey) {
			t.Fatal("wrong source key accepted")
		}
	}
	for _, size := range []int{0, commitment.MaxResourceBytes + 1} {
		if _, err := commitment.ItemMAC(key(), p, make([]byte, size)); !errors.Is(err, commitment.ErrBounds) {
			t.Fatal("invalid resource bound accepted")
		}
	}
	if _, err := commitment.ItemMAC(key(), p, make([]byte, commitment.MaxResourceBytes)); err != nil {
		t.Fatal("boundary resource rejected", err)
	}
	for _, id := range []string{"", "Monitor", "Monitor/", "/service", "Monitor/a/b", "Monitor/../x", "Monitor/a?token=private", "Monitor/a\nprivate", "Monitor/a\x00", "Monitor/\xff", "Credential/.", "Credential/..", "Credential/a%20b", "Credential/a#b", "Credential/a\\b", strings.Repeat("A", 65) + "/x", "Monitor/" + strings.Repeat("a", 257)} {
		bad := p
		bad.ID = id
		if _, err := commitment.ItemMAC(key(), bad, raw); !errors.Is(err, commitment.ErrPosition) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "do-not-print") {
			t.Fatal("unsafe identity accepted or exposed", err)
		}
	}
	for _, id := range []string{"Credential/équipe@ops", "Recipient/Alice & Bob", "NotificationGroup/مناوبة", "Credential/" + strings.Repeat("é", 128)} {
		valid := p
		valid.ID = id
		if _, err := commitment.ItemMAC(key(), valid, raw); err != nil {
			t.Fatal("shared resource ID was restricted beyond the API envelope", err)
		}
	}
	tooLong := p
	tooLong.ID = "Credential/" + strings.Repeat("é", 129)
	if _, err := commitment.ItemMAC(key(), tooLong, raw); !errors.Is(err, commitment.ErrPosition) {
		t.Fatal("identity limit counted runes rather than bytes")
	}
	for _, change := range []func(*commitment.Position){
		func(p *commitment.Position) { p.Ordinal = 0 }, func(p *commitment.Position) { p.Ordinal = math.MaxUint64 },
		func(p *commitment.Position) { p.Source.Token = "https://secret.invalid/path?token=private" }, func(p *commitment.Position) { p.Source.Token = "source.1" },
		func(p *commitment.Position) { p.Source.Token = "source.00000000000000000000" }, func(p *commitment.Position) { p.Source.Token = "source.18446744073709551616" },
		func(p *commitment.Position) { p.Source.Document = 0 }, func(p *commitment.Position) { p.Source.Item = math.MaxUint64 },
	} {
		bad := p
		change(&bad)
		if _, err := commitment.ItemMAC(key(), bad, raw); !errors.Is(err, commitment.ErrPosition) {
			t.Fatal("invalid coordinate accepted")
		}
	}
	for _, n := range []uint64{0, commitment.MaxSources + 1, math.MaxUint64} {
		if _, err := commitment.SourceToken(n); err == nil {
			t.Fatal("invalid source ordinal accepted")
		}
	}
}

func TestMACRejectsChangedInputsAndCrossCollectionKeys(t *testing.T) {
	p := position()
	raw := []byte(`{"a":1,"b":"<>&"}`)
	mac, err := commitment.ItemMAC(key(), p, raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range [][]byte{nil, mac[:31], append(append([]byte{}, mac[:]...), 0), bytes.Repeat([]byte{1}, 32)} {
		if err = commitment.VerifyItem(key(), p, raw, candidate); !errors.Is(err, commitment.ErrMismatch) {
			t.Fatal("invalid MAC accepted", err)
		}
	}
	for _, change := range []func(*commitment.Position){func(p *commitment.Position) { p.Ordinal++ }, func(p *commitment.Position) { p.ID = "Monitor/other" }, func(p *commitment.Position) { p.Source.Document++ }, func(p *commitment.Position) { p.Source.Item++ }, func(p *commitment.Position) { p.Source.Token, _ = commitment.SourceToken(2) }} {
		other := p
		change(&other)
		if err = commitment.VerifyItem(key(), other, raw, mac[:]); !errors.Is(err, commitment.ErrMismatch) {
			t.Fatal("changed position accepted")
		}
	}
	if err = commitment.VerifyItem(key(), p, []byte(`{"b":"<>&","a":1}`), mac[:]); !errors.Is(err, commitment.ErrMismatch) {
		t.Fatal("semantic equality replaced exact bytes")
	}
	for range 4 {
		other := make([]byte, 32)
		if _, err = rand.Read(other); err != nil {
			t.Fatal(err)
		}
		if err = commitment.VerifyItem(other, p, raw, mac[:]); !errors.Is(err, commitment.ErrMismatch) {
			t.Fatal("cross-collection key accepted")
		}
	}
}

func TestInventoryBindsKeyCountSourceAndOrderedItems(t *testing.T) {
	p := position()
	item, err := commitment.ItemMAC(key(), p, []byte(`{"credential":"fictional"}`))
	if err != nil {
		t.Fatal(err)
	}
	finish := func(k []byte, count uint64, source [32]byte, changedItem bool) [32]byte {
		a, err := commitment.NewAccumulator(k, count, source)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		for n := uint64(1); n <= count; n++ {
			current := p
			current.Ordinal = n
			currentItem := item
			if changedItem {
				currentItem[0] ^= 1
			}
			if err = a.Add(current, currentItem); err != nil {
				t.Fatal(err)
			}
		}
		digest, err := a.Finish()
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	original := finish(key(), 1, [32]byte{}, false)
	otherKey := bytes.Repeat([]byte{0x73}, 32)
	for _, changed := range [][32]byte{
		finish(otherKey, 1, [32]byte{}, false), finish(key(), 0, [32]byte{}, false),
		finish(key(), 2, [32]byte{}, false), finish(key(), 1, [32]byte{1}, false),
		finish(key(), 1, [32]byte{}, true),
	} {
		if changed == original {
			t.Fatal("inventory failed to bind all inputs")
		}
	}
	for _, expected := range [][]byte{nil, original[:31], append(append([]byte{}, original[:]...), 0), bytes.Repeat([]byte{0x12}, 32)} {
		a := newItems(t, 1)
		if err = a.Add(p, item); err != nil {
			t.Fatal(err)
		}
		if err = a.Verify(expected); !errors.Is(err, commitment.ErrMismatch) {
			t.Fatal("invalid inventory MAC accepted", err)
		}
		if _, err = a.Finish(); err == nil {
			t.Fatal("failed verification could be finalized again")
		}
	}
}

func TestAccumulatorOrderCountAndFinalization(t *testing.T) {
	p := position()
	mac, _ := commitment.ItemMAC(key(), p, []byte(`{}`))
	var zero commitment.Accumulator
	if zero.Add(p, mac) == nil {
		t.Fatal("zero accumulator accepted Add")
	}
	if _, err := zero.Finish(); err == nil {
		t.Fatal("zero accumulator finalized")
	}
	for _, n := range []uint64{commitment.MaxItems + 1, math.MaxUint64} {
		if _, err := commitment.NewAccumulator(key(), n, [32]byte{}); !errors.Is(err, commitment.ErrBounds) {
			t.Fatal("oversized count accepted")
		}
	}
	for _, test := range []string{"early", "out-of-order", "duplicate-ordinal", "too-many", "invalid-position"} {
		t.Run(test, func(t *testing.T) {
			a := newItems(t, 1)
			switch test {
			case "early":
				if _, err := a.Finish(); err == nil {
					t.Fatal("early Finish succeeded")
				}
			case "out-of-order":
				bad := p
				bad.Ordinal = 2
				if a.Add(bad, mac) == nil {
					t.Fatal("out of order accepted")
				}
			case "duplicate-ordinal", "too-many":
				if err := a.Add(p, mac); err != nil {
					t.Fatal(err)
				}
				if a.Add(p, mac) == nil {
					t.Fatal("extra ordinal accepted")
				}
			case "invalid-position":
				bad := p
				bad.ID = "bad"
				if a.Add(bad, mac) == nil {
					t.Fatal("invalid position accepted")
				}
			}
			if _, err := a.Finish(); err == nil {
				t.Fatal("invalid accumulator finalized")
			}
			if a.Add(p, mac) == nil {
				t.Fatal("invalid accumulator resumed")
			}
		})
	}
	a := newItems(t, 1)
	if err := a.Add(p, mac); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Finish(); err != nil {
		t.Fatal(err)
	}
	if a.Add(p, mac) == nil {
		t.Fatal("Add after Finish succeeded")
	}
	if _, err := a.Finish(); err == nil {
		t.Fatal("Finish reused")
	}
	// Duplicate resource identity checking belongs to staging, not an unbounded
	// map hidden inside the cryptographic accumulator.
	a = newItems(t, 2)
	_ = a.Add(p, mac)
	p.Ordinal = 2
	if err := a.Add(p, mac); err != nil {
		t.Fatal("accumulator unexpectedly tracks resource IDs", err)
	}
	if _, err := a.Finish(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceBoundariesChunkingAndFailure(t *testing.T) {
	fingerprint := func(parts []string, split bool) [32]byte {
		s := newSources(t, uint64(len(parts)), 100)
		for i, part := range parts {
			token, _ := commitment.SourceToken(uint64(i + 1))
			if err := s.Begin(token); err != nil {
				t.Fatal(err)
			}
			if split {
				for _, b := range []byte(part) {
					if _, err := s.Write([]byte{b}); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if _, err := s.Write([]byte(part)); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.End(); err != nil {
				t.Fatal(err)
			}
		}
		result, err := s.Finish()
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if fingerprint([]string{"ab", "c"}, true) != fingerprint([]string{"ab", "c"}, false) {
		t.Fatal("chunk boundaries changed source identity")
	}
	if fingerprint([]string{"ab", "c"}, false) == fingerprint([]string{"a", "bc"}, false) {
		t.Fatal("ambiguous joined sources")
	}
	if fingerprint([]string{"ab", "c"}, false) == fingerprint([]string{"c", "ab"}, false) {
		t.Fatal("source order lost")
	}
	var zero commitment.SourceAccumulator
	if zero.Begin(position().Source.Token) == nil {
		t.Fatal("zero source accumulator opened")
	}
	if _, err := zero.Write(nil); err == nil {
		t.Fatal("zero Write accepted")
	}
	if zero.End() == nil {
		t.Fatal("zero End accepted")
	}
	if _, err := zero.Finish(); err == nil {
		t.Fatal("zero Finish accepted")
	}
	for _, count := range []uint64{commitment.MaxSources + 1, math.MaxUint64} {
		if _, err := commitment.NewSourceAccumulator(key(), count, 1); !errors.Is(err, commitment.ErrBounds) {
			t.Fatal("source count overflow")
		}
	}
	if _, err := commitment.NewSourceAccumulator(key(), 1, math.MaxUint64); !errors.Is(err, commitment.ErrBounds) {
		t.Fatal("byte quota overflow")
	}
	for _, test := range []string{"early", "active-finish", "duplicate-begin", "order", "quota", "no-source-write", "no-source-end"} {
		t.Run(test, func(t *testing.T) {
			s := newSources(t, 1, 2)
			token := position().Source.Token
			switch test {
			case "early":
				if _, err := s.Finish(); err == nil {
					t.Fatal("early finish")
				}
			case "active-finish":
				_ = s.Begin(token)
				if _, err := s.Finish(); err == nil {
					t.Fatal("active finish")
				}
			case "duplicate-begin":
				_ = s.Begin(token)
				if s.Begin(token) == nil {
					t.Fatal("duplicate begin")
				}
			case "order":
				next, _ := commitment.SourceToken(2)
				if s.Begin(next) == nil {
					t.Fatal("source order ignored")
				}
			case "quota":
				_ = s.Begin(token)
				if n, err := s.Write([]byte("abc")); n != 0 || !errors.Is(err, commitment.ErrBounds) {
					t.Fatal("quota ignored")
				}
			case "no-source-write":
				if _, err := s.Write(nil); err == nil {
					t.Fatal("write outside source")
				}
			case "no-source-end":
				if s.End() == nil {
					t.Fatal("end outside source")
				}
			}
			if _, err := s.Finish(); err == nil {
				t.Fatal("failed source accumulator finalized")
			}
		})
	}
	s := newSources(t, 1, 0)
	if err := s.Begin(position().Source.Token); err != nil {
		t.Fatal(err)
	}
	if err := s.End(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	if s.Begin(position().Source.Token) == nil {
		t.Fatal("source Begin after Finish")
	}
	if _, err := s.Write(nil); err == nil {
		t.Fatal("source Write after Finish")
	}
	if s.End() == nil {
		t.Fatal("source End after Finish")
	}
	if _, err := s.Finish(); err == nil {
		t.Fatal("source Finish repeated")
	}
}

func TestKeyOwnershipAndRedactedFormatting(t *testing.T) {
	k := key()
	s, err := commitment.NewSourceAccumulator(k, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	clear(k) // Constructor owns its copy; caller mutation must not change identity.
	other := newSources(t, 0, 0)
	one, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	two, err := other.Finish()
	if err != nil || one != two {
		t.Fatal("retained caller key alias")
	}
	a := newItems(t, 0)
	source := newSources(t, 1, 10)
	for _, value := range []any{a, *a, source, *source} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
			output := fmt.Sprintf(format, value)
			if !strings.Contains(output, "key and input omitted") || strings.Contains(output, strings.Repeat("q", 32)) || strings.Contains(output, hex.EncodeToString(key())) {
				t.Fatal("private state formatted", output)
			}
		}
		if _, err := json.Marshal(value); !errors.Is(err, commitment.ErrSerialization) {
			t.Fatal("private state serialized")
		}
	}
}
