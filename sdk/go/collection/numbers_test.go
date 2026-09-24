package collection

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func numericMonitor(format, token string, manifest bool) string {
	if format == "yaml" {
		if manifest {
			return "monitors:\n  - name: numeric\n    pulse_check:\n      type: http\n      interval: 60s\n      timeout: 5s\n      retries: " + token + "\n      config:\n        url: https://example.test\n"
		}
		return "apiVersion: cpra.io/v2\nkind: Monitor\nmetadata:\n  id: numeric\nspec:\n  check:\n    interval: 60s\n    timeout: 5s\n    retries: " + token + "\n    driver:\n      type: http\n      config:\n        url: https://example.test\n"
	}
	if manifest {
		return `{"monitors":[{"name":"numeric","pulse_check":{"type":"http","interval":"60s","timeout":"5s","retries":` + token + `,"config":{"url":"https://example.test"}}}]}`
	}
	return `{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"numeric"},"spec":{"check":{"interval":"60s","timeout":"5s","retries":` + token + `,"driver":{"type":"http","config":{"url":"https://example.test"}}}}}`
}

func TestCollectionNumericControlsRejectFractionalCoercion(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		for _, manifest := range []bool{false, true} {
			for _, token := range []string{"0.99999999999999999", "1e-999", "9007199254740993.0", "9007199254740993e0", "1.0", "1e3"} {
				t.Run(fmt.Sprintf("%s/manifest=%t/%s", format, manifest, token), func(t *testing.T) {
					input := numericMonitor(format, token, manifest)
					visits := 0
					if err := Decode(context.Background(), strings.NewReader(input), DecodeOptions{}, func(Item) error { visits++; return nil }); err == nil || visits != 0 {
						t.Fatal("non-integer token became an accepted integer control", err, visits)
					}
					directory := t.TempDir()
					frozen, err := Freeze(context.Background(), []Source{Reader("numbers", strings.NewReader(input))}, Options{TempDir: directory})
					if err == nil {
						frozen.Close()
						t.Fatal("Freeze accepted a non-integer control")
					}
					entries, err := os.ReadDir(directory)
					if err != nil || len(entries) != 0 {
						t.Fatal("rejected input left a private spool", err)
					}
				})
			}
		}
	}
}

func TestCollectionNumericControlsRetainExactInt64(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		for _, manifest := range []bool{false, true} {
			for _, token := range []string{"0", "9007199254740993", "9223372036854775807"} {
				t.Run(fmt.Sprintf("%s/manifest=%t/%s", format, manifest, token), func(t *testing.T) {
					input := numericMonitor(format, token, manifest)
					check := func(item Item) error {
						var spec struct {
							Check struct {
								Retries json.RawMessage `json:"retries"`
							} `json:"check"`
						}
						if err := json.Unmarshal(item.Resource.Spec, &spec); err != nil {
							return err
						}
						if string(spec.Check.Retries) != token {
							t.Fatalf("integer changed: got %s want %s", spec.Check.Retries, token)
						}
						return nil
					}
					if err := Decode(context.Background(), strings.NewReader(input), DecodeOptions{}, check); err != nil {
						t.Fatal(err)
					}
					frozen := freezeTest(t, input)
					item, err := frozen.Item(context.Background(), 0)
					if err != nil {
						t.Fatal(err)
					}
					if err := check(item); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestCollectionExactJSONNumbersAndStructuralValidation(t *testing.T) {
	for _, token := range []string{"9007199254740993.0", "0.99999999999999999", "1e-999", "1e+09", "-0", "18446744073709551616"} {
		value, err := decodeJSON([]byte(`{"nested":[` + token + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		number, ok := value.(map[string]any)["nested"].([]any)[0].(json.Number)
		if !ok || number.String() != token {
			t.Fatal("JSON numeric token changed:", number)
		}
	}
	for _, raw := range []string{`{"x":{"a":1,"\u0061":2}}`, `{"x":[1,]}`, `{"x":01}`, `{} {}`, strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)} {
		if _, err := decodeJSON([]byte(raw)); err == nil {
			t.Fatal("accepted invalid, duplicate or too-deep JSON")
		}
	}
}

func TestCollectionYAMLDecimalSpellingWithoutRounding(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"0.99999999999999999", "0.99999999999999999"}, {"1e-999", "1e-999"},
		{"9007199254740993.0", "9007199254740993.0"}, {"+.5", "0.5"},
		{"-000.50", "-0.50"}, {"1_000.25", "1000.25"}, {"1.", "1.0"},
	} {
		value, err := decodeYAML([]byte("value: " + test.input + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		number, ok := value.(map[string]any)["value"].(json.Number)
		if !ok || number.String() != test.want {
			t.Fatalf("YAML numeric value changed: %s => %v", test.input, value)
		}
	}
	for _, token := range []string{".inf", "-.Inf", ".nan", "!!float .", "!!float -", "!!float true"} {
		if _, err := decodeYAML([]byte("value: " + token + "\n")); err == nil {
			t.Fatal("accepted non-finite or invalid YAML number:", token)
		}
	}
}

func TestCollectionManifestNegativeZeroStillInherits(t *testing.T) {
	input := `{"monitors":[{"name":"zero","pulse_check":{"type":"http","interval":"60s","timeout":"5s","retries":3,"max_failures":4,"unhealthy_threshold":-0,"config":{"url":"https://example.test","retries":-0}}}]}`
	check := func(item Item) error {
		var spec struct {
			Check struct {
				UnhealthyThreshold int `json:"unhealthyThreshold"`
				Driver             struct {
					Config struct {
						Retries int `json:"retries"`
					} `json:"config"`
				} `json:"driver"`
			} `json:"check"`
		}
		if err := json.Unmarshal(item.Resource.Spec, &spec); err != nil {
			return err
		}
		if spec.Check.UnhealthyThreshold != 4 || spec.Check.Driver.Config.Retries != 3 {
			t.Fatal("negative zero lost manifest inheritance")
		}
		return nil
	}
	if err := Decode(context.Background(), strings.NewReader(input), DecodeOptions{}, check); err != nil {
		t.Fatal(err)
	}
	frozen := freezeTest(t, input)
	item, err := frozen.Item(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := check(item); err != nil {
		t.Fatal(err)
	}
}
