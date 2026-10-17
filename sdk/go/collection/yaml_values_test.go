package collection

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestYAMLExpansionAndMergeRules(t *testing.T) {
	got, err := decodeYAML([]byte("first: &first {a: first, b: first}\nsecond: &second {a: second, c: second}\nmerged: {<<: [*first, *second], a: explicit}\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": "explicit", "b": "first", "c": "second"}
	if !reflect.DeepEqual(got.(map[string]any)["merged"], want) {
		t.Fatalf("merge precedence: %#v", got)
	}
	for _, input := range []string{"self: &self [*self]", "key: {<<: 1}", "key: {<<: {a: 1}, <<: {b: 2}}"} {
		if _, err := decodeYAML([]byte(input)); err == nil {
			t.Fatal("unsafe expansion accepted")
		}
	}
	var bomb strings.Builder
	bomb.WriteString("a0: &a0 [x, x, x, x]\n")
	for i := 1; i < 12; i++ {
		fmt.Fprintf(&bomb, "a%d: &a%d [*a%d, *a%d, *a%d, *a%d]\n", i, i, i-1, i-1, i-1, i-1)
	}
	if _, err := decodeYAML([]byte(bomb.String())); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("unbounded expansion: %v", err)
	}
}

func TestYAMLAliasFreeMetadataRetainsOriginalBounds(t *testing.T) {
	input := []byte("key: " + strings.Repeat("x", (1<<20)+1) + "\n")
	if _, err := decodeYAML(input); err != nil {
		t.Fatal("ordinary bounded document narrowed", err)
	}
}

const conflictingDestination = `monitors:
- id: fixture
  name: fixture
  pulse_check: {type: tcp, interval: 1s, timeout: 1s, config: {host: localhost, port: 80}}
  codes: {red: {notify_group: ops, notify: discord, config: {secret_typo: PRIVATE}}}
`

func TestOriginalOperationDestinationNormalization(t *testing.T) {
	var first string
	for _, profile := range []string{"", fileNormalizationV1} {
		var encoded string
		err := DecodeOperationSource(context.Background(), strings.NewReader(conflictingDestination), profile, DecodeOptions{}, func(item NormalizedItem) error {
			encoded = string(item.JSON)
			return nil
		})
		if err != nil || encoded == "" || strings.Contains(encoded, "PRIVATE") {
			t.Fatal("original input could not be reproduced", err)
		}
		if first != "" && encoded != first {
			t.Fatal("original normalized identity changed")
		}
		first = encoded
	}
	for _, profile := range []string{"", FileNormalizationProfile} {
		var err error
		if profile == "" {
			err = Decode(t.Context(), strings.NewReader(conflictingDestination), DecodeOptions{}, func(Item) error { t.Fatal("conflicting input emitted"); return nil })
		} else {
			err = NormalizeFile(t.Context(), strings.NewReader(conflictingDestination), profile, DecodeOptions{}, func(NormalizedItem) error { t.Fatal("conflicting input emitted"); return nil })
		}
		if err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatalf("new input did not fail safely: %v", err)
		}
	}
}

func TestOriginalUnprofiledAliasesRemainUnprofiled(t *testing.T) {
	input := "apiVersion: cpra.io/v2\nkind: Recipient\nmetadata: {id: &id alice, name: *id}\nspec: {endpointRefs: [mail]}\n"
	var decoded, resumed string
	err := NormalizeFile(t.Context(), strings.NewReader(input), FileNormalizationProfile, DecodeOptions{}, func(i NormalizedItem) error { decoded = string(i.JSON); return nil })
	if err != nil {
		t.Fatal(err)
	}
	err = DecodeOperationSource(t.Context(), strings.NewReader(input), "", DecodeOptions{}, func(i NormalizedItem) error { resumed = string(i.JSON); return nil })
	if err != nil || decoded != resumed {
		t.Fatal("new unprofiled source cannot be reproduced", err)
	}
}

func TestA2AProfileProjectionIsVersioned(t *testing.T) {
	for _, category := range []string{"recovery", "notification"} {
		if fileBaseDriver(category, "a2a", fileNormalizationV1) || !fileBaseDriver(category, "a2a", FileNormalizationProfile) {
			t.Fatal("agent role changed historical profile or is missing from current builtins")
		}
	}
}

func TestNotificationGroupTypeIsNotDiscarded(t *testing.T) {
	for _, value := range []string{"123", "false", "[]", "{}"} {
		input := strings.Replace(conflictingDestination, "notify_group: ops, notify: discord, config: {secret_typo: PRIVATE}", "notify_group: "+value+", notify: log, config: {}", 1)
		if err := Decode(t.Context(), strings.NewReader(input), DecodeOptions{}, func(Item) error { t.Fatal("wrong group type emitted"); return nil }); err == nil {
			t.Fatal("wrong group type discarded")
		}
		if err := DecodeOperationSource(t.Context(), strings.NewReader(input), "", DecodeOptions{}, func(NormalizedItem) error { return nil }); err != nil {
			t.Fatal("original normalization changed", err)
		}
	}
}
