package loader

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

// MonitorSource retains one location per streamed monitor, not its contents or
// syntax tree. JSON offsets refer to the decompressed original input.
type MonitorSource struct {
	Item, Line, Column int
	Offset             int64
	ParserContext      bool
}

// Error annotates local validation failures. JSON line counting is performed
// only on failure, in a bounded streaming pass; valid fleet loading keeps no
// fleet-wide newline index. Failure to reopen leaves coordinates unavailable.
func (s MonitorSource) Error(ctx context.Context, filename string, cause error) error {
	if s.Line == 0 && s.Offset >= 0 {
		s.Line, s.Column = inputPosition(ctx, filename, s.Offset)
	}
	location := fmt.Sprintf("%s document 1", filename)
	if s.Item > 0 {
		location += fmt.Sprintf(" monitor %d", s.Item)
	}
	if s.Line > 0 && s.Column > 0 {
		if s.ParserContext {
			location += fmt.Sprintf(" near line %d column %d (parser context)", s.Line, s.Column)
		} else {
			location += fmt.Sprintf(" at line %d column %d", s.Line, s.Column)
		}
	}
	return fmt.Errorf("%s: %w", location, cause)
}

func inputPosition(ctx context.Context, filename string, offset int64) (int, int) {
	f, err := os.Open(filename)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var reader io.Reader = f
	if strings.HasSuffix(strings.ToLower(filename), ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, 0
		}
		defer gz.Close()
		reader = gz
	}
	line, column := 1, 1
	var buffer [32 << 10]byte
	for offset > 0 {
		if ctx.Err() != nil {
			return 0, 0
		}
		n, err := io.ReadFull(reader, buffer[:min(offset, int64(len(buffer)))])
		if err != nil {
			return 0, 0
		}
		for _, c := range buffer[:n] {
			if c == '\n' {
				line++
				column = 1
			} else if utf8.RuneStart(c) {
				column++
			}
		}
		offset -= int64(n)
	}
	return line, column
}

var fieldParts = regexp.MustCompile(`[^.\[\]]+`)

func fieldKey(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "")) }

// Shared driver diagnostics call recovery input config; file manifests use
// target. Other names are matched without case/underscore differences.
func diagnosticPath(cause error) []string {
	detail, ok := api.ValidationFieldError(cause)
	if !ok {
		return nil
	}
	field := strings.ReplaceAll(detail.Field, "intervention.config", "intervention.target")
	return fieldParts.FindAllString(field, -1)
}

func yamlDiagnosticNode(node *yaml.Node, parts []string, depth int) *yaml.Node {
	if node == nil || depth > 128 {
		return node
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return yamlDiagnosticNode(node.Content[0], parts, depth+1)
	}
	if node.Kind == yaml.AliasNode {
		return yamlDiagnosticNode(node.Alias, parts, depth+1)
	}
	if len(parts) == 0 {
		return node
	}
	if node.Kind == yaml.SequenceNode {
		index, err := strconv.Atoi(parts[0])
		if err == nil && index >= 0 && index < len(node.Content) {
			return yamlDiagnosticNode(node.Content[index], parts[1:], depth+1)
		}
	}
	if node.Kind == yaml.MappingNode {
		entries, err := effectiveMappingFields(node, depth)
		if err != nil {
			return node
		}
		if index, err := strconv.Atoi(parts[0]); err == nil {
			keys := make([]string, 0, len(entries)/2)
			for i := 0; i < len(entries); i += 2 {
				keys = append(keys, entries[i].Value)
			}
			slices.Sort(keys)
			if index >= 0 && index < len(keys) {
				for i := 0; i < len(entries); i += 2 {
					if entries[i].Value == keys[index] {
						return yamlDiagnosticNode(entries[i+1], parts[1:], depth+1)
					}
				}
			}
		} else {
			for i := 0; i < len(entries); i += 2 {
				if fieldKey(entries[i].Value) == fieldKey(parts[0]) {
					return yamlDiagnosticNode(entries[i+1], parts[1:], depth+1)
				}
			}
		}
	}
	// A missing field points at the nearest existing enclosing object.
	return node
}

func yamlSourceError(ctx context.Context, filename string, item int, node *yaml.Node, cause error) error {
	point := yamlDiagnosticNode(node, diagnosticPath(cause), 0)
	source := MonitorSource{Item: item, Offset: -1}
	if point != nil {
		source.Line, source.Column = point.Line, point.Column
	}
	return source.Error(ctx, filename, cause)
}

