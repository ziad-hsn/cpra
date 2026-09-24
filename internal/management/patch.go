package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// PreparePatch applies RFC 7396 merge semantics to a single observed resource.
// Arrays replace; null removes optional properties. Only desired spec, display
// name and labels are patchable. The merged resource follows full validation.
func (c *Catalog) PreparePatch(ctx context.Context, kind, id, version string, patch []byte) (*PreparedChange, error) {
	if len(patch) == 0 || len(patch) > api.MaxResourceBytes {
		return nil, ErrValidation
	}
	var fields map[string]json.RawMessage
	if api.StrictDecode(patch, &fields) != nil || fields == nil {
		return nil, ErrValidation
	}
	for key := range fields {
		if key != "metadata" && key != "spec" {
			return nil, errors.Join(ErrValidation, errors.New("patch can change only desired spec, name and labels"))
		}
	}
	if raw, ok := fields["metadata"]; ok {
		var metadata map[string]json.RawMessage
		if api.StrictDecode(raw, &metadata) != nil || metadata == nil {
			return nil, ErrValidation
		}
		for key := range metadata {
			if key != "name" && key != "labels" {
				return nil, ErrValidation
			}
		}
	}
	if kind == "Credential" {
		var spec map[string]json.RawMessage
		if raw, ok := fields["spec"]; ok && (api.StrictDecode(raw, &spec) != nil || spec == nil) {
			return nil, ErrValidation
		}
		if raw, ok := spec["value"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, errors.Join(ErrValidation, errors.New("credential value cannot be removed; delete an unreferenced credential explicitly"))
		}
	}
	current, err := c.Get(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if current.Metadata.ResourceVersion != version {
		return nil, persistence.ErrCatalogConflict
	}
	raw, _ := json.Marshal(current)
	var original, delta any
	if decodeNumbers(raw, &original) != nil || decodeNumbers(patch, &delta) != nil {
		return nil, ErrValidation
	}
	merged, err := json.Marshal(mergeValue(original, delta))
	if err != nil {
		return nil, ErrValidation
	}
	resource, err := api.DecodeResource(merged)
	if err != nil {
		return nil, ErrValidation
	}
	return c.Prepare(ctx, resource, version, false)
}

func decodeNumbers(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d.Decode(out)
}

func mergeValue(original, delta any) any {
	patch, ok := delta.(map[string]any)
	if !ok {
		return delta
	}
	base, ok := original.(map[string]any)
	if !ok {
		base = map[string]any{}
	}
	for key, value := range patch {
		if value == nil {
			delete(base, key)
		} else {
			base[key] = mergeValue(base[key], value)
		}
	}
	return base
}
