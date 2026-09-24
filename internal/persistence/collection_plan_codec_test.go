package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func planCodecHeader(count uint64) CollectionPlanHeader {
	return CollectionPlanHeader{CodecVersion: CollectionPlanCodecVersion, CompilerVersion: CollectionPlanCompilerVersion,
		PlanID: "322deb62-4e55-4c26-a0bd-9f3cb3bd8f89", OperationID: operationHandle("5ef3c1bc-6b7e-489d-b4d9-fc5a0d2ab72a", 1),
		UploadID: "1a7283c1-9cac-4dfe-954d-ebac945efbdc", Actor: "operator", IdentityFormat: commitment.Format,
		ContentDigest: strings.Repeat("a", 64), InputProgressDigest: strings.Repeat("b", 64), ItemCount: count, ObservedIndex: 17}
}

func planCodecRow(ordinal, input uint64, key CatalogKey, change string) CollectionPlanRow {
	zero := uint64(0)
	guard := CollectionPlanGuard{Key: key, OriginalUID: "original-uid", OriginalRevision: "original-revision", OriginalGeneration: 1, ReverseVersion: &zero}
	if change == "create" {
		guard = CollectionPlanGuard{Key: key, Absent: true}
	}
	return CollectionPlanRow{Ordinal: ordinal, InputOrdinal: input, Source: "source.00000000000000000001", Document: 1,
		Item: input, Key: key, Change: change, Target: guard}
}

func planCodecFixture(e *CollectionPlanEncoder) error {
	credential := CatalogKey{Kind: "Credential", ID: "credential"}
	endpoint := CatalogKey{Kind: "NotificationEndpoint", ID: "endpoint"}
	monitor := CatalogKey{Kind: "Monitor", ID: "monitor"}
	if err := e.BeginRow(planCodecRow(1, 3, credential, "create")); err != nil {
		return err
	}
	if err := e.EndRow(); err != nil {
		return err
	}
	second := planCodecRow(2, 1, endpoint, "update")
	second.GuardCount, second.RequiresCount, second.TouchesCount = 1, 1, 1
	if err := e.BeginRow(second); err != nil {
		return err
	}
	if err := e.WriteGuards([]CollectionPlanGuard{{Key: credential, Absent: true, FromOrdinal: 1}}); err != nil {
		return err
	}
	if err := e.WriteRequires([]uint64{1}); err != nil {
		return err
	}
	if err := e.WriteTouches([]CatalogKey{credential}); err != nil {
		return err
	}
	if err := e.EndRow(); err != nil {
		return err
	}
	third := planCodecRow(3, 2, monitor, "unchanged")
	third.GuardCount, third.RequiresCount = 2, 2
	if err := e.BeginRow(third); err != nil {
		return err
	}
	if err := e.WriteGuards([]CollectionPlanGuard{{Key: credential, Absent: true, FromOrdinal: 1},
		{Key: endpoint, OriginalUID: "old-endpoint", OriginalRevision: "old-version", OriginalGeneration: 1, FromOrdinal: 2}}); err != nil {
		return err
	}
	if err := e.WriteRequires([]uint64{1, 2}); err != nil {
		return err
	}
	return e.EndRow()
}

func planCodecBytes(t *testing.T) ([]byte, CollectionPlanDescriptor) {
	t.Helper()
	var b bytes.Buffer
	d, err := EncodeCollectionPlan(context.Background(), &b, planCodecHeader(3), planCodecFixture)
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), d
}

