package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"
)

var responseRequired = sync.OnceValue(func() map[string]map[string]bool {
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Required []string `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if json.Unmarshal(Schema(), &document) != nil {
		return nil
	}
	result := make(map[string]map[string]bool, len(document.Components.Schemas))
	for name, schema := range document.Components.Schemas {
		fields := make(map[string]bool, len(schema.Required))
		for _, name := range schema.Required {
			fields[name] = true
		}
		result[name] = fields
	}
	return result
})

// Progress uses pointers to represent an absent observation, not permission for
// JSON null. Keep this policy tied to the canonical schemas: changing the Go
// representation must not broaden the operation-progress wire contract.
var responseProgressNull = sync.OnceValue(func() map[string]map[string]bool {
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Type json.RawMessage `json:"type"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	result := map[string]map[string]bool{
		"Operation":                    {"uploaded": false, "committed": false, "applied": false, "validated": false, "itemCount": false, "executionResult": false},
		"ApplyResult":                  {"committed": false, "applied": false, "inputOrdinal": false, "planOrdinal": false, "sourceDocument": false, "sourceItem": false, "generation": false, "committedIndex": false, "decidedAt": false, "childDisposition": false},
		"ExecutionResultAvailability":  {"counts": false, "summary": false},
		"ExecutionChildDisposition":    {"updatedAt": false},
		"Preflight":                    {"itemCount": false},
		"CollectionReselectionAttempt": {"errorCode": false},
	}
	if json.Unmarshal(Schema(), &document) != nil {
		return result // Fail closed for these reviewed progress fields.
	}
	for model, fields := range result {
		for name := range fields {
			raw := document.Components.Schemas[model].Properties[name].Type
			var single string
			var union []string
			if json.Unmarshal(raw, &single) == nil {
				fields[name] = single == "null"
			} else if json.Unmarshal(raw, &union) == nil {
				for _, value := range union {
					fields[name] = fields[name] || value == "null"
				}
			}
		}
	}
	return result
})

// DecodeResponse permits additive fields from newer servers while checking the
// presence of required contract fields. Unavailable zero values must be explicit.
// Unlike mutation decoding, forward-compatible response fields are not rejected.
func DecodeResponse(raw []byte, out any) error {
	if out == nil || reflect.TypeOf(out).Kind() != reflect.Pointer || reflect.ValueOf(out).IsNil() {
		return errors.New("response destination must be a non-nil pointer")
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("missing JSON response payload")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := uniqueValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON response value")
	}
	if err := requiredResponseFields(raw, reflect.TypeOf(out), 0); err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func requiredResponseFields(raw []byte, t reflect.Type, depth int) error {
	if depth > 128 {
		return errors.New("response nesting exceeds 128")
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(json.RawMessage(nil)) {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("required response value is null")
	}
	if t == reflect.TypeOf(time.Time{}) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "-" || tag[0] == "" {
				continue
			}
			v, ok := fields[tag[0]]
			optional := false
			for _, s := range tag[1:] {
				if s == "omitempty" || s == "omitzero" {
					optional = true
				}
			}
			// Explicit emission of an optional zero (x-omitempty:false) is a
			// serialization choice, not a requirement for older server responses.
			// Named public models follow the canonical contract's required list.
			if required, model := responseRequired()[t.Name()]; model && t.PkgPath() == reflect.TypeOf(Measurement{}).PkgPath() {
				optional = !required[tag[0]]
			}
			if !ok {
				if !optional {
					return fmt.Errorf("required response field %q is absent", tag[0])
				}
				continue
			}
			if optional && f.Type.Kind() == reflect.Pointer && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				if nullable, tracked := responseProgressNull()[t.Name()][tag[0]]; tracked && !nullable && t.PkgPath() == reflect.TypeOf(Operation{}).PkgPath() {
					return fmt.Errorf("response field %q does not allow null", tag[0])
				}
				continue
			}
			if err := requiredResponseFields(v, f.Type, depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, v := range values {
			if err := requiredResponseFields(v, t.Elem(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, v := range values {
			if err := requiredResponseFields(v, t.Elem(), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
