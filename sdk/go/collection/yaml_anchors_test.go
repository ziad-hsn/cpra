package collection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestYAMLAnchorsOrderingAndCoordinates(t *testing.T) {
	var a YAMLAnchors
	parse := func(input string, start int) *yaml.Node {
		t.Helper()
		node, err := a.Parse([]byte(input), func(n int) int { return n + start - 1 })
		if err != nil {
			t.Fatal(err)
		}
		return node
	}
	parse("- config: &target {host: first, port: 443}\n", 10)
	root := parse("- before: *target\n  replacement: &target {host: second}\n  after: *target\n", 20)
	value, err := (&builder{}).decodeYAMLNode(root)
	if err != nil {
		t.Fatal(err)
	}
	got := value.([]any)[0].(map[string]any)
	if got["before"].(map[string]any)["host"] != "first" || got["after"].(map[string]any)["host"] != "second" {
		t.Fatal("anchor rebinding changed serialization order")
	}
	positions := yamlPositions(root, func(n int) int { return n })
	if positions["/0/before/host"].Line != 10 || positions["/0/after/host"].Line != 21 {
		t.Fatal("definition coordinates lost", positions)
	}
	root = parse("- config: *target # *unused\n  text: '*also_unused'\n", 30)
	value, err = (&builder{}).decodeYAMLNode(root)
	if err != nil || value.([]any)[0].(map[string]any)["config"].(map[string]any)["host"] != "second" {
		t.Fatal("retained anchor lost", err)
	}
	if a.nodes > 65536 || a.bytes > 1<<20 {
		t.Fatal("registry escaped quota")
	}
}

func TestYAMLAnchorsRejectForwardCyclesAndExpansion(t *testing.T) {
	for _, input := range []string{"before: *later\nlater: &later value\n", "cycle: &cycle [*cycle]\n", "one: *absent\n"} {
		var a YAMLAnchors
		_, err := a.Parse([]byte(input), func(n int) int { return n + 10 })
		var source *SourceError
		if !errors.As(err, &source) || strings.Contains(err.Error(), "absent") || len(a.values) != 0 {
			t.Fatal("unsafe alias accepted or exposed", err)
		}
	}
	var a YAMLAnchors
	if _, err := a.Parse([]byte("x: &x {one: two}\n"), nil); err != nil {
		t.Fatal(err)
	}
	a = YAMLAnchors{}
	if _, err := a.Parse([]byte("x: *x\n"), nil); err == nil {
		t.Fatal("cross-document alias accepted")
	}
	var input strings.Builder
	input.WriteString("x0: &x0 [a,a,a,a]\n")
	for n := 1; n < 12; n++ {
		fmt.Fprintf(&input, "x%d: &x%d [*x%d,*x%d,*x%d,*x%d]\n", n, n, n-1, n-1, n-1, n-1)
	}
	if _, err := a.Parse([]byte(input.String()), nil); err == nil {
		t.Fatal("expansion bomb accepted")
	}
}

