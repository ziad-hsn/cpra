package collection

import (
	"errors"
	"github.com/ziad-hsn/cpra/sdk/go/api"

	"gopkg.in/yaml.v3"
)

// Each streamed entry has its own YAML document, so an alias cannot retain a
// preceding monitor or refer into a later one. Count every expansion, including
// mapping keys, before allocating its result. A tiny anchor graph must not turn
// into an unbounded collection before the encoded-resource limit is checked.
type yamlValues struct {
	nodes, bytes int
	active       map[*yaml.Node]bool
}

func yamlExpansion(n *yaml.Node, depth int) bool {
	if n.Kind == yaml.AliasNode || n.Tag == "!!merge" {
		return true
	}
	// The ordinary decoder rejects excess nesting. Do not scan deeper merely
	// to select the decoder, or traverse Alias pointers in this inspection.
	if depth > 64 {
		return false
	}
	for _, child := range n.Content {
		if yamlExpansion(child, depth+1) {
			return true
		}
	}
	return false
}

func (d *yamlValues) value(n *yaml.Node, depth int) (any, error) {
	if n == nil || depth > 64 {
		return nil, errors.New("YAML nesting depth exceeds 64")
	}
	d.nodes--
	d.bytes -= len(n.Value)
	if d.nodes < 0 || d.bytes < 0 {
		return nil, errors.New("expanded YAML entry exceeds limit")
	}
	if d.active[n] {
		return nil, errors.New("cyclic YAML alias")
	}
	d.active[n] = true
	defer delete(d.active, n)
	switch n.Kind {
	case yaml.AliasNode:
		return d.value(n.Alias, depth+1)
	case yaml.MappingNode:
		out := make(map[string]any)
		seen := make(map[string]bool)
		var merge *yaml.Node
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" && key.Tag != "!!merge" {
				return nil, errors.New("mapping keys must be strings")
			}
			if seen[key.Value] {
				return nil, &SourceError{Location: Location{Line: key.Line, Column: key.Column}, Cause: api.InvalidField("", "duplicateField")}
			}
			seen[key.Value] = true
			d.nodes--
			d.bytes -= len(key.Value)
			if d.nodes < 0 || d.bytes < 0 {
				return nil, errors.New("expanded YAML entry exceeds limit")
			}
			if key.Tag == "!!merge" {
				merge = n.Content[i+1]
				continue
			}
			v, err := d.value(n.Content[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			out[key.Value] = v
		}
		if merge != nil {
			value, err := d.value(merge, depth+1)
			if err != nil {
				return nil, err
			}
			// Explicit keys override merged keys. In a merge sequence an
			// earlier map overrides a later map (YAML merge-key contract).
			maps, sequence := value.([]any)
			if !sequence {
				maps = []any{value}
			}
			for _, value := range maps {
				m, ok := value.(map[string]any)
				if !ok {
					return nil, errors.New("YAML merge requires a mapping or sequence of mappings")
				}
				for key, v := range m {
					if _, exists := out[key]; !exists {
						out[key] = v
					}
				}
			}
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, min(len(n.Content), max(d.nodes, 0)))
		for _, child := range n.Content {
			v, err := d.value(child, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.ScalarNode:
		// Preserve the original scalar and JSON-number rules across profiles.
		return nodeValue(n, depth)
	default:
		return nil, errors.New("unsupported YAML node")
	}
}
