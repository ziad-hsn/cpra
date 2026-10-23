package collection

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

// SourcePosition identifies an original source character, using one-based lines
// and columns. A zero position means unavailable; it is never a resource value.
type SourcePosition struct {
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
	Field  string `json:"field,omitempty"` // Original reviewed schema spelling, never a free-form mapping key.
}

// SourceError associates a safe validation cause with its original local input.
// It does not copy the rejected scalar, source contents or provider response.
type SourceError struct {
	Location Location
	Cause    error
	Field    string // Original field spelling when retained safely.
}

func (e *SourceError) Error() string {
	if field, ok := api.ValidationFieldError(e.Cause); ok {
		name := api.SafeValidationField(e.Field)
		if name == "" {
			name = field.Field
		}
		return fmt.Sprintf("%s: %s: %s", e.Location, name, api.ValidationMessage(field.Reason))
	}
	return fmt.Sprintf("%s: %s", e.Location, e.Cause)
}
func (e *SourceError) Unwrap() error { return e.Cause }

// ValidationError locates a static validation failure in this item's original
// source. Field positions are private staging metadata, not part of the wire
// resource or its inventory identity. Missing fields use their enclosing object.
func (i Item) ValidationError(err error) error {
	if err == nil {
		return nil
	}
	location := i.Location
	originalField := ""
	if field, ok := api.ValidationFieldError(err); ok {
		path := sourceFieldKey(field.Field)
		for {
			if point, ok := i.FieldPositions[path]; ok && point.Line > 0 && point.Column > 0 {
				location.Line, location.Column = point.Line, point.Column
				originalField = api.SafeValidationField(point.Field)
				break
			}
			end := strings.LastIndexAny(path, ".[ ")
			if end < 0 {
				break
			}
			path = path[:end]
		}
	}
	return &SourceError{Location: location, Cause: err, Field: originalField}
}

// Normalize spelling only for coordinate lookup. Actual resource keys, schema
// checks, normalized bytes and error field names keep their original contracts.
func sourceFieldKey(field string) string { return strings.ToLower(strings.ReplaceAll(field, "_", "")) }

func resourceFieldPositions(fields map[string]SourcePosition, manifest bool) map[string]SourcePosition {
	out := make(map[string]SourcePosition)
	for _, field := range slices.Sorted(maps.Keys(fields)) {
		point := fields[field]
		if len(out) >= 1024 {
			break
		}
		field = canonicalSourcePath(field)
		if field == "" {
			continue
		}
		canonical := field
		if manifest {
			switch {
			case field == "name":
				canonical = "metadata.name"
			case field == "id":
				canonical = "metadata.id"
			case field == "enabled":
				canonical = "spec.enabled"
			case strings.HasPrefix(field, "pulse_check.config"):
				canonical = "spec.check.driver" + strings.TrimPrefix(field, "pulse_check")
			case field == "pulse_check.type":
				canonical = "spec.check.driver.type"
			case field == "pulse_check" || strings.HasPrefix(field, "pulse_check."):
				canonical = "spec.check" + strings.TrimPrefix(field, "pulse_check")
			case strings.HasPrefix(field, "intervention.target"):
				canonical = "spec.recovery.driver.config" + strings.TrimPrefix(field, "intervention.target")
			case field == "intervention.action":
				canonical = "spec.recovery.driver.type"
			case field == "intervention" || strings.HasPrefix(field, "intervention."):
				canonical = "spec.recovery" + strings.TrimPrefix(field, "intervention")
			case field == "auto_ignore" || strings.HasPrefix(field, "auto_ignore."):
				canonical = "spec.autoIgnore" + strings.TrimPrefix(field, "auto_ignore")
			case strings.HasPrefix(field, "codes."):
				parts := strings.SplitN(strings.TrimPrefix(field, "codes."), ".", 2)
				canonical = "spec.notifications." + parts[0]
				if len(parts) == 2 {
					switch {
					case parts[1] == "notify":
						canonical += ".driver.type"
					case parts[1] == "config" || strings.HasPrefix(parts[1], "config."):
						canonical += ".driver." + parts[1]
					case parts[1] == "notify_group":
						canonical += ".groupRef"
					default:
						canonical += "." + parts[1]
					}
				}
			default:
				canonical = "spec." + field
			}
		}
		if safeSourceField(canonical) && point.Line > 0 && point.Column > 0 {
			point.Field = api.SafeValidationField(field)
			out[sourceFieldKey(canonical)] = point
		}
	}
	return out
}

func subsetPositions(fields map[string]SourcePosition, prefix string) map[string]SourcePosition {
	return indexPositions(fields).subset(prefix)
}

