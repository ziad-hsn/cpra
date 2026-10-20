package collection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestManifestDurationErrorDoesNotRetainRejectedNumber(t *testing.T) {
	const marker = "987654321987654321987654321987654321"
	input := "monitors:\n  - name: service\n    pulse_check: {type: tcp, interval: 1s, timeout: 1s, config: {host: example.test, port: 443}}\n    intervention:\n      action: docker\n      target:\n        container: service\n        operation_timeout: " + marker + "\n"
	err := Decode(t.Context(), strings.NewReader(input), DecodeOptions{SourceName: "service.yaml"}, func(Item) error { return nil })
	var source *SourceError
	field, ok := api.ValidationFieldError(err)
	if !errors.As(err, &source) || source.Location.Line != 8 || !ok || field.Field != "spec.recovery.driver.config.operationTimeout" {
		t.Fatal("duration cause lost original coordinates", err)
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(current.Error(), marker) {
			t.Fatal("rejected numeric value retained", current)
		}
	}
}

func TestSourcePositionsManyNamedResourcesRespectCancellation(t *testing.T) {
	var input strings.Builder
	input.WriteString("endpoints:\n")
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&input, "  e%05d: {type: webhook, config: {url: https://example.test}}\n", i)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	seen := 0
	err := Decode(ctx, strings.NewReader(input.String()), DecodeOptions{}, func(Item) error { seen++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || seen != 1 {
		t.Fatal("named definitions continued after cancellation", seen, err)
	}
}

func TestFrozenValidationKeepsOriginalFieldCoordinates(t *testing.T) {
	for _, test := range []struct {
		name, input  string
		line, column int
	}{
		{"yaml", "\n# one\n---\nmonitors:\n  - name: service\n    pulse_check:\n      type: http\n      interval: 1s\n      timeout: 1s\n      config:\n        url: broken\n", 11, 14},
		{"json", "\n{\n \"monitors\": [\n {\"name\":\"service\",\"pulse_check\":{\"type\":\"http\",\"interval\":\"1s\",\"timeout\":\"1s\",\"config\":{\n \"url\": \"broken\"\n }}}\n ]\n}", 5, 9},
		{"resource", "\napiVersion: cpra.io/v2\nkind: Monitor\nmetadata: {id: service}\nspec:\n  check:\n    interval: 1s\n    timeout: 1s\n    driver:\n      type: http\n      config: {url: broken}\n", 11, 21},
	} {
		t.Run(test.name, func(t *testing.T) {
			frozen := freezeProfileTest(t, test.input)
			item, err := frozen.Item(t.Context(), 0)
			if err != nil {
				t.Fatal(err)
			}
			cause := api.ValidateResourceSettings(item.Resource)
			if cause == nil {
				t.Fatal("invalid HTTP URL passed settings validation")
			}
			err = item.ValidationError(cause)
			var source *SourceError
			if !errors.As(err, &source) || source.Location.Line != test.line || source.Location.Column != test.column || source.Location.Document != 1 || source.Location.Item != 1 {
				t.Fatalf("wrong source coordinates: %+v (%v)", source, err)
			}
			field, ok := api.ValidationFieldError(err)
			if !ok || field.Field != "spec.check.driver.config.url" || strings.Contains(err.Error(), "broken") {
				t.Fatal("lost safe cause", err)
			}
			encoded, _ := json.Marshal(item.Resource)
			if strings.Contains(string(encoded), "fieldPositions") {
				t.Fatal("source metadata entered resource bytes")
			}
		})
	}
}

func TestSourceCoordinatesAcrossDocumentsAndMergedValues(t *testing.T) {
	input := "# first\napiVersion: cpra.io/v2\nkind: Credential\nmetadata: {id: secret}\nspec: {value: private}\n---\nmonitors:\n  - name: second\n    pulse_check:\n      type: http\n      interval: 1s\n      timeout: 1s\n      config:\n        <<: &settings {url: broken}\n"
	frozen := freezeProfileTest(t, input)
	item, err := frozen.Item(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	cause := item.ValidationError(api.ValidateResourceSettings(item.Resource))
	var source *SourceError
	if !errors.As(cause, &source) || source.Location.Document != 2 || source.Location.Item != 1 || source.Location.Line != 14 || source.Location.Column != 29 {
		t.Fatalf("merge origin lost: %v", cause)
	}
	if len(item.FieldPositions) > 1024 {
		t.Fatal("unbounded coordinates")
	}
}

func TestSourcePositionsDoNotRetainMapKeysOrValues(t *testing.T) {
	input := `{"apiVersion":"cpra.io/v2","kind":"Credential","metadata":{"id":"secret","labels":{"PRIVATE_FIELD":"PRIVATE_VALUE","password":"private","spec.check.driver.config.url":"private"}},"spec":{"value":"PRIVATE_VALUE"}}`
	frozen := freezeProfileTest(t, input)
	item, err := frozen.Item(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(item.FieldPositions)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "password") || strings.Contains(string(raw), "spec.check.driver.config.url") {
		t.Fatal("coordinate metadata retained an arbitrary key or value", string(raw))
	}
}

func TestSourcePositionsPreserveExistingWhitespaceAcceptance(t *testing.T) {
	input := "  apiVersion: cpra.io/v2\nkind: Credential\nmetadata: {id: key}\nspec: {value: private}\n"
	for _, profile := range []string{fileNormalizationV1, FileNormalizationProfile} {
		err := NormalizeFile(t.Context(), strings.NewReader(input), profile, DecodeOptions{}, func(item NormalizedItem) error {
			if item.ID != "Credential/key" || item.Location.Line != 1 || item.Location.Column != 3 {
				t.Fatal("normalization or position changed", item.Location)
			}
			return nil
		})
		if err != nil {
			t.Fatal("previously accepted source was rejected", profile, err)
		}
	}
}
func TestSourcePositionsIgnoreOverriddenMergeSubtrees(t *testing.T) {
	input := "monitors:\n  - name: sample\n    pulse_check:\n      <<: &base {type: http, interval: 1s, timeout: 1s, config: {url: discarded}}\n      config: {}\n"
	frozen := freezeProfileTest(t, input)
	item, err := frozen.Item(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	cause := item.ValidationError(api.ValidateResourceSettings(item.Resource))
	var source *SourceError
	if !errors.As(cause, &source) || source.Location.Line != 5 || source.Location.Column != 15 {
		t.Fatal("diagnostic used an overridden value", cause)
	}
	if _, exists := item.FieldPositions["spec.check.driver.config.url"]; exists {
		t.Fatal("overridden coordinate retained")
	}
}

func TestSourcePositionsDistinguishDottedEndpointIDs(t *testing.T) {
	input := "endpoints:\n  ops:\n    type: webhook\n    config: {url: https://example.test}\n  ops.config:\n    type: webhook\n    config: {url: broken}\nmonitors: []\n"
	frozen := freezeProfileTest(t, input)
	item, err := frozen.Item(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	cause := item.ValidationError(api.InvalidField("spec.config.url", "httpURL"))
	var source *SourceError
	if !errors.As(cause, &source) || source.Location.Line != 7 || source.Location.Column != 19 {
		t.Fatal("dotted identity aliased another resource", cause)
	}
}
func TestMalformedSourceKeepsSafeOrigin(t *testing.T) {
	for _, input := range []string{
		"\nmonitors:\n - name: PRIVATE_VALUE\n   pulse_check: [bad\n",
		"\n{\n \"monitors\": [\n {\"name\": \"PRIVATE_VALUE\", broken}\n]}\n",
	} {
		err := NormalizeFile(t.Context(), strings.NewReader(input), FileNormalizationProfile, DecodeOptions{SourceName: "services.yaml"}, func(NormalizedItem) error { return nil })
		var source *SourceError
		if !errors.As(err, &source) || source.Location.Source != "services.yaml" || source.Location.Document != 1 || source.Location.Item != 1 || source.Location.Line < 3 {
			t.Fatalf("syntax failure lost source: %v", err)
		}
		for current := err; current != nil; current = errors.Unwrap(current) {
			if strings.Contains(current.Error(), "PRIVATE_VALUE") {
				t.Fatal("syntax error retained scalar", current)
			}
		}
	}
}

func TestSourcePositionsOmitFreeFormPayloadDescendants(t *testing.T) {
	fields := map[string]SourcePosition{}
	for _, path := range []string{"/metadata/labels/password", "/spec/check/driver/config/headers/url", "/spec/check/driver/credentialRefs/url", "/spec/check/driver/config/parameters/password", "/spec/parameterSchema/type", "/spec/resultSchema/type", "/status/type", "/metadata/labels/spec.check.driver.config.url"} {
		fields[path] = SourcePosition{Line: 1, Column: 1}
	}
	for _, root := range []string{"/metadata/labels", "/spec/check/driver/config/headers", "/spec/check/driver/credentialRefs", "/spec/check/driver/config/parameters", "/spec/parameterSchema", "/spec/resultSchema", "/status"} {
		for _, descendant := range []string{"/123", "/0/1", "/0/type", "/type/0"} {
			fields[root+descendant] = SourcePosition{Line: 1, Column: 1}
		}
	}
	if got := resourceFieldPositions(fields, false); len(got) != 0 {
		t.Fatal("free-form fields entered source metadata", got)
	}
}

func TestJSONMetadataSyntaxReportsOriginalToken(t *testing.T) {
	for _, input := range []string{
		"{\n\"kind\":\"Monitor\",\n\"spec\":{\n\"check\":PRIVATE_VALUE,\n\"other\":true\n}\n}",
		"{\n\"endpoints\":{\n\"ops\":{\n\"config\":PRIVATE_VALUE,\n\"type\":\"webhook\"\n}\n}\n}",
	} {
		err := Decode(t.Context(), strings.NewReader(input), DecodeOptions{SourceName: "services.json"}, func(Item) error { return nil })
		var source *SourceError
		if !errors.As(err, &source) || source.Location.Source != "services.json" || source.Location.Document != 1 || source.Location.Line != 4 {
			t.Fatalf("syntax failure is not at its original token: %v", err)
		}
		for current := err; current != nil; current = errors.Unwrap(current) {
			if strings.Contains(current.Error(), "PRIVATE_VALUE") {
				t.Fatal("syntax error retained scalar", current)
			}
		}
	}
}
func TestSourcePositionBudgetDoesNotBorrowPreviousEntry(t *testing.T) {
	var input strings.Builder
	input.WriteString("endpoints:\n")
	for i := 0; i < 1400; i++ {
		fmt.Fprintf(&input, "  e%04d:\n    type: webhook\n    config:\n      url: https://example.test\n      headers:\n", i)
		for n := 0; n < 20; n++ {
			fmt.Fprintf(&input, "        Header%d: value\n", n)
		}
	}
	input.WriteString("monitors: []\n")
	var last NormalizedItem
	err := NormalizeFile(t.Context(), strings.NewReader(input.String()), FileNormalizationProfile, DecodeOptions{}, func(item NormalizedItem) error { last = item; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if last.ID != "NotificationEndpoint/e1399" || last.Location.Line != 0 || last.Location.Column != 0 {
		t.Fatal("unavailable coordinates inherited another entry", last.ID, last.Location)
	}
}