// YAML map decoding does not prefix errors returned by custom element
// unmarshallers. Recover an endpoint's stable ordinal without copying its name.
func yamlMetadataError(node *yaml.Node, cause error) error {
	if _, ok := api.ValidationFieldError(cause); !ok {
		return cause
	}
	root := yamlDiagnosticNode(node, nil, 0)
	if root == nil || root.Kind != yaml.MappingNode {
		return cause
	}
	rootFields, err := effectiveMappingFields(root, 0)
	if err != nil {
		return cause
	}
	var endpoints *yaml.Node
	for i := 0; i < len(rootFields); i += 2 {
		if rootFields[i].Value == "endpoints" {
			endpoints = yamlDiagnosticNode(rootFields[i+1], nil, 0)
			break
		}
	}
	if endpoints == nil || endpoints.Kind != yaml.MappingNode {
		return cause
	}
	entries, err := effectiveMappingFields(endpoints, 0)
	if err != nil {
		return cause
	}
	values := make(map[string]*yaml.Node, len(entries)/2)
	keys := make([]string, 0, len(entries)/2)
	for i := 0; i < len(entries); i += 2 {
		keys = append(keys, entries[i].Value)
		values[entries[i].Value] = entries[i+1]
	}
	slices.Sort(keys)
	for index, key := range keys {
		var endpoint manifest.Endpoint
		if err := values[key].Decode(&endpoint); err != nil {
			if _, ok := api.ValidationFieldError(err); ok {
				return api.PrefixValidationError(fmt.Sprintf("endpoints[%d]", index), err)
			}
		}
	}
	return cause
}

func yamlSyntaxError(ctx context.Context, filename string, item int, cause error, originalLine func(int) int) error {
	source := MonitorSource{Item: item, Offset: -1}
	line := 1
	if match := yamlErrorLine.FindStringSubmatch(cause.Error()); len(match) == 2 {
		if parsed, err := strconv.Atoi(match[1]); err == nil && parsed > 0 {
			line = parsed
		}
	}
	// yaml.v3 exposes only a textual parser-context line for syntax failures,
	// sometimes omitting it for the first line. It supplies neither the failing
	// field nor a column. Do not present this context as an exact value position.
	detail := error(api.InvalidField("", "invalidYAML"))
	if line = originalLine(line); line > 0 {
		detail = fmt.Errorf("near line %d (parser context; column unavailable): %w", line, detail)
	}
	return source.Error(ctx, filename, detail)
}

// Walk just the requested field on failure. json.Decoder supplies exact offsets
// with JSON's escaping rules, including surrogate pairs that YAML cannot parse.
func jsonFieldOffset(raw []byte, parts []string, depth int) int64 {
	if len(parts) == 0 || depth > 128 {
		return 0
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil {
		return 0
	}
	index, indexErr := strconv.Atoi(parts[0])
	selectedKey := ""
	if first == json.Delim('{') && indexErr == nil {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return 0
		}
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		if index < 0 || index >= len(keys) {
			return 0
		}
		selectedKey = keys[index]
	}
	if first != json.Delim('{') && first != json.Delim('[') {
		return 0
	}
	for ordinal := 0; decoder.More(); ordinal++ {
		matches := indexErr == nil && index == ordinal
		if first == json.Delim('{') {
			key, err := decoder.Token()
			if err != nil {
				return 0
			}
			value, ok := key.(string)
			if !ok {
				return 0
			}
			matches = indexErr == nil && value == selectedKey || indexErr != nil && fieldKey(value) == fieldKey(parts[0])
		}
		var child json.RawMessage
		if decoder.Decode(&child) != nil {
			return 0
		}
		if matches {
			return decoder.InputOffset() - int64(len(child)) + jsonFieldOffset(child, parts[1:], depth+1)
		}
	}
	return 0
}

func jsonSourceError(ctx context.Context, filename string, item int, raw []byte, end int64, cause error) error {
	return jsonFieldSourceError(ctx, filename, item, raw, end, diagnosticPath(cause), cause)
}

func jsonFieldSourceError(ctx context.Context, filename string, item int, raw []byte, end int64, parts []string, cause error) error {
	start := end - int64(len(raw))
	position := jsonFieldOffset(raw, parts, 0)
	var validation *api.ValidationError
	if errors.As(cause, &validation) && validation.Offset > 0 && validation.Reason != "invalidType" {
		if len(raw) == 0 {
			start, position = 0, validation.Offset-1
		} else if validation.Offset <= int64(len(raw)) {
			position = validation.Offset - 1
		}
	}
	var cursor *jsonCursorError
	return (MonitorSource{Item: item, Offset: max(0, start+position), ParserContext: errors.As(cause, &cursor)}).Error(ctx, filename, cause)
}