type sourcePositionIndex struct {
	fields map[string]SourcePosition
	paths  []string
}

func indexPositions(fields map[string]SourcePosition) sourcePositionIndex {
	return sourcePositionIndex{fields: fields, paths: slices.Sorted(maps.Keys(fields))}
}

// Each resource visits only its own coordinate range. Building the document
// index once avoids rescanning every other endpoint's fields for every entry.
func (index sourcePositionIndex) subset(prefix string) map[string]SourcePosition {
	result := make(map[string]SourcePosition)
	if point, ok := index.fields[prefix]; ok {
		result[""] = point
	}
	start, _ := slices.BinarySearch(index.paths, prefix+"/")
	for _, field := range index.paths[start:] {
		if !strings.HasPrefix(field, prefix+"/") {
			break
		}
		result[strings.TrimPrefix(field, prefix)] = index.fields[field]
	}
	return result
}

// Source positions are best-effort diagnostic metadata bounded independently
// of resource acceptance. Overflow drops excess coordinates, never input data.
func yamlPositions(root *yaml.Node, line func(int) int) map[string]SourcePosition {
	result := make(map[string]SourcePosition)
	remaining := 65536
	active := make(map[*yaml.Node]bool)
	var visit func(*yaml.Node, string, int)
	visit = func(n *yaml.Node, path string, depth int) {
		remaining--
		if n == nil || depth > 64 || remaining < 0 || active[n] || len(path) > 160 {
			return
		}
		active[n] = true
		defer delete(active, n)
		if n.Kind == yaml.DocumentNode {
			if len(n.Content) > 0 {
				visit(n.Content[0], path, depth+1)
			}
			return
		}
		if _, exists := result[path]; !exists {
			result[path] = SourcePosition{Line: line(n.Line), Column: n.Column}
		}
		switch n.Kind {
		case yaml.AliasNode:
			visit(n.Alias, path, depth+1)
		case yaml.SequenceNode:
			for i, child := range n.Content {
				visit(child, sourcePathChild(path, strconv.Itoa(i)), depth+1)
			}
		case yaml.MappingNode:
			for _, entry := range effectivePositionFields(n, &remaining) {
				child := sourcePathChild(path, entry.key)
				visit(entry.node, child, depth+1)
			}
		}
	}
	visit(root, "", 0)
	return result
}

type byteReader interface {
	ReadByte() (byte, error)
	Peek(int) ([]byte, error)
}
type sourceReader struct {
	*bufio.Reader
	point SourcePosition
}

func newSourceReader(r io.Reader) *sourceReader {
	reader := bufio.NewReader(r)
	if prefix, _ := reader.Peek(3); bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = reader.Discard(3) // Counted by the source quota, not a visible column.
	}
	return &sourceReader{Reader: reader, point: SourcePosition{Line: 1, Column: 1}}
}
func advancePosition(point SourcePosition, data []byte) SourcePosition {
	for _, c := range data {
		if c == '\n' {
			point.Line++
			point.Column = 1
		} else if utf8.RuneStart(c) {
			point.Column++
		}
	}
	return point
}
func (r *sourceReader) ReadByte() (byte, error) {
	c, err := r.Reader.ReadByte()
	if err == nil {
		r.point = advancePosition(r.point, []byte{c})
	}
	return c, err
}
func (r *sourceReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.point = advancePosition(r.point, p[:n])
	return n, err
}
func readerPosition(r byteReader) SourcePosition {
	if source, ok := r.(*sourceReader); ok {
		return source.point
	}
	return SourcePosition{}
}

