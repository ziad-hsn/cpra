//go:build externaljobs

package collection

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCollectionExternalParametersPreserveExactNumbers(t *testing.T) {
	for _, token := range []string{"9007199254740993.0", "0.99999999999999999", "1e-999", "1e+09", "-0"} {
		jsonInput := `{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"numeric"},"spec":{"check":{"interval":"60s","timeout":"5s","driver":{"type":"external","config":{"jobTypeID":"numeric-check","version":"v1","parameters":{"amount":` + token + `}}}}}}`
		if _, err := api.DecodeResource([]byte(jsonInput)); err != nil {
			t.Fatal("supported direct resource rejected:", err)
		}
		yamlInput := "apiVersion: cpra.io/v2\nkind: Monitor\nmetadata:\n  id: numeric\nspec:\n  check:\n    interval: 60s\n    timeout: 5s\n    driver:\n      type: external\n      config:\n        jobTypeID: numeric-check\n        version: v1\n        parameters:\n          amount: " + token + "\n"
		for _, input := range []string{jsonInput, yamlInput} {
			check := func(item Item) error {
				var spec api.MonitorSpec
				if err := json.Unmarshal(item.Resource.Spec, &spec); err != nil {
					return err
				}
				var config api.ExternalConfig
				if err := json.Unmarshal(spec.Check.Driver.Config, &config); err != nil {
					return err
				}
				var parameters map[string]json.RawMessage
				if err := json.Unmarshal(config.Parameters, &parameters); err != nil {
					return err
				}
				want := token
				if input == yamlInput && token == "-0" {
					want = "0"
				} // YAML integer negative zero has the same mathematical value.
				if string(parameters["amount"]) != want {
					t.Fatalf("parameter changed: got %s want %s", parameters["amount"], want)
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
	}
}