func TestCollectionPlanCodecRoundTripAndDescriptor(t *testing.T) {
	raw, descriptor := planCodecBytes(t)
	dry, err := EncodeCollectionPlan(context.Background(), io.Discard, planCodecHeader(3), planCodecFixture)
	if err != nil || dry != descriptor {
		t.Fatalf("dry descriptor=%+v err=%v", dry, err)
	}
	hash := sha256.Sum256(raw)
	if descriptor.Bytes != uint64(len(raw)) || descriptor.Digest != hex.EncodeToString(hash[:]) {
		t.Fatal("descriptor does not cover exact complete artifact")
	}
	var kinds []string
	decoded, err := DecodeCollectionPlan(context.Background(), bytes.NewReader(raw), func(f CollectionPlanFragment) error {
		kinds = append(kinds, f.Kind)
		if f.Header != nil {
			f.Header.ItemCount = 999
		} // Callback cannot mutate retained parsing state.
		if f.Row != nil {
			f.Row.GuardCount = 999
		}
		if len(f.Guards) > 0 {
			f.Guards[0].FromOrdinal = 999
		}
		return nil
	})
	if err != nil || decoded != descriptor {
		t.Fatalf("decode=%+v err=%v", decoded, err)
	}
	want := []string{"header", "row", "end", "row", "guards", "requires", "touches", "end", "row", "guards", "requires", "end", "footer"}
	if !reflect.DeepEqual(kinds, want) || descriptor.Fragments != uint64(len(want)) {
		t.Fatalf("fragments=%v descriptor=%+v", kinds, descriptor)
	}
	for _, mutate := range []func(*CollectionPlanHeader){func(h *CollectionPlanHeader) { h.PlanID = "abc82201-d625-4d7e-8b5a-3e9a3c607a71" },
		func(h *CollectionPlanHeader) { h.ContentDigest = strings.Repeat("c", 64) }, func(h *CollectionPlanHeader) { h.InputProgressDigest = strings.Repeat("d", 64) },
		func(h *CollectionPlanHeader) { h.Actor = "another" }, func(h *CollectionPlanHeader) { h.UploadID = "b360ae4e-0d1d-41ae-bc67-c18b7c9d7c70" }} {
		h := planCodecHeader(3)
		mutate(&h)
		d, err := EncodeCollectionPlan(context.Background(), io.Discard, h, planCodecFixture)
		if err != nil || d == descriptor {
			t.Fatalf("identity did not affect descriptor: %+v %v", d, err)
		}
	}
}

func planCodecFrames(t *testing.T, raw []byte) [][]byte {
	t.Helper()
	raw = raw[len(collectionPlanMagic):]
	var result [][]byte
	for len(raw) > 0 {
		if len(raw) < 4 {
			t.Fatal("test frame truncated")
		}
		n := int(binary.BigEndian.Uint32(raw[:4]))
		raw = raw[4:]
		if n > len(raw) {
			t.Fatal("test frame truncated")
		}
		result = append(result, bytes.Clone(raw[:n]))
		raw = raw[n:]
	}
	return result
}

func planCodecFrameStream(frames [][]byte) []byte {
	var b bytes.Buffer
	b.WriteString(collectionPlanMagic)
	for _, frame := range frames {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(frame)))
		b.Write(n[:])
		b.Write(frame)
	}
	return b.Bytes()
}

func planCodecMutateFrame(t *testing.T, frames [][]byte, index int, change func(*CollectionPlanFragment)) {
	t.Helper()
	var f CollectionPlanFragment
	if err := json.Unmarshal(frames[index], &f); err != nil {
		t.Fatal(err)
	}
	change(&f)
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	frames[index] = raw
}

