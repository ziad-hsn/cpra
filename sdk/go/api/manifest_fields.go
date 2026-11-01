package api

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"unicode"
)

// ValidateManifestDriverFields checks file-manifest spellings before conversion
// to API names. YAML uses exact snake_case tags; JSON also permits case folding.
// It does not inspect provider values or keys inside user-defined maps. Callers
// replaying a historical file-normalization profile retain its original rules.
func ValidateManifestDriverFields(category, kind string, raw json.RawMessage, jsonInput bool) error {
	target := driverTarget(category, kind)
	if target == nil {
		return errors.Join(ErrUnsupportedDriver, InvalidField("type", "unsupportedDriver"))
	}
	var fields map[string]json.RawMessage
	if err := StrictDecode(raw, &fields); err != nil {
		return PrefixValidationError("config", err)
	}
	typ := reflect.TypeOf(target).Elem()
	names := make(map[string]string, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		canonical := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if canonical == "" || canonical == "-" {
			continue
		}
		var name strings.Builder
		for _, char := range canonical {
			if unicode.IsUpper(char) {
				name.WriteByte('_')
				char = unicode.ToLower(char)
			}
			name.WriteRune(char)
		}
		spelling := name.String()
		if jsonInput && category == "check" && kind == "icmp" && canonical == "ignorePrivilege" {
			spelling = "privilege"
		}
		names[spelling] = canonical
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		accepted := false
		safeField := "config"
		for spelling, canonical := range names {
			if key == spelling || jsonInput && strings.EqualFold(key, spelling) {
				accepted = true
				break
			}
			// A known but misspelled field can retain a safe canonical location.
			if strings.EqualFold(strings.ReplaceAll(key, "_", ""), canonical) {
				safeField = "config." + canonical
			}
		}
		if !accepted {
			return InvalidField(safeField, "unknownField")
		}
	}
	return nil
}
