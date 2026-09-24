//go:build externaljobs

package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestJobSchemaSemantics(t *testing.T) {
	for _, test := range []struct{ name, schema, valid, invalid string }{
		{"required_nullable", `{"type":"object","required":["x"],"properties":{"x":{"type":["string","null"]}},"additionalProperties":false}`, `{"x":null}`, `{}`},
		{"properties_without_type", `{"properties":{"x":{"type":"number"}}}`, `7`, `{"x":false}`},
		{"additional_default", `{"properties":{"x":{"type":"number"}}}`, `{"y":"yes"}`, `{"x":"no"}`},
		{"additional_schema", `{"additionalProperties":{"type":"integer"}}`, `{"x":1.0}`, `{"x":1.1}`},
		{"unicode", `{"type":"string","minLength":2,"maxLength":2}`, `"é猫"`, `"a"`},
		{"unanchored_pattern", `{"pattern":"bc"}`, `"abcd"`, `"ad"`},
		{"integer", `{"type":"integer"}`, `1e0`, `1.25`},
		{"exact_number", `{"const":9007199254740993}`, `9007199254740993.0`, `9007199254740992`},
		{"exact_enum", `{"enum":[1,{"x":[9007199254740993]}]}`, `{"x":[9007199254740993.0]}`, `{"x":[9007199254740992]}`},
		{"limits", `{"minimum":0.1,"exclusiveMaximum":0.4,"multipleOf":0.1}`, `0.3`, `0.4`},
		{"array", `{"type":"array","items":{"type":"integer"},"minItems":1,"maxItems":2}`, `[1,2]`, `[1,2,3]`},
		{"local_reference", `{"$defs":{"value":{"minimum":1}},"$ref":"#/$defs/value","maximum":2}`, `2`, `3`},
		{"escaped_pointer", `{"$defs":{"a/b~c":{"const":1}},"$ref":"#/$defs/a~1b~0c"}`, `1.0`, `2`},
		{"boolean", `{"properties":{"x":false}}`, `{}`, `{"x":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, err := compileJobSchema(t.Context(), []byte(test.schema))
			if err != nil {
				t.Fatal(err)
			}
			if err = s.validate(t.Context(), []byte(test.valid)); err != nil {
				t.Fatal("valid value rejected", err)
			}
			if err = s.validate(t.Context(), []byte(test.invalid)); !errors.Is(err, ErrJobValueInvalid) {
				t.Fatal("invalid value accepted", err)
			}
		})
	}
}

func TestJobSchemaRejectsUnsupportedAndMalformedContracts(t *testing.T) {
	for i, raw := range []string{
		`{"type":"number","type":"string"}`, `{} true`, `null`, `[]`,
		`{"$id":"https://example.test/schema"}`, `{"format":"hostname"}`, `{"default":1}`, `{"anyOf":[true,false]}`,
		`{"$schema":"https://example.test/schema"}`, `{"$ref":"https://example.test/schema"}`, `{"$ref":"file:///etc/passwd"}`, `{"$ref":"data:application/json,{}"}`,
		`{"$ref":"#/$defs/missing"}`, `{"$defs":{"x":true},"$ref":"#/$defs/%78"}`, `{"$defs":{"x":true},"$ref":"#/$defs/x/extra"}`,
		`{"$defs":{"unused":{"$ref":"#/$defs/unused"}}}`, `{"$defs":{"x":{"$ref":"#/$defs/y"},"y":{"$ref":"#/$defs/x"}}}`,
		`{"$defs":{"unused":{"unknown":"secret-canary"}}}`, `{"properties":{"x":{"$defs":{}}}}`,
		`{"type":["integer","integer"]}`, `{"required":["x","x"]}`, `{"minLength":-1}`, `{"minimum":1e999999999}`, `{"multipleOf":0}`,
		`{"pattern":"(?=x)"}`, `{"pattern":"a{1000}b{1000}"}`,
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			_, err := compileJobSchema(t.Context(), []byte(raw))
			if err == nil {
				t.Fatal("invalid schema accepted")
			}
			if strings.Contains(err.Error(), "secret-canary") {
				t.Fatal("error exposes schema content")
			}
		})
	}
}

func TestJobSchemaInputAndWorkLimits(t *testing.T) {
	s, err := compileJobSchema(t.Context(), []byte(`true`))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"x":1,"x":2}`, `1 2`, `1e999999999`, strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34)} {
		if err := s.validate(t.Context(), []byte(raw)); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if err := s.validate(t.Context(), []byte(`"`+strings.Repeat("x", jobValueBytes)+`"`)); !errors.Is(err, ErrJobSchemaBudget) {
		t.Fatal(err)
	}
	if _, err := compileJobSchema(t.Context(), []byte(strings.Repeat(" ", jobSchemaBytes+1))); !errors.Is(err, ErrJobSchemaBudget) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := compileJobSchema(ctx, []byte(`true`)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.validate(ctx, []byte(`1`)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := compileJobSchema(nil, []byte(`true`)); err == nil {
		t.Fatal("nil context accepted")
	}
	// Acyclic local references can still amplify work. Shared scan accounting
	// stops repeated regex processing rather than caching instance plaintext.
	definitions := map[string]any{}
	for i := 0; i < 25; i++ {
		n := map[string]any{"pattern": "x"}
		if i > 0 {
			n["$ref"] = fmt.Sprintf("#/$defs/n%d", i-1)
		}
		definitions[fmt.Sprintf("n%d", i)] = n
	}
	raw, _ := json.Marshal(map[string]any{"$defs": definitions, "$ref": "#/$defs/n24"})
	s, err = compileJobSchema(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.validate(t.Context(), []byte(`"`+strings.Repeat("x", 120<<10)+`"`)); !errors.Is(err, ErrJobSchemaBudget) {
		t.Fatal("amplified scan not bounded", err)
	}
	budget := &jobSchemaBudget{ctx: t.Context(), steps: jobSchemaWork}
	if err := s.root.validate(nil, budget, 0); !errors.Is(err, ErrJobSchemaBudget) {
		t.Fatal("work budget ignored", err)
	}
}

func FuzzJobSchema(f *testing.F) {
	f.Add([]byte(`{"type":"integer"}`), []byte(`1`))
	f.Add([]byte(`{"$defs":{"x":true},"$ref":"#/$defs/x"}`), []byte(`null`))
	f.Fuzz(func(t *testing.T, schema, value []byte) {
		if len(schema) > jobSchemaBytes || len(value) > jobValueBytes {
			t.Skip()
		}
		s, err := compileJobSchema(t.Context(), schema)
		if err == nil {
			_ = s.validate(t.Context(), value)
		}
	})
}

type jobCancelContext struct {
	context.Context
	remaining int
}

func (c *jobCancelContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestJobSchemaCancellationDuringTraversal(t *testing.T) {
	s, err := compileJobSchema(t.Context(), []byte(`{"items":{"type":"integer"}}`))
	if err != nil {
		t.Fatal(err)
	}
	input := `[` + strings.Repeat("1,", 100) + `1]`
	ctx := &jobCancelContext{Context: t.Context(), remaining: 50}
	if err := s.validate(ctx, []byte(input)); !errors.Is(err, context.Canceled) {
		t.Fatal("parse ignored cancellation", err)
	}
	value, err := decodeJobJSON(t.Context(), []byte(input), jobValueBytes)
	if err != nil {
		t.Fatal(err)
	}
	ctx = &jobCancelContext{Context: t.Context(), remaining: 50}
	if err := s.root.validate(value, &jobSchemaBudget{ctx: ctx}, 0); !errors.Is(err, context.Canceled) {
		t.Fatal("evaluation ignored cancellation", err)
	}
}