func jsonPositions(raw []byte, start SourcePosition) map[string]SourcePosition {
	result := make(map[string]SourcePosition)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	offset := 0
	point := start
	remaining := 65536
	position := func() SourcePosition {
		end := int(decoder.InputOffset())
		for end < len(raw) && (raw[end] == ' ' || raw[end] == '\n' || raw[end] == '\r' || raw[end] == '\t' || raw[end] == ',' || raw[end] == ':') {
			end++
		}
		if end >= offset {
			point = advancePosition(point, raw[offset:end])
			offset = end
		}
		return point
	}
	var visit func(string, int) error
	visit = func(path string, depth int) error {
		remaining--
		if remaining < 0 || depth > 64 {
			return errors.New("position limit")
		}
		at := position()
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if len(path) <= 160 {
			result[path] = at
		}
		switch token {
		case json.Delim('{'):
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("invalid key")
				}
				child := sourcePathChild(path, name)
				if err := visit(child, depth+1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
		case json.Delim('['):
			for i := 0; decoder.More(); i++ {
				if err := visit(sourcePathChild(path, strconv.Itoa(i)), depth+1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
		}
		return err
	}
	_ = visit("", 0)
	return result
}

func locationAt(location Location, point SourcePosition) Location {
	location.Line, location.Column = point.Line, point.Column
	return location
}

type positionField struct {
	key  string
	node *yaml.Node
}

func effectivePositionFields(root *yaml.Node, budget *int) []positionField {
	fields := []positionField{}
	seen := map[string]bool{}
	active := map[*yaml.Node]bool{}
	var collect func(*yaml.Node, int)
	collect = func(n *yaml.Node, depth int) {
		*budget--
		if n == nil || depth > 64 || *budget < 0 || active[n] {
			return
		}
		active[n] = true
		defer delete(active, n)
		switch n.Kind {
		case yaml.AliasNode:
			collect(n.Alias, depth+1)
		case yaml.SequenceNode:
			for _, child := range n.Content {
				collect(child, depth+1)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				key := n.Content[i]
				if key.Tag != "!!merge" && !seen[key.Value] {
					*budget--
					if *budget < 0 {
						return
					}
					seen[key.Value] = true
					fields = append(fields, positionField{key.Value, n.Content[i+1]})
				}
			}
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Tag == "!!merge" {
					collect(n.Content[i+1], depth+1)
				}
			}
		}
	}
	collect(root, 0)
	return fields
}
func adjustFirstColumn(positions map[string]SourcePosition, line, column int) {
	if column <= 1 {
		return
	}
	for field, point := range positions {
		if point.Line == line {
			point.Column += column - 1
			positions[field] = point
		}
	}
}

func sourcePathChild(parent, key string) string {
	return parent + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}
func canonicalSourcePath(pointer string) string {
	var path string
	for _, encoded := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		key := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		if key == "" || strings.ContainsAny(key, ".[]/") {
			return ""
		}
		if _, err := strconv.ParseUint(key, 10, 32); err == nil && path != "" {
			path += "[" + key + "]"
			continue
		}
		if path != "" {
			path += "."
		}
		path += key
	}
	return path
}

func safeSourceField(path string) bool {
	if api.SafeValidationField(path) == "" {
		return false
	}
	parts := strings.Split(sourceFieldKey(path), ".")
	for i, part := range parts {
		name, _, indexed := strings.Cut(part, "[")
		switch name {
		case "headers", "labels", "credentialrefs", "parameters", "parameterschema", "resultschema", "status":
			// Numeric mapping keys and array elements are both descendants of
			// the free-form value, including when they end the field path.
			if indexed || i < len(parts)-1 {
				return false
			}
		case "notifications", "prioritybycolor":
			if indexed {
				return false
			}
			if i == len(parts)-1 {
				continue
			}
			switch parts[i+1] {
			case "yellow", "red", "green", "cyan", "gray", "blue":
			default:
				return false
			}
		}
	}
	return true
}

var yamlSyntaxLine = regexp.MustCompile(`(?:^| )line ([0-9]+):`)

func yamlSyntaxError(err error) error {
	location := Location{}
	if match := yamlSyntaxLine.FindStringSubmatch(err.Error()); len(match) == 2 {
		location.Line, _ = strconv.Atoi(match[1])
	}
	return &SourceError{Location: location, Cause: api.InvalidField("", "invalidYAML")}
}
func parseSourceError(err error, location Location, line func(int) int) error {
	var previous *SourceError
	if errors.As(err, &previous) {
		if previous.Location.Line > 0 {
			location.Line = line(previous.Location.Line)
			location.Column = previous.Location.Column
		}
		err = previous.Cause
	}
	return &SourceError{Location: location, Cause: err}
}
func jsonSourceError(raw []byte, err error, location Location) error {
	var value json.RawMessage
	var syntax *json.SyntaxError
	if json.Unmarshal(raw, &value) != nil {
		if cause := json.Unmarshal(raw, &value); errors.As(cause, &syntax) && syntax.Offset > 0 && syntax.Offset <= int64(len(raw)) {
			point := advancePosition(SourcePosition{Line: location.Line, Column: location.Column}, raw[:syntax.Offset-1])
			location.Line, location.Column = point.Line, point.Column
		}
	}
	// All token/decoder text is discarded, including through Unwrap.
	reason := "invalidJSON"
	if field, ok := api.ValidationFieldError(err); ok {
		reason = field.Reason
	}
	return &SourceError{Location: location, Cause: api.InvalidField("", reason)}
}
