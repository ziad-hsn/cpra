package loader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

// Custom schema decoders resolve interface fields but bypass decoder-wide
// unknown-field settings. Validate the original keys against the resolved
// concrete values after decoding, including nested notification configs.
func decodeJSONValue(decoder *json.Decoder, target any, strict bool) error {
	_, err := decodeJSONResource(decoder, target, strict)
	return err
}

func decodeJSONResource(decoder *json.Decoder, target any, strict bool) ([]byte, error) {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return raw, jsonInputError(err, "")
	}
	if len(raw) > 1<<20 {
		return raw, api.InvalidField("", "resourceTooLarge")
	}
	if !strict {
		return raw, jsonInputError(json.Unmarshal(raw, target), "")
	}
	if err := api.StrictDecode(raw, target); err != nil {
		return raw, err
	}
	// Parse with JSON's escape rules before resolving interface fields. Synthetic
	// validation nodes do not carry source coordinates; callers retain raw spans.
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return raw, jsonInputError(err, "")
	}
	var node yaml.Node
	if err := node.Encode(tree); err != nil {
		return raw, api.InvalidField("", "invalidType")
	}
	err := knownFields(&node, reflect.ValueOf(target), "", "json", 0)
	if detail, ok := api.ValidationFieldError(err); ok {
		err = api.InvalidField(detail.Field, detail.Reason)
	}
	return raw, err
}

func decodeYAMLValue(node *yaml.Node, target any, strict bool) error {
	if err := duplicateYAMLField(node, "", 0); err != nil {
		return err
	}
	if err := node.Decode(target); err != nil {
		if detail, ok := api.ValidationFieldError(err); ok {
			return api.InvalidField(detail.Field, detail.Reason)
		}
		return yamlDecodeError(node, err)
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
	if format == "yaml" && node.Kind == yaml.ScalarNode && node.Tag != "!!null" {
		valid := true
		switch value.Kind() {
		case reflect.String:
			valid = node.Tag == "!!str"
		case reflect.Bool:
			valid = node.Tag == "!!bool"
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			valid = node.Tag == "!!int" || value.Type() == reflect.TypeOf(time.Duration(0)) && node.Tag == "!!str"
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			valid = node.Tag == "!!int"
		case reflect.Float32, reflect.Float64:
			valid = node.Tag == "!!float" || node.Tag == "!!int"
		case reflect.Slice:
			// StringList permits a string shorthand, not YAML's scalar coercion.
			if value.Type() == reflect.TypeOf(manifest.StringList{}) {
				valid = node.Tag == "!!str"
			}
		}
		if !valid {
			return yamlFieldError(node, path, "invalidType")
		}
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
	seenFields := make(map[int]bool)
	for n := 0; n < len(entries); n += 2 {
		key, child := entries[n], entries[n+1]
		if format == "yaml" && (key.Kind != yaml.ScalarNode || key.Tag != "!!str") {
			return yamlFieldError(key, path, "invalidType")
		}
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
				return yamlFieldError(key, path, "unknownField")
			}
			if seenFields[index] {
				return yamlFieldError(key, path, "duplicateField")
			}
			seenFields[index] = true
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

func yamlFieldError(node *yaml.Node, path, reason string) error {
	return fmt.Errorf("line %d column %d: %w", node.Line, node.Column, api.InvalidField(strings.TrimPrefix(path, "."), reason))
}

func duplicateYAMLField(node *yaml.Node, path string, depth int) error {
	if depth > 128 {
		return api.InvalidField("", "invalidResource")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if seen[key.Value] {
				return yamlFieldError(key, path, "duplicateField")
			}
			seen[key.Value] = true
			if err := duplicateYAMLField(node.Content[i+1], strings.TrimPrefix(path+"."+key.Value, "."), depth+1); err != nil {
				return err
			}
		}
	} else {
		for i, child := range node.Content {
			childPath := path
			if node.Kind == yaml.SequenceNode {
				childPath += fmt.Sprintf("[%d]", i)
			}
			if err := duplicateYAMLField(child, childPath, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

var yamlErrorLine = regexp.MustCompile(`(?:^|\n| )line ([0-9]+):`)

func yamlDecodeError(node *yaml.Node, cause error) error {
	line, column := node.Line, node.Column
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		line, column = node.Content[0].Line, node.Content[0].Column
	}
	if match := yamlErrorLine.FindStringSubmatch(cause.Error()); len(match) == 2 {
		if value, err := strconv.Atoi(match[1]); err == nil && value > 0 {
			line, column = value, 1
		}
	}
	return fmt.Errorf("line %d column %d: %w", line, column, api.InvalidField("", "invalidType"))
}
func jsonInputError(err error, field string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if detail, ok := api.ValidationFieldError(err); ok {
		return api.InvalidField(detail.Field, detail.Reason)
	}
	cause := api.InvalidField(field, "invalidJSON")
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		cause.Offset = syntax.Offset
	}
	return cause
}
