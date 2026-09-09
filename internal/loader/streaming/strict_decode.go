package streaming

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// Custom schema decoders resolve interface fields but bypass decoder-wide
// unknown-field settings. Validate the original keys against the resolved
// concrete values after decoding, including nested notification configs.
func decodeJSONValue(decoder *json.Decoder, target any, strict bool) error {
	if !strict {
		return decoder.Decode(target)
	}
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return err
	}
	// Parse JSON with its own escape rules before constructing validation nodes.
	// YAML does not accept every valid JSON escape, including surrogate pairs.
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return err
	}
	var node yaml.Node
	if err := node.Encode(tree); err != nil {
		return err
	}
	return knownFields(&node, reflect.ValueOf(target), "", "json", 0)
}

func decodeYAMLValue(node *yaml.Node, target any, strict bool) error {
	if err := node.Decode(target); err != nil {
		return err
	}
	if !strict {
		return nil
	}
	return knownFields(node, reflect.ValueOf(target), "", "yaml", 0)
}

func knownFields(node *yaml.Node, value reflect.Value, path, format string, depth int) error {
	if depth > 128 {
		return fmt.Errorf("configuration nesting exceeds strict validation limit")
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		return knownFields(node.Content[0], value, path, format, depth+1)
	}
	if node.Kind == yaml.AliasNode {
		return knownFields(node.Alias, value, path, format, depth+1)
	}
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return nil
	}
	if node.Kind == yaml.SequenceNode && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
		for n, child := range node.Content {
			if n < value.Len() {
				if err := knownFields(child, value.Index(n), fmt.Sprintf("%s[%d]", path, n), format, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	fields := map[string]int{}
	var fieldNames []string
	if value.Kind() == reflect.Struct {
		fieldNames = make([]string, value.NumField())
		for n := 0; n < value.NumField(); n++ {
			field := value.Type().Field(n)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get(format), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
				if format == "yaml" {
					name = strings.ToLower(name)
				}
			}
			fields[name] = n
			fieldNames[n] = name
		}
	}
	entries, err := effectiveMappingFields(node, depth)
	if err != nil {
		return err
	}
	for n := 0; n < len(entries); n += 2 {
		key, child := entries[n], entries[n+1]
		childPath := path + "." + key.Value
		switch value.Kind() {
		case reflect.Struct:
			index, ok := fields[key.Value]
			if !ok && format == "json" {
				// encoding/json prefers an exact match, then the first
				// case-insensitive match in declaration order.
				for n, name := range fieldNames {
					if name != "" && strings.EqualFold(name, key.Value) {
						index, ok = n, true
						break
					}
				}
			}
			if !ok {
				return fmt.Errorf("unknown configuration field %q", strings.TrimPrefix(childPath, "."))
			}
			if err := knownFields(child, value.Field(index), childPath, format, depth+1); err != nil {
				return err
			}
		case reflect.Map:
			if value.Type().Key().Kind() == reflect.String {
				mapKey := reflect.ValueOf(key.Value).Convert(value.Type().Key())
				if err := knownFields(child, value.MapIndex(mapKey), childPath, format, depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// YAML merge keys supply defaults: explicit keys override every merged mapping,
// and an earlier mapping in a merge sequence overrides later mappings. Validate
// only the effective values, matching the concrete value decoded by yaml.v3.
func effectiveMappingFields(node *yaml.Node, depth int) ([]*yaml.Node, error) {
	var entries []*yaml.Node
	seen := make(map[string]bool)
	var collect func(*yaml.Node, int) error
	collect = func(current *yaml.Node, level int) error {
		if level > 128 {
			return fmt.Errorf("configuration nesting exceeds strict validation limit")
		}
		switch current.Kind {
		case yaml.AliasNode:
			return collect(current.Alias, level+1)
		case yaml.SequenceNode:
			for _, child := range current.Content {
				if err := collect(child, level+1); err != nil {
					return err
				}
			}
		case yaml.MappingNode:
			for n := 0; n < len(current.Content); n += 2 {
				key := current.Content[n]
				if key.Tag != "!!merge" && !seen[key.Value] {
					seen[key.Value] = true
					entries = append(entries, key, current.Content[n+1])
				}
			}
			for n := 0; n < len(current.Content); n += 2 {
				if current.Content[n].Tag == "!!merge" {
					if err := collect(current.Content[n+1], level+1); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := collect(node, depth); err != nil {
		return nil, err
	}
	return entries, nil
}