func TestCollectionPlanCodecRejectsMalformedStreams(t *testing.T) {
	valid, _ := planCodecBytes(t)
	// Assert the structural boundary itself, not eventual failure of a checksum
	// left over from the valid fixture. The invalid frame must never be delivered.
	rejectedAt := map[string]int{
		"wrong-codec": 0, "wrong-compiler": 0, "repeated-header": 1, "missing-row": 1,
		"duplicate-input": 3, "duplicate-key": 3, "untrusted-source-path": 3, "wrong-target": 3,
		"self-predecessor": 4, "wrong-predecessor-key": 4, "requires-after-touches": 5,
		"unsorted-guards": 9, "duplicate-requires": 10, "bad-row-footer": 2,
		"bad-final-count": 12, "bad-final-bytes": 12, "bad-final-digest": 12,
		"extra-kind-payload": 0, "provider-field": 0, "duplicate-json-member": 0,
		"case-aliased-member": 0, "noncanonical-whitespace": 0,
		"array-before-allocation": 4, "nested-before-allocation": 0,
	}
	cases := []struct {
		name   string
		mutate func([][]byte) [][]byte
	}{
		{"wrong-codec", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 0, func(v *CollectionPlanFragment) { v.Header.CodecVersion++ })
			return f
		}},
		{"wrong-compiler", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 0, func(v *CollectionPlanFragment) { v.Header.CompilerVersion = "future" })
			return f
		}},
		{"repeated-header", func(f [][]byte) [][]byte { return append(f[:1], append([][]byte{f[0]}, f[1:]...)...) }},
		{"missing-row", func(f [][]byte) [][]byte { return append(f[:1], f[3:]...) }},
		{"duplicate-input", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 3, func(v *CollectionPlanFragment) { v.Row.InputOrdinal = 3 })
			return f
		}},
		{"duplicate-key", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 3, func(v *CollectionPlanFragment) {
				v.Row.Key = CatalogKey{"Credential", "credential"}
				v.Row.Target.Key = v.Row.Key
			})
			return f
		}},
		{"untrusted-source-path", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 3, func(v *CollectionPlanFragment) { v.Row.Source = "/private/secrets.yaml" })
			return f
		}},
		{"wrong-target", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 3, func(v *CollectionPlanFragment) { v.Row.Target.Key.ID = "elsewhere" })
			return f
		}},
		{"self-predecessor", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 4, func(v *CollectionPlanFragment) { v.Guards[0].FromOrdinal = 2 })
			return f
		}},
		{"wrong-predecessor-key", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 4, func(v *CollectionPlanFragment) { v.Guards[0].Key.ID = "other" })
			return f
		}},
		{"requires-after-touches", func(f [][]byte) [][]byte { f[5], f[6] = f[6], f[5]; return f }},
		{"unsorted-guards", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 9, func(v *CollectionPlanFragment) { v.Guards[0], v.Guards[1] = v.Guards[1], v.Guards[0] })
			return f
		}},
		{"duplicate-requires", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 10, func(v *CollectionPlanFragment) { v.Requires = []uint64{1, 1} })
			return f
		}},
		{"bad-row-footer", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 2, func(v *CollectionPlanFragment) { v.End.Digest = strings.Repeat("f", 64) })
			return f
		}},
		{"bad-final-count", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 12, func(v *CollectionPlanFragment) { v.Footer.Rows++ })
			return f
		}},
		{"bad-final-bytes", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 12, func(v *CollectionPlanFragment) { v.Footer.Bytes++ })
			return f
		}},
		{"bad-final-digest", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 12, func(v *CollectionPlanFragment) { v.Footer.Digest = strings.Repeat("f", 64) })
			return f
		}},
		{"extra-kind-payload", func(f [][]byte) [][]byte {
			planCodecMutateFrame(t, f, 0, func(v *CollectionPlanFragment) { v.Requires = []uint64{1} })
			return f
		}},
		{"provider-field", func(f [][]byte) [][]byte {
			f[0] = bytes.Replace(f[0], []byte(`"kind":"header"`), []byte(`"kind":"header","provider":"private-token"`), 1)
			return f
		}},
		{"duplicate-json-member", func(f [][]byte) [][]byte {
			f[0] = bytes.Replace(f[0], []byte(`"kind":"header"`), []byte(`"kind":"header","kind":"header"`), 1)
			return f
		}},
		{"case-aliased-member", func(f [][]byte) [][]byte {
			f[0] = bytes.Replace(f[0], []byte(`"codec_version":1`), []byte(`"Codec_Version":1`), 1)
			return f
		}},
		{"noncanonical-whitespace", func(f [][]byte) [][]byte { f[0] = append([]byte(" "), f[0]...); return f }},
		{"array-before-allocation", func(f [][]byte) [][]byte {
			f[4] = []byte(`{"kind":"guards","guards":[` + strings.Repeat("null,", 256) + `null]}`)
			return f
		}},
		{"nested-before-allocation", func(f [][]byte) [][]byte {
			f[0] = []byte(strings.Repeat("[", 10) + "0" + strings.Repeat("]", 10))
			return f
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			malformed := planCodecFrameStream(tc.mutate(planCodecFrames(t, valid)))
			accepted := 0
			d, err := DecodeCollectionPlan(context.Background(), bytes.NewReader(malformed), func(CollectionPlanFragment) error { accepted++; return nil })
			if err == nil || d != (CollectionPlanDescriptor{}) {
				t.Fatalf("invalid stream accepted: %+v %v", d, err)
			}
			if expected, exists := rejectedAt[tc.name]; !exists || accepted != expected {
				t.Fatalf("accepted %d frames, want exactly %d before structural rejection (case declared=%v)", accepted, expected, exists)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secrets.yaml") {
				t.Fatal("private input in error")
			}
		})
	}
	for _, cut := range []int{0, 1, len(collectionPlanMagic), len(collectionPlanMagic) + 3, len(valid) - 1, len(valid) - 20} {
		d, err := DecodeCollectionPlan(context.Background(), bytes.NewReader(valid[:cut]), func(CollectionPlanFragment) error { return nil })
		if err == nil || d != (CollectionPlanDescriptor{}) {
			t.Fatalf("truncation %d accepted", cut)
		}
	}
	for _, suffix := range [][]byte{{0}, []byte("\n"), valid} {
		d, err := DecodeCollectionPlan(context.Background(), io.MultiReader(bytes.NewReader(valid), bytes.NewReader(suffix)), func(CollectionPlanFragment) error { return nil })
		if err == nil || d != (CollectionPlanDescriptor{}) {
			t.Fatal("trailing data accepted")
		}
	}
}

