package management

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// collectionChangedFields describes the same captured versions used by
// preflight. Values never leave this comparison. Free-form objects and arrays
// are reported at their containing field rather than exposing their keys.
func collectionChangedFields(old, next api.Resource) []string {
	old.Spec, next.Spec = comparableSpec(old.Kind, old.Spec), comparableSpec(next.Kind, next.Spec)
	defer clear(old.Spec)
	defer clear(next.Spec)
	fields := make([]string, 0, 16)
	if !reflect.DeepEqual(old.Metadata.Name, next.Metadata.Name) {
		fields = append(fields, "metadata.name")
	}
	a, _ := json.Marshal(old.Metadata.Labels)
	b, _ := json.Marshal(next.Metadata.Labels)
	if !jsonEqual(a, b) {
		fields = append(fields, "metadata.labels")
	}
	clear(a)
	clear(b)
	var visit func(string, json.RawMessage, json.RawMessage, int)
	visit = func(path string, a, b json.RawMessage, depth int) {
		if jsonEqual(a, b) || len(fields) >= 64 {
			return
		}
		var am, bm map[string]json.RawMessage
		if depth > 8 || json.Unmarshal(a, &am) != nil || json.Unmarshal(b, &bm) != nil || am == nil || bm == nil {
			fields = append(fields, path)
			return
		}
		defer func() {
			for _, raw := range am {
				clear(raw)
			}
			for _, raw := range bm {
				clear(raw)
			}
		}()
		keys := maps.Clone(am)
		maps.Copy(keys, bm)
		for _, key := range slices.Sorted(maps.Keys(keys)) {
			if jsonEqual(am[key], bm[key]) {
				continue
			}
			child := path + "." + key
			if api.SafeValidationField(child) == "" {
				// Unsupported future object shapes remain readable, but their
				// unreviewed property names are not safe terminal diagnostics.
				fields = append(fields, path)
				return
			}
			switch key {
			case "check", "recovery", "driver", "config", "tls", "autoIgnore", "notifications", "red", "yellow", "green", "cyan", "gray", "blue":
				visit(child, am[key], bm[key], depth+1)
			default:
				fields = append(fields, child)
			}
			if len(fields) >= 64 {
				return
			}
		}
	}
	visit("spec", old.Spec, next.Spec, 0)
	if len(fields) >= 64 {
		// A coarse, explicit summary is complete without a truncated list.
		return []string{"metadata", "spec"}
	}
	slices.Sort(fields)
	return slices.Compact(fields)
}