func TestYAMLStreamAliasesAndBOM(t *testing.T) {
	input := `monitors:
- id: first
  name: first
  pulse_check: &pulse
    type: tcp
    interval: 1s
    timeout: 1s
    config: {host: localhost, port: 80}
- id: second
  name: second
  pulse_check: *pulse
`
	var got []NormalizedItem
	if err := NormalizeFile(t.Context(), strings.NewReader("\ufeff"+input), FileNormalizationProfile, DecodeOptions{}, func(i NormalizedItem) error { got = append(got, i); return nil }); err != nil || len(got) != 2 {
		t.Fatal("stream alias failed", err, len(got))
	}
	if got[1].FieldPositions["spec.check.driver.config.host"].Line != 8 {
		t.Fatal("definition coordinate changed", got[1].FieldPositions)
	}
	for _, profile := range []string{fileNormalizationV1, fileNormalizationV2} {
		if err := NormalizeFile(t.Context(), strings.NewReader(input), profile, DecodeOptions{}, func(NormalizedItem) error { return nil }); err == nil {
			t.Fatal("historical input contract changed", profile)
		}
	}
	var a, b []byte
	jsonSource := `{"apiVersion":"cpra.io/v2","kind":"Recipient","metadata":{"id":"alice"},"spec":{"endpointRefs":["mail"]}}`
	for n, prefix := range []string{"", "\ufeff"} {
		err := NormalizeFile(t.Context(), strings.NewReader(prefix+jsonSource), FileNormalizationProfile, DecodeOptions{}, func(i NormalizedItem) error {
			if n == 0 {
				a = i.JSON
			} else {
				b = i.JSON
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(a, b) {
		t.Fatal("BOM changed normalized bytes")
	}
}

func TestYAMLStreamMetadataAnchorOrder(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"before", "notification_groups: {team: [&name ops]}\nmonitors:\n- id: first\n  name: *name\n  pulse_check: {type: tcp, interval: 1s, timeout: 1s, config: {host: localhost, port: 80}}\nendpoints: {ops: {type: log, config: {file: events.jsonl}}}\n", true},
		{"after", "monitors:\n- id: first\n  name: *name\n  pulse_check: {type: tcp, interval: 1s, timeout: 1s, config: {host: localhost, port: 80}}\nnotification_groups: {team: [&name ops]}\n", false},
		{"cross-document", "apiVersion: cpra.io/v2\nkind: Recipient\nmetadata: {id: &id alice}\nspec: {endpointRefs: [mail]}\n---\napiVersion: cpra.io/v2\nkind: Recipient\nmetadata: {id: *id}\nspec: {endpointRefs: [mail]}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var items []NormalizedItem
			err := NormalizeFile(t.Context(), strings.NewReader(tc.input), FileNormalizationProfile, DecodeOptions{}, func(i NormalizedItem) error { items = append(items, i); return nil })
			if (err == nil) != tc.valid {
				t.Fatal("metadata alias order", err)
			}
			if tc.valid && (len(items) != 3 || items[0].ID != "Monitor/first") {
				t.Fatal("metadata changed resource emission order", len(items))
			}
		})
	}
}

func TestUnprofiledAliasCreationAndContinuationAgree(t *testing.T) {
	input := "apiVersion: cpra.io/v2\nkind: List\nitems:\n- apiVersion: cpra.io/v2\n  kind: Recipient\n  metadata: {id: alice}\n  spec: &notify {endpointRefs: [mail]}\n- apiVersion: cpra.io/v2\n  kind: Recipient\n  metadata: {id: bob}\n  spec: *notify\n"
	frozen, err := Freeze(t.Context(), []Source{Reader("input", strings.NewReader(input))}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Close()
	index := 0
	err = DecodeOperationSource(t.Context(), strings.NewReader(input), frozen.NormalizationProfile(), DecodeOptions{}, func(i NormalizedItem) error {
		original, err := frozen.Item(t.Context(), index)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(original.Resource)
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, i.JSON) {
			t.Fatal("unprofiled continuation changed resource")
		}
		index++
		return nil
	})
	if err != nil || index != 2 {
		t.Fatal("cannot continue original unprofiled input", err, index)
	}
}

func TestUnprofiledUnusedAnchorQuotaPreservesOriginalAcceptance(t *testing.T) {
	var a YAMLAnchors
	a.allowUnusedOverflow = true
	input := []byte("unused: &large '" + strings.Repeat("x", (1<<20)+1) + "'\n")
	if _, err := a.Parse(input, nil); err != nil {
		t.Fatal("unused original anchor narrowed acceptance", err)
	}
	if !a.saturated || len(a.values) != 0 {
		t.Fatal("overflow kept unbounded values")
	}
	if _, err := a.Parse([]byte("local: &local value\nalias: *local\n"), nil); err != nil {
		t.Fatal("same-entry original aliases failed", err)
	}
	if _, err := a.Parse([]byte("external: *large\n"), nil); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatal("overflow misreported as unknown anchor", err)
	}
	var current YAMLAnchors
	if _, err := current.Parse(input, nil); err == nil {
		t.Fatal("v3 registry quota not enforced")
	}
}