type planCodecCountingWriter struct{ bytes uint64 }

func (w *planCodecCountingWriter) Write(p []byte) (int, error) {
	w.bytes += uint64(len(p))
	return len(p), nil
}

func planCodecManyGuards(count int) func(*CollectionPlanEncoder) error {
	return func(e *CollectionPlanEncoder) error {
		row := planCodecRow(1, 1, CatalogKey{"Monitor", "large"}, "create")
		row.GuardCount = uint64(count)
		if err := e.BeginRow(row); err != nil {
			return err
		}
		for at := 0; at < count; {
			chunk := make([]CollectionPlanGuard, 0, CollectionPlanMaxChunkEntries)
			for len(chunk) < cap(chunk) && at < count {
				chunk = append(chunk, CollectionPlanGuard{Key: CatalogKey{"Credential", fmt.Sprintf("credential-%08d", at)},
					OriginalUID: strings.Repeat("u", 256), OriginalRevision: strings.Repeat("v", 256), OriginalGeneration: 1})
				at++
			}
			if err := e.WriteGuards(chunk); err != nil {
				return err
			}
		}
		return e.EndRow()
	}
}

func TestCollectionPlanCodecStreamsRowLargerThanCommand(t *testing.T) {
	var b bytes.Buffer
	d, err := EncodeCollectionPlan(context.Background(), &b, planCodecHeader(1), planCodecManyGuards(7000))
	if err != nil || d.Bytes <= 4<<20 {
		t.Fatalf("large row bytes=%d err=%v", d.Bytes, err)
	}
	var guards int
	decoded, err := DecodeCollectionPlan(context.Background(), bytes.NewReader(b.Bytes()), func(f CollectionPlanFragment) error {
		if len(f.Guards) > CollectionPlanMaxChunkEntries {
			t.Fatal("unbounded chunk")
		}
		guards += len(f.Guards)
		return nil
	})
	if err != nil || decoded != d || guards != 7000 {
		t.Fatalf("large stream=%+v guards=%d err=%v", decoded, guards, err)
	}
	for _, frame := range planCodecFrames(t, b.Bytes()) {
		if len(frame) > CollectionPlanMaxFragmentBytes {
			t.Fatal("unbounded frame")
		}
	}
	w := &planCodecCountingWriter{}
	d, err = EncodeCollectionPlan(context.Background(), w, planCodecHeader(1), planCodecManyGuards(60_000))
	if !errors.Is(err, ErrCollectionPlanLimit) || d != (CollectionPlanDescriptor{}) || w.bytes > CollectionPlanMaxBytes {
		t.Fatalf("quota bytes=%d descriptor=%+v err=%v", w.bytes, d, err)
	}
}

