package collection

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestDecodeMatchesFrozenManifestAndTypedResources(t *testing.T) {
	for _, input := range []string{
		"monitors:\n  - name: service\n    pulse_check:\n      type: http\n      interval: 60s\n      timeout: 5s\n      config:\n        url: https://example.test\n",
		`{"apiVersion":"cpra.io/v2","kind":"Recipient","metadata":{"id":"alice"},"spec":{"endpointRefs":["mail"]}}`,
	} {
		var decoded []Item
		err := Decode(context.Background(), strings.NewReader(input), DecodeOptions{}, func(item Item) error { decoded = append(decoded, item); return nil })
		if err != nil {
			t.Fatal(err)
		}
		frozen, err := Freeze(context.Background(), []Source{Reader("reader", strings.NewReader(input))}, Options{TempDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		defer frozen.Close()
		if len(decoded) != frozen.Len() {
			t.Fatal("streaming parser changed item count")
		}
		for i, got := range decoded {
			want, err := frozen.Item(context.Background(), i)
			if err != nil || got.ID != want.ID || got.Location != want.Location || !reflect.DeepEqual(got.Resource, want.Resource) {
				t.Fatalf("parser paths differ: %#v %#v %v", got, want, err)
			}
			if got.ContentDigest == want.ContentDigest || want.Position.Ordinal != uint64(i+1) {
				t.Fatal("local decoder digest was confused with a keyed frozen inventory")
			}
		}
	}
}

func TestDecodeNeverStagesPlaintextAndPropagatesPartialFailure(t *testing.T) {
	private := t.TempDir()
	t.Setenv("TMPDIR", private)
	t.Setenv("TMP", private)
	t.Setenv("TEMP", private)
	one := `{"apiVersion":"cpra.io/v2","kind":"Credential","metadata":{"id":"key"},"spec":{"value":"private-value"}}`
	visits := 0
	if err := Decode(context.Background(), strings.NewReader("["+one+",{bad]"), DecodeOptions{}, func(item Item) error { visits++; return nil }); err == nil || visits != 1 {
		t.Fatal("late parse error boundary lost", err, visits)
	}
	entries, err := os.ReadDir(private)
	if err != nil || len(entries) != 0 {
		t.Fatal("streaming decode wrote plaintext files", err)
	}
	if err := Decode(context.Background(), strings.NewReader("["+one+","+one+"]"), DecodeOptions{}, func(Item) error { return nil }); err == nil {
		t.Fatal("duplicate identities accepted")
	}
	want := errors.New("encrypted staging full")
	if err := Decode(context.Background(), strings.NewReader(one), DecodeOptions{}, func(Item) error { return want }); !errors.Is(err, want) {
		t.Fatal("sink failure lost", err)
	}
}

func TestDecodeBoundsTrailingInputAndCancellation(t *testing.T) {
	one := `{"apiVersion":"cpra.io/v2","kind":"Recipient","metadata":{"id":"alice"},"spec":{"endpointRefs":["mail"]}}`
	for _, input := range []string{one + strings.Repeat(" ", 1024), strings.Repeat(" ", 1024)} {
		if err := Decode(context.Background(), strings.NewReader(input), DecodeOptions{MaxBytes: 512}, func(Item) error { return nil }); err == nil {
			t.Fatal("oversized trailing whitespace ignored")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Decode(ctx, strings.NewReader(one), DecodeOptions{}, func(Item) error { t.Fatal("canceled callback ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDecodeNormalizesManifestZeroWithoutChangingTypedZero(t *testing.T) {
	manifest := `{"monitors":[{"id":"service","pulse_check":{"type":"http","interval":"60s","timeout":"5s","retries":3,"max_failures":4,"unhealthy_threshold":0,"config":{"url":"https://example.test","retries":0}}}]}`
	typed := `{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"service"},"spec":{"check":{"interval":"60s","timeout":"5s","retries":3,"maxFailures":4,"unhealthyThreshold":0,"driver":{"type":"http","config":{"url":"https://example.test","retries":0}}}}}`
	for _, test := range []struct {
		input              string
		retries, threshold int64
	}{{manifest, 3, 4}, {typed, 0, 0}} {
		err := Decode(context.Background(), strings.NewReader(test.input), DecodeOptions{}, func(item Item) error {
			var spec struct {
				Check struct {
					UnhealthyThreshold int64 `json:"unhealthyThreshold"`
					Driver             struct {
						Config struct {
							Retries int64 `json:"retries"`
						} `json:"config"`
					} `json:"driver"`
				} `json:"check"`
			}
			if err := json.Unmarshal(item.Resource.Spec, &spec); err != nil {
				return err
			}
			if spec.Check.UnhealthyThreshold != test.threshold || spec.Check.Driver.Config.Retries != test.retries {
				t.Fatal("wrong manifest/v2 zero semantics")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDecodeYAMLLateFailureAndResourceBounds(t *testing.T) {
	one := "apiVersion: cpra.io/v2\nkind: Recipient\nmetadata:\n  id: alice\nspec:\n  endpointRefs: [mail]\n"
	visits := 0
	if err := Decode(context.Background(), strings.NewReader(one+"---\nspec: [\n"), DecodeOptions{}, func(Item) error { visits++; return nil }); err == nil || visits != 1 {
		t.Fatal("late YAML error lost previously staged boundary", err, visits)
	}
	for _, syntax := range []string{one + "---\n" + strings.Replace(one, "alice", "bob", 1),
		`[{"apiVersion":"cpra.io/v2","kind":"Recipient","metadata":{"id":"alice"},"spec":{"endpointRefs":["mail"]}},{"apiVersion":"cpra.io/v2","kind":"Recipient","metadata":{"id":"bob"},"spec":{"endpointRefs":["mail"]}}]`} {
		visits = 0
		if err := Decode(context.Background(), strings.NewReader(syntax), DecodeOptions{MaxResources: 1}, func(Item) error { visits++; return nil }); err == nil || visits != 1 {
			t.Fatal("resource quota did not stop staging", err, visits)
		}
	}
	var resource api.Resource
	if err := Decode(context.Background(), strings.NewReader(one), DecodeOptions{}, func(item Item) error { resource = item.Resource; return nil }); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(resource)
	for _, limit := range []int{len(raw) - 1, len(raw)} {
		visits = 0
		err := Decode(context.Background(), strings.NewReader(one), DecodeOptions{MaxResourceBytes: limit}, func(Item) error { visits++; return nil })
		if (err == nil) != (limit == len(raw)) || visits != limit-(len(raw)-1) {
			t.Fatal("encoded resource boundary is not exact", limit, err, visits)
		}
	}
}