func TestUnprofiledYAMLDepthCompatibility(t *testing.T) {
	for _, aliases := range []bool{false, true} {
		for depth := 62; depth <= 66; depth++ {
			input := strings.Repeat("{key: ", depth) + "{}" + strings.Repeat("}", depth)
			if aliases {
				input = "anchor: &value " + input + "\nalias: *value\n"
			}
			original, originalErr := decodeYAML([]byte(input))
			a := YAMLAnchors{allowUnusedOverflow: true}
			root, err := a.Parse([]byte(input), nil)
			var value any
			if err == nil {
				value, err = (&builder{}).decodeYAMLNode(root)
			}
			if (err == nil) != (originalErr == nil) || err == nil && !reflect.DeepEqual(original, value) {
				t.Fatalf("depth %d aliases %t changed acceptance: original=%v current=%v", depth, aliases, originalErr, err)
			}
		}
	}
}

func TestYAMLAnchorsRequireOneDocument(t *testing.T) {
	for _, input := range []string{"a: *value\n---\nb: *value\n", "a: *value\n...\nb: *value\n", "---\na: *value\n---\nb: value\n"} {
		var a YAMLAnchors
		if _, err := a.Parse([]byte("value: &value example\n"), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Parse([]byte(input), nil); err == nil {
			t.Fatal("multiple documents accepted")
		}
	}
}

func TestUnprofiledYAMLStandaloneRootsPreserveContinuation(t *testing.T) {
	for _, input := range []string{
		"- apiVersion: cpra.io/v2\n  kind: Recipient\n  metadata: {id: alice}\n  spec: {endpointRefs: [mail]}\n",
		"null\n---\napiVersion: cpra.io/v2\nkind: Recipient\nmetadata: {id: alice}\nspec: {endpointRefs: [mail]}\n",
		"---\n[]\n---\napiVersion: cpra.io/v2\nkind: Recipient\nmetadata: {id: alice}\nspec: {endpointRefs: [mail]}\n",
	} {
		var historical []NormalizedItem
		if err := NormalizeFile(t.Context(), strings.NewReader(input), fileNormalizationV2, DecodeOptions{}, func(i NormalizedItem) error { historical = append(historical, i); return nil }); err != nil {
			t.Fatal(err)
		}
		frozen, err := Freeze(t.Context(), []Source{Reader("input", strings.NewReader(input))}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if frozen.Len() != len(historical) || frozen.Len() != 1 {
			t.Fatal("standalone root lost resources")
		}
		original, err := frozen.Item(t.Context(), 0)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(original.Resource)
		if !bytes.Equal(raw, historical[0].JSON) || original.Location.Document != historical[0].Location.Document || original.Location.Item != historical[0].Location.Item {
			t.Fatal("standalone root changed original identity")
		}
		index := 0
		err = DecodeOperationSource(t.Context(), strings.NewReader(input), "", DecodeOptions{}, func(i NormalizedItem) error {
			if !bytes.Equal(i.JSON, historical[index].JSON) || i.Location.Document != historical[index].Location.Document || i.Location.Item != historical[index].Location.Item {
				t.Fatal("standalone continuation changed original identity")
			}
			index++
			return nil
		})
		if err != nil || index != 1 {
			t.Fatal(err)
		}
		if err := frozen.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnprofiledYAMLRejectsEmptyFinalMapping(t *testing.T) {
	input := "apiVersion: cpra.io/v2\nkind: Recipient\nmetadata: {id: alice}\nspec: {endpointRefs: [mail]}\n---\n{}\n"
	for _, profile := range []string{"", fileNormalizationV2, FileNormalizationProfile} {
		var err error
		if profile == "" {
			frozen, failure := Freeze(t.Context(), []Source{Reader("input", strings.NewReader(input))}, Options{})
			err = failure
			if frozen != nil {
				frozen.Close()
				t.Fatal("invalid final document published frozen input")
			}
		} else {
			err = NormalizeFile(t.Context(), strings.NewReader(input), profile, DecodeOptions{}, func(NormalizedItem) error { return nil })
		}
		if err == nil {
			t.Fatal("empty final mapping accepted", profile)
		}
	}
	if err := DecodeOperationSource(t.Context(), strings.NewReader(input), "", DecodeOptions{}, func(NormalizedItem) error { return nil }); err == nil {
		t.Fatal("empty final mapping accepted during continuation")
	}
}