func TestCollectionPlanCodecRejectsEncoderMisuse(t *testing.T) {
	for name, emit := range map[string]func(*CollectionPlanEncoder) error{
		"missing-rows":    func(*CollectionPlanEncoder) error { return nil },
		"empty-chunk":     func(e *CollectionPlanEncoder) error { _ = e.WriteGuards(nil); return nil },
		"oversized-chunk": func(e *CollectionPlanEncoder) error { _ = e.WriteRequires(make([]uint64, 257)); return nil },
		"unfinished-row": func(e *CollectionPlanEncoder) error {
			return e.BeginRow(planCodecRow(1, 1, CatalogKey{"Monitor", "one"}, "create"))
		},
		"missing-required-own-predecessor": func(e *CollectionPlanEncoder) error {
			key := CatalogKey{"Credential", "first"}
			if err := e.BeginRow(planCodecRow(1, 1, key, "create")); err != nil {
				return err
			}
			if err := e.EndRow(); err != nil {
				return err
			}
			row := planCodecRow(2, 2, CatalogKey{"Monitor", "second"}, "create")
			row.GuardCount = 1
			if err := e.BeginRow(row); err != nil {
				return err
			}
			if err := e.WriteGuards([]CollectionPlanGuard{{Key: key, Absent: true, FromOrdinal: 1}}); err != nil {
				return err
			}
			return e.EndRow()
		},
		"missing-required-unchanged-predecessor": func(e *CollectionPlanEncoder) error {
			key := CatalogKey{"Credential", "first"}
			first := planCodecRow(1, 1, key, "unchanged")
			if err := e.BeginRow(first); err != nil {
				return err
			}
			if err := e.EndRow(); err != nil {
				return err
			}
			row := planCodecRow(2, 2, CatalogKey{"Monitor", "second"}, "create")
			row.GuardCount = 1
			if err := e.BeginRow(row); err != nil {
				return err
			}
			if err := e.WriteGuards([]CollectionPlanGuard{first.Target}); err != nil {
				return err
			}
			return e.EndRow()
		},
		"missing-target-reverse-guard": func(e *CollectionPlanEncoder) error {
			row := planCodecRow(1, 1, CatalogKey{"Monitor", "first"}, "update")
			row.Target.ReverseVersion = nil
			if err := e.BeginRow(row); err != nil {
				return err
			}
			if err := e.EndRow(); err != nil {
				return err
			}
			if err := e.BeginRow(planCodecRow(2, 2, CatalogKey{"Monitor", "second"}, "create")); err != nil {
				return err
			}
			return e.EndRow()
		},
		"changed-predecessor-with-original-only-guard": func(e *CollectionPlanEncoder) error {
			key := CatalogKey{"Credential", "first"}
			first := planCodecRow(1, 1, key, "update")
			if err := e.BeginRow(first); err != nil {
				return err
			}
			if err := e.EndRow(); err != nil {
				return err
			}
			row := planCodecRow(2, 2, CatalogKey{"Monitor", "second"}, "create")
			row.GuardCount, row.RequiresCount = 1, 1
			if err := e.BeginRow(row); err != nil {
				return err
			}
			if err := e.WriteGuards([]CollectionPlanGuard{first.Target}); err != nil {
				return err
			}
			if err := e.WriteRequires([]uint64{1}); err != nil {
				return err
			}
			return e.EndRow()
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := EncodeCollectionPlan(context.Background(), io.Discard, planCodecHeader(2), emit)
			if err == nil || d != (CollectionPlanDescriptor{}) {
				t.Fatalf("misuse accepted %+v %v", d, err)
			}
		})
	}
	var escaped *CollectionPlanEncoder
	_, err := EncodeCollectionPlan(context.Background(), io.Discard, planCodecHeader(3), func(e *CollectionPlanEncoder) error { escaped = e; return planCodecFixture(e) })
	if err != nil || !errors.Is(escaped.EndRow(), ErrCollectionPlanInvalid) || !errors.Is(escaped.WriteTouches(nil), ErrCollectionPlanInvalid) {
		t.Fatal("encoder usable outside callback")
	}
	var zero CollectionPlanEncoder
	if !errors.Is(zero.BeginRow(CollectionPlanRow{}), ErrCollectionPlanInvalid) || !errors.Is(zero.EndRow(), ErrCollectionPlanInvalid) {
		t.Fatal("zero encoder did not fail safely")
	}
	d, err := EncodeCollectionPlan(context.Background(), io.Discard, planCodecHeader(3), func(e *CollectionPlanEncoder) error {
		if err := planCodecFixture(e); err != nil {
			return err
		}
		// All rows are already complete, so footer validation would succeed if
		// the encoder incorrectly forgot this deliberately ignored error.
		if err := e.WriteRequires([]uint64{1}); err == nil {
			t.Fatal("invalid chunk outside row accepted")
		}
		return nil
	})
	if err == nil || d != (CollectionPlanDescriptor{}) {
		t.Fatal("ignored error was not sticky after a complete valid plan")
	}
	_, err = EncodeCollectionPlan(context.Background(), io.Discard, planCodecHeader(1), func(e *CollectionPlanEncoder) error {
		row := planCodecRow(1, 1, CatalogKey{"Monitor", "target"}, "update")
		row.Target.ReverseVersion = nil
		err := e.BeginRow(row)
		if !errors.Is(err, ErrCollectionPlanInvalid) {
			t.Fatal("missing target reverse token was not rejected at BeginRow")
		}
		return err
	})
	if !errors.Is(err, ErrCollectionPlanInvalid) {
		t.Fatal("target failure was not preserved")
	}
}

type planCodecShortWriter struct{}

func (planCodecShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestCollectionPlanCodecCancellationAndIOErrorsHaveNoDescriptor(t *testing.T) {
	sentinel := errors.New("caller stopped")
	for name, writer := range map[string]io.Writer{"short": planCodecShortWriter{}, "failing": planCodecErrorWriter{sentinel}} {
		t.Run(name, func(t *testing.T) {
			d, err := EncodeCollectionPlan(context.Background(), writer, planCodecHeader(3), planCodecFixture)
			if err == nil || d != (CollectionPlanDescriptor{}) {
				t.Fatal("failed write returned descriptor")
			}
		})
	}
	d, err := EncodeCollectionPlan(context.Background(), io.Discard, planCodecHeader(3), func(*CollectionPlanEncoder) error { return sentinel })
	if !errors.Is(err, sentinel) || d != (CollectionPlanDescriptor{}) {
		t.Fatal("callback error lost")
	}
	ctx, cancel := context.WithCancel(context.Background())
	d, err = EncodeCollectionPlan(ctx, io.Discard, planCodecHeader(3), func(e *CollectionPlanEncoder) error { cancel(); return planCodecFixture(e) })
	if !errors.Is(err, context.Canceled) || d != (CollectionPlanDescriptor{}) {
		t.Fatal("encode cancellation lost")
	}
	raw, _ := planCodecBytes(t)
	ctx, cancel = context.WithCancel(context.Background())
	d, err = DecodeCollectionPlan(ctx, bytes.NewReader(raw), func(f CollectionPlanFragment) error {
		if f.Kind == "footer" {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || d != (CollectionPlanDescriptor{}) {
		t.Fatal("late decode cancellation lost")
	}
	d, err = DecodeCollectionPlan(context.Background(), bytes.NewReader(raw), func(CollectionPlanFragment) error { return sentinel })
	if !errors.Is(err, sentinel) || d != (CollectionPlanDescriptor{}) {
		t.Fatal("decode callback error lost")
	}
	// A declared oversized frame is rejected before the decoder tries to read
	// any frame bytes; this reader panics on any read beyond the prefix.
	var prefix bytes.Buffer
	prefix.WriteString(collectionPlanMagic)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], CollectionPlanMaxFragmentBytes+1)
	prefix.Write(size[:])
	d, err = DecodeCollectionPlan(context.Background(), io.MultiReader(bytes.NewReader(prefix.Bytes()), planCodecPanicReader{}), func(CollectionPlanFragment) error { return nil })
	if !errors.Is(err, ErrCollectionPlanLimit) || d != (CollectionPlanDescriptor{}) {
		t.Fatal("frame allocation bound missing")
	}
}

type planCodecErrorWriter struct{ err error }

func (w planCodecErrorWriter) Write([]byte) (int, error) { return 0, w.err }

type planCodecPanicReader struct{}

func (planCodecPanicReader) Read([]byte) (int, error) { panic("read beyond bounded prefix") }
