package collection

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

func (b *builder) parseYAML(ctx context.Context, input io.Reader, source string) error {
	scan := bufio.NewScanner(input)
	scan.Buffer(make([]byte, 64<<10), b.o.MaxResourceBytes+1)
	var metadata, entry bytes.Buffer
	document, item := 1, 0
	lineNumber, entryLine := 0, 0
	firstColumn := 1
	if source, ok := input.(*sourceReader); ok {
		lineNumber, firstColumn = source.point.Line-1, source.point.Column
	}
	firstLine := lineNumber + 1
	var metadataLines []int
	sequence := ""
	indent := -1
	documentHasContent := false
	emitEntry := func() error {
		if entry.Len() == 0 {
			return nil
		}
		value, root, err := b.decodeYAMLDocument(entry.Bytes())
		positions := yamlPositions(root, func(line int) int { return entryLine + line - 1 })
		adjustFirstColumn(positions, firstLine, firstColumn)
		entry.Reset()
		if err != nil {
			return parseSourceError(err, Location{Source: source, Document: document, Item: item + 1, Line: entryLine}, func(line int) int { return entryLine + line - 1 })
		}
		array, ok := value.([]any)
		if !ok || len(array) != 1 {
			return fmt.Errorf("document %d: expected one block entry", document)
		}
		item++
		return b.emit(array[0], Location{Source: source, Document: document, Item: item}, sequence == "monitors", subsetPositions(positions, "/0"))
	}
	finish := func() error {
		if err := emitEntry(); err != nil {
			return err
		}
		if metadata.Len() > 0 {
			value, root, err := b.decodeYAMLDocument(metadata.Bytes())
			positions := yamlPositions(root, func(line int) int {
				if line > 0 && line <= len(metadataLines) {
					return metadataLines[line-1]
				}
				return 0
			})
			adjustFirstColumn(positions, firstLine, firstColumn)
			if err != nil {
				return parseSourceError(err, Location{Source: source, Document: document, Item: item + 1}, func(line int) int {
					if line > 0 && line <= len(metadataLines) {
						return metadataLines[line-1]
					}
					return 0
				})
			}
			if value != nil {
				if err := b.document(ctx, value, Location{Source: source, Document: document, Item: item}, positions); err != nil {
					return err
				}
			}
		}
		metadata.Reset()
		metadataLines = nil
		entry.Reset()
		sequence = ""
		indent = -1
		item = 0
		documentHasContent = false
		return nil
	}
	for scan.Scan() {
		lineNumber++
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scan.Text()
		trim := strings.TrimSpace(line)
		leading := len(line) - len(strings.TrimLeft(line, " "))
		if leading == 0 && (trim == "---" || strings.HasPrefix(trim, "--- #") || trim == "...") {
			if documentHasContent {
				if err := finish(); err != nil {
					return err
				}
				document++
			}
			continue
		}
		blank := trim == "" || strings.HasPrefix(trim, "#")
		if !blank {
			documentHasContent = true
		}
		isItem := trim == "-" || strings.HasPrefix(trim, "- ")
		if sequence != "" && !blank && leading == 0 && !isItem {
			if err := emitEntry(); err != nil {
				return err
			}
			sequence = ""
			indent = -1
		}
		if sequence == "" && leading == 0 {
			if key, ok := blockHeader(line); ok && (key == "monitors" || key == "items") {
				sequence = key
				indent = -1
				if metadata.Len()+len(key)+5 > b.o.MaxDocumentBytes {
					return fmt.Errorf("document metadata exceeds limit")
				}
				metadata.WriteString(key + ": []\n")
				metadataLines = append(metadataLines, lineNumber)
				continue
			}
		}
		if sequence != "" {
			if !blank {
				if indent < 0 {
					if !isItem {
						return fmt.Errorf("document %d: %s requires block entries", document, sequence)
					}
					indent = leading
				}
				if leading < indent {
					return fmt.Errorf("document %d: invalid block indentation", document)
				}
				if isItem && leading == indent {
					if err := emitEntry(); err != nil {
						return err
					}
				}
			}
			if entry.Len()+len(line)+1 > b.o.MaxResourceBytes {
				return fmt.Errorf("document %d item %d exceeds resource limit", document, item+1)
			}
			if !blank || entry.Len() > 0 {
				if entry.Len() == 0 {
					entryLine = lineNumber
				}
				entry.WriteString(line)
				entry.WriteByte('\n')
			}
		} else {
			if metadata.Len()+len(line)+1 > b.o.MaxDocumentBytes {
				return fmt.Errorf("document %d metadata exceeds limit", document)
			}
			metadataLines = append(metadataLines, lineNumber)
			metadata.WriteString(line)
			metadata.WriteByte('\n')
		}
	}
	if err := scan.Err(); err != nil {
		return fmt.Errorf("source line exceeds resource limit or cannot be read")
	}
	return finish()
}

func blockHeader(line string) (string, bool) {
	before, after, ok := strings.Cut(line, ":")
	if !ok {
		return "", false
	}
	after = strings.TrimSpace(after)
	if after != "" && !strings.HasPrefix(after, "#") {
		return "", false
	}
	before = strings.TrimSpace(before)
	if len(before) > 1 && ((before[0] == '\'' && before[len(before)-1] == '\'') || (before[0] == '"' && before[len(before)-1] == '"')) {
		before = before[1 : len(before)-1]
	}
	return before, true
}

func decodeYAML(data []byte) (any, error) {
	return (&builder{}).decodeYAML(data)
}

func (b *builder) decodeYAML(data []byte) (any, error) {
	value, _, err := b.decodeYAMLDocument(data)
	return value, err
}
func (b *builder) decodeYAMLDocument(data []byte) (any, *yaml.Node, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, &document, yamlSyntaxError(err)
	}
	if len(document.Content) == 0 {
		return nil, &document, nil
	}
	if b.normalizationProfile == fileNormalizationV1 || !yamlExpansion(document.Content[0], 0) {
		value, err := nodeValue(document.Content[0], 0)
		return value, &document, err
	}
	decoder := yamlValues{nodes: 65_536, bytes: 1 << 20, active: make(map[*yaml.Node]bool)}
	value, err := decoder.value(document.Content[0], 0)
	return value, &document, err
}

func nodeValue(node *yaml.Node, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("nesting depth exceeds 64")
	}
	switch node.Kind {
	case yaml.MappingNode:
		result := make(map[string]any, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return nil, fmt.Errorf("mapping keys must be strings")
			}
			if _, exists := result[key.Value]; exists {
				return nil, &SourceError{Location: Location{Line: key.Line, Column: key.Column}, Cause: api.InvalidField("", "duplicateField")}
			}
			value, err := nodeValue(node.Content[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			result[key.Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := nodeValue(child, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, nil
	case yaml.AliasNode:
		return nil, fmt.Errorf("YAML aliases are unsupported; use explicit values")
	case yaml.ScalarNode:
		if node.Tag == "!!timestamp" {
			return node.Value, nil
		}
		if node.Tag == "!!float" {
			return yamlNumber(node.Value)
		}
		var value any
		if err := node.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid YAML scalar")
		}
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported YAML node")
	}
}

// yamlNumber changes only YAML decimal spelling into JSON spelling. Keeping the
// token avoids float64 rounding and underflow before typed resource validation.
// Non-finite values and unsupported numeric forms cannot enter a JSON resource.
func yamlNumber(value string) (json.Number, error) {
	value = strings.ReplaceAll(value, "_", "")
	value = strings.TrimPrefix(value, "+")
	sign := ""
	if strings.HasPrefix(value, "-") {
		sign, value = "-", value[1:]
	}
	exponent := ""
	if index := strings.IndexAny(value, "eE"); index >= 0 {
		value, exponent = value[:index], value[index:]
	}
	whole, fraction, point := strings.Cut(value, ".")
	if whole == "" && fraction == "" {
		return "", fmt.Errorf("YAML number cannot be represented as JSON")
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	value = sign + whole
	if point {
		if fraction == "" {
			fraction = "0"
		}
		value += "." + fraction
	}
	number := json.Number(value + exponent)
	if _, err := json.Marshal(number); err != nil {
		return "", fmt.Errorf("YAML number cannot be represented as JSON")
	}
	return number, nil
}

// readJSONValue is a bounded lexical reader: a giant single string cannot cause
// the unbounded allocation that decoding a RawMessage before checking its size
// would allow. JSON syntax and duplicate keys are checked afterward.
func readJSONValue(r byteReader, limit int) ([]byte, error) {
	if _, err := peekNonspace(r); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	depth := 0
	quoted, escaped := false, false
	for {
		p, err := r.Peek(1)
		if err == io.EOF {
			if quoted || depth != 0 {
				return nil, fmt.Errorf("incomplete JSON value")
			}
			break
		}
		if err != nil {
			return nil, err
		}
		c := p[0]
		if buffer.Len() > 0 && !quoted && depth == 0 && (c == ',' || c == ']' || c == '}' || c == ' ' || c == '\t' || c == '\n' || c == '\r') {
			break
		}
		if buffer.Len() >= limit {
			return nil, fmt.Errorf("JSON value exceeds %d bytes", limit)
		}
		_, _ = r.ReadByte()
		buffer.WriteByte(c)
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
		} else {
			switch c {
			case '"':
				quoted = true
			case '{', '[':
				depth++
				if depth > 64 {
					return nil, fmt.Errorf("nesting depth exceeds 64")
				}
			case '}', ']':
				depth--
				if depth < 0 {
					return nil, fmt.Errorf("invalid JSON value")
				}
			}
		}
		if !quoted && depth == 0 && (c == '}' || c == ']' || c == '"') {
			break
		}
	}
	if buffer.Len() == 0 {
		return nil, fmt.Errorf("missing JSON value")
	}
	return buffer.Bytes(), nil
}

func peekNonspace(r byteReader) (byte, error) {
	for {
		p, err := r.Peek(1)
		if err != nil {
			return 0, err
		}
		switch p[0] {
		case ' ', '\n', '\r', '\t':
			_, _ = r.ReadByte()
		default:
			return p[0], nil
		}
	}
}
func punctuation(r byteReader, want byte) error {
	c, err := peekNonspace(r)
	if err != nil {
		return err
	}
	if c != want {
		return fmt.Errorf("expected JSON punctuation %q", want)
	}
	_, _ = r.ReadByte()
	return nil
}

func (b *builder) parseJSON(ctx context.Context, r byteReader, source string) error {
	for document := 1; ; document++ {
		first, err := peekNonspace(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if first != '{' && first != '[' {
			return &SourceError{Location: locationAt(Location{Source: source, Document: document}, readerPosition(r)), Cause: api.InvalidField("", "invalidJSON")}
		}
		if err := b.parseJSONDocument(ctx, r, source, document); err != nil {
			var positioned *SourceError
			if errors.As(err, &positioned) {
				return err
			}
			return &SourceError{Location: locationAt(Location{Source: source, Document: document}, readerPosition(r)), Cause: err}
		}
	}
}

func (b *builder) parseJSONDocument(ctx context.Context, r byteReader, source string, document int) error {
	first, err := peekNonspace(r)
	if err != nil {
		return err
	}
	location := Location{Source: source, Document: document}
	start := readerPosition(r)
	positions := map[string]SourcePosition{"": start}
	if first == '[' {
		if err = b.jsonArray(ctx, r, location, false); err != nil {
			return err
		}
	} else {
		if err = punctuation(r, '{'); err != nil {
			return err
		}
		fields := make(map[string]any)
		seen := make(map[string]bool)
		metadataBytes := 0
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			c, e := peekNonspace(r)
			if e != nil {
				return e
			}
			if c == '}' {
				_, _ = r.ReadByte()
				break
			}
			raw, e := readJSONValue(r, 1024)
			if e != nil {
				return e
			}
			var key string
			if e = json.Unmarshal(raw, &key); e != nil {
				return fmt.Errorf("invalid JSON key")
			}
			if seen[key] {
				return fmt.Errorf("duplicate top-level JSON key")
			}
			seen[key] = true
			if e = punctuation(r, ':'); e != nil {
				return e
			}
			if key == "monitors" || key == "items" {
				if e = b.jsonArray(ctx, r, location, key == "monitors"); e != nil {
					return e
				}
				fields[key] = []any{}
			} else {
				if _, e = peekNonspace(r); e != nil {
					return e
				}
				fieldStart := readerPosition(r)
				raw, e = readJSONValue(r, b.o.MaxDocumentBytes-metadataBytes)
				if e != nil {
					return e
				}
				metadataBytes += len(raw)
				value, e := decodeJSON(raw)
				if e != nil {
					return jsonSourceError(raw, e, locationAt(location, fieldStart))
				}
				for field, point := range jsonPositions(raw, fieldStart) {
					name := sourcePathChild("", key) + field
					if len(positions) < 65536 {
						positions[name] = point
					}
				}
				fields[key] = value
			}
			c, e = peekNonspace(r)
			if e != nil {
				return e
			}
			if c == '}' {
				_, _ = r.ReadByte()
				break
			}
			if e = punctuation(r, ','); e != nil {
				return e
			}
			c, e = peekNonspace(r)
			if e != nil {
				return e
			}
			if c == '}' {
				return fmt.Errorf("trailing JSON comma")
			}
		}
		if err = b.document(ctx, fields, location, positions); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) jsonArray(ctx context.Context, r byteReader, location Location, manifest bool) error {
	if err := punctuation(r, '['); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := peekNonspace(r)
		if err != nil {
			return err
		}
		if c == ']' {
			_, _ = r.ReadByte()
			return nil
		}
		start := readerPosition(r)
		raw, err := readJSONValue(r, b.o.MaxResourceBytes)
		location.Item++
		location = locationAt(location, start)
		if err != nil {
			return &SourceError{Location: locationAt(location, readerPosition(r)), Cause: api.InvalidField("", "invalidJSON")}
		}
		value, err := decodeJSON(raw)
		if err != nil {
			return jsonSourceError(raw, err, location)
		}
		if err = b.emit(value, location, manifest, jsonPositions(raw, start)); err != nil {
			return err
		}
		c, err = peekNonspace(r)
		if err != nil {
			return err
		}
		if c == ']' {
			_, _ = r.ReadByte()
			return nil
		}
		if err = punctuation(r, ','); err != nil {
			return err
		}
		c, err = peekNonspace(r)
		if err != nil {
			return err
		}
		if c == ']' {
			return fmt.Errorf("trailing JSON comma")
		}
	}
}

func decodeJSON(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := jsonValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing JSON")
	}
	return value, nil
}

// jsonValue checks duplicate keys and depth while retaining exact numeric
// tokens. Passing JSON through YAML scalar decoding would round some numbers
// into different values and even turn invalid fractional controls into integers.
func jsonValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("nesting depth exceeds 64")
	}
	token, err := d.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid JSON")
	}
	switch token {
	case json.Delim('{'):
		value := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, fmt.Errorf("invalid JSON key")
			}
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("invalid JSON key")
			}
			if _, exists := value[name]; exists {
				return nil, fmt.Errorf("duplicate JSON key")
			}
			child, err := jsonValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			value[name] = child
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("invalid JSON object")
		}
		return value, nil
	case json.Delim('['):
		value := []any{}
		for d.More() {
			child, err := jsonValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			value = append(value, child)
		}
		if end, err := d.Token(); err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("invalid JSON array")
		}
		return value, nil
	default:
		if _, delimiter := token.(json.Delim); delimiter {
			return nil, fmt.Errorf("invalid JSON value")
		}
		return token, nil
	}
}

func (b *builder) document(ctx context.Context, value any, location Location, positions map[string]SourcePosition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	coordinates := indexPositions(positions)
	if value == nil {
		return nil
	}
	if list, ok := value.([]any); ok {
		for index, item := range list {
			if err := ctx.Err(); err != nil {
				return err
			}
			location.Item++
			if err := b.emit(item, location, false, coordinates.subset(fmt.Sprintf("/%d", index))); err != nil {
				return err
			}
		}
		return nil
	}
	m, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("document must contain a resource, list, or monitor manifest")
	}
	if kind, ok := m["kind"].(string); ok {
		if kind == "List" {
			if m["apiVersion"] != api.APIVersion {
				return fmt.Errorf("unsupported List apiVersion")
			}
			for key := range m {
				if key != "apiVersion" && key != "kind" && key != "metadata" && key != "items" {
					return fmt.Errorf("unknown List field")
				}
			}
			items, ok := m["items"].([]any)
			if !ok {
				return fmt.Errorf("List.items must be an array")
			}
			for index, item := range items {
				if err := ctx.Err(); err != nil {
					return err
				}
				location.Item++
				if err := b.emit(item, location, false, coordinates.subset(fmt.Sprintf("/items/%d", index))); err != nil {
					return err
				}
			}
			return nil
		}
		location.Item++
		return b.emit(m, location, false, positions)
	}
	for key := range m {
		if key != "version" && key != "monitors" && key != "endpoints" && key != "notification_groups" {
			return fmt.Errorf("unknown monitor manifest field")
		}
	}
	if len(m) == 0 {
		return fmt.Errorf("empty mapping is not a resource")
	}
	for _, entry := range []struct{ field, kind string }{{"endpoints", "NotificationEndpoint"}, {"notification_groups", "NotificationGroup"}} {
		value, exists := m[entry.field]
		if !exists {
			continue
		}
		definitions, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("manifest %s must be a mapping", entry.field)
		}
		keys := make([]string, 0, len(definitions))
		for key := range definitions {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := ctx.Err(); err != nil {
				return err
			}
			location.Item++
			sourceFields := coordinates.subset(sourcePathChild("/"+entry.field, key))
			fields := make(map[string]SourcePosition)
			for field, point := range sourceFields {
				name := "/spec"
				if entry.kind == "NotificationGroup" {
					name += "/endpointRefs"
				}
				name += field
				fields[name] = point
			}
			location = locationAt(location, sourceFields[""])
			var spec any
			if entry.kind == "NotificationGroup" {
				spec = map[string]any{"endpointRefs": definitions[key]}
			} else {
				endpoint, ok := definitions[key].(map[string]any)
				if !ok {
					return fmt.Errorf("manifest endpoint must be an object")
				}
				for field := range endpoint {
					if field != "type" && field != "config" {
						return fmt.Errorf("unknown manifest endpoint field")
					}
				}
				driver, err := driverFromManifest("notification", endpoint["type"], endpoint["config"])
				if err != nil {
					return (Item{Location: location, FieldPositions: resourceFieldPositions(fields, false)}).ValidationError(api.PrefixValidationError("spec", err))
				}
				spec = driver
			}
			resource, err := envelope(entry.kind, key, key, spec)
			if err != nil {
				return err
			}

			if err = b.add(resource, location, resourceFieldPositions(fields, false)); err != nil {
				return err
			}
		}
	}
	if monitors, exists := m["monitors"]; exists {
		list, ok := monitors.([]any)
		if !ok {
			return fmt.Errorf("manifest monitors must be an array")
		}
		for index, item := range list {
			if err := ctx.Err(); err != nil {
				return err
			}
			location.Item++
			if err := b.emit(item, location, true, coordinates.subset(fmt.Sprintf("/monitors/%d", index))); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *builder) emit(value any, location Location, manifest bool, positions map[string]SourcePosition) error {
	location = locationAt(location, positions[""])
	fields := resourceFieldPositions(positions, manifest)
	m, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: resource must be an object", location)
	}
	var resource api.Resource
	var err error
	if manifest {
		resource, err = monitorFromManifestProfile(m, b.normalizationProfile == fileNormalizationV1 || b.allowDestinationConflict)
	} else {
		for key := range m {
			switch key {
			case "apiVersion", "kind", "metadata", "spec", "status":
			default:
				return (Item{Location: location, FieldPositions: fields}).ValidationError(api.InvalidField("", "unknownField"))
			}
		}
		encoded, e := json.Marshal(m)
		if e != nil {
			return fmt.Errorf("%s: invalid resource scalar", location)
		}
		resource, err = api.DecodeResource(encoded)
	}
	if err != nil {
		return (Item{Location: location, FieldPositions: fields}).ValidationError(err)
	}
	return b.add(resource, location, fields)
}

func envelope(kind, id, name string, spec any) (api.Resource, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return api.Resource{}, err
	}
	return api.Resource{APIVersion: api.APIVersion, Kind: kind, Metadata: api.Metadata{ID: id, Name: api.Pointer(name)}, Spec: raw}, nil
}

func monitorFromManifest(m map[string]any) (api.Resource, error) {
	return monitorFromManifestProfile(m, false)
}

func monitorFromManifestProfile(m map[string]any, allowDestinationConflict bool) (api.Resource, error) {
	for key := range m {
		switch key {
		case "id", "name", "pulse_check", "codes", "intervention", "enabled", "tags", "maintenance", "auto_ignore":
		default:
			return api.Resource{}, fmt.Errorf("unknown manifest monitor field")
		}
	}
	name, _ := m["name"].(string)
	id, _ := m["id"].(string)
	if value, exists := m["id"]; exists {
		if _, ok := value.(string); !ok {
			return api.Resource{}, fmt.Errorf("manifest id must be a string")
		}
	}
	if value, exists := m["name"]; exists {
		if _, ok := value.(string); !ok {
			return api.Resource{}, fmt.Errorf("manifest name must be a string")
		}
	}
	if id == "" {
		if name == "" {
			return api.Resource{}, fmt.Errorf("manifest monitor name is required")
		}
		sum := sha256.Sum256([]byte(name))
		id = "name:" + hex.EncodeToString(sum[:])
	}
	spec := make(map[string]any)
	if value, ok := m["auto_ignore"]; ok {
		spec["autoIgnore"] = value
	}
	for _, key := range []string{"enabled", "tags", "maintenance"} {
		if value, ok := m[key]; ok {
			spec[key] = value
		}
	}
	pulse, ok := m["pulse_check"].(map[string]any)
	if !ok {
		return api.Resource{}, fmt.Errorf("manifest monitor pulse_check is required")
	}
	check := make(map[string]any)
	for key, value := range pulse {
		switch key {
		case "type", "config":
		case "interval", "timeout", "groups", "retries":
			check[key] = value
		case "max_failures":
			check["maxFailures"] = value
		case "unhealthy_threshold":
			check["unhealthyThreshold"] = value
		case "healthy_threshold":
			check["healthyThreshold"] = value
		default:
			return api.Resource{}, fmt.Errorf("unknown manifest pulse field")
		}
	}
	if groups, ok := check["groups"].(string); ok {
		check["groups"] = []string{groups}
	}
	if value, exists := check["unhealthyThreshold"]; !exists || isUnsetOrZeroInteger(value) {
		if maxFailures, exists := check["maxFailures"]; exists && isPositiveInteger(maxFailures) {
			check["unhealthyThreshold"] = maxFailures
		}
	}
	driver, err := driverFromManifest("check", pulse["type"], pulse["config"])
	if err != nil {
		return api.Resource{}, api.PrefixValidationError("spec.check.driver", err)
	}
	// Manifest per-driver zero inherits positive pulse retries. Typed resource
	// configuration preserves explicit zero, so resolve manifest defaults here.
	if isPositiveInteger(check["retries"]) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(driver.Config, &fields); err != nil {
			return api.Resource{}, fmt.Errorf("invalid manifest check configuration")
		}
		raw, exists := fields["retries"]
		if !exists || isUnsetOrZeroInteger(raw) {
			fields["retries"], _ = json.Marshal(check["retries"])
			driver.Config, _ = json.Marshal(fields)
		}
	}
	check["driver"] = driver
	spec["check"] = check
	if intervention, exists := m["intervention"]; exists && intervention != nil {
		object, ok := intervention.(map[string]any)
		if !ok {
			return api.Resource{}, fmt.Errorf("manifest intervention must be an object")
		}
		recovery := make(map[string]any)
		for key, value := range object {
			switch key {
			case "action", "target":
			case "retries":
				recovery[key] = value
			case "max_attempts":
				recovery["maxAttempts"] = value
			case "max_failures":
				recovery["maxFailures"] = value
			default:
				return api.Resource{}, fmt.Errorf("unknown manifest intervention field")
			}
		}
		driver, err := driverFromManifest("recovery", object["action"], object["target"])
		if err != nil {
			return api.Resource{}, api.PrefixValidationError("spec.recovery.driver", err)
		}
		recovery["driver"] = driver
		spec["recovery"] = recovery
	}
	if codes, exists := m["codes"]; exists {
		object, ok := codes.(map[string]any)
		if !ok {
			return api.Resource{}, fmt.Errorf("manifest codes must be an object")
		}
		rules := make(map[string]any)
		for color, value := range object {
			code, ok := value.(map[string]any)
			if !ok {
				return api.Resource{}, fmt.Errorf("manifest code must be an object")
			}
			for key := range code {
				switch key {
				case "dispatch", "notify", "notify_group", "config", "trigger":
				default:
					return api.Resource{}, fmt.Errorf("unknown manifest code field")
				}
			}
			if value, exists := code["notify_group"]; !allowDestinationConflict && exists && value != nil {
				if _, ok := value.(string); !ok {
					return api.Resource{}, fmt.Errorf("notification group must be a string")
				}
			}
			rule := make(map[string]any)
			if trigger, exists := code["trigger"]; exists {
				rule["trigger"] = trigger
			}
			if dispatch, exists := code["dispatch"]; exists {
				rule["dispatch"] = dispatch
			}
			if group, ok := code["notify_group"].(string); ok && group != "" {
				if !allowDestinationConflict {
					notify, supplied := code["notify"]
					if supplied && notify != nil && notify != "" || code["config"] != nil {
						return api.Resource{}, fmt.Errorf("notification group conflicts with inline destination")
					}
				}
				rule["groupRef"] = group
			} else {
				driver, err := driverFromManifest("notification", code["notify"], code["config"])
				if err != nil {
					return api.Resource{}, api.PrefixValidationError(api.SafeValidationField("spec.notifications."+color+".driver"), err)
				}
				rule["driver"] = driver
			}
			rules[color] = rule
		}
		spec["notifications"] = rules
	}
	return envelope("Monitor", id, name, spec)
}

func isUnsetOrZeroInteger(value any) bool {
	if value == nil {
		return true
	}
	raw, err := json.Marshal(value)
	var number int64
	return err == nil && json.Unmarshal(raw, &number) == nil && number == 0
}

func isPositiveInteger(value any) bool {
	raw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var number int64
	return json.Unmarshal(raw, &number) == nil && number > 0
}

func driverFromManifest(category string, kindValue, config any) (api.DriverConfig, error) {
	kind, ok := kindValue.(string)
	if !ok || kind == "" {
		return api.DriverConfig{}, fmt.Errorf("manifest driver type is required")
	}
	if _, ok := config.(map[string]any); !ok {
		return api.DriverConfig{}, fmt.Errorf("manifest driver config must be an object")
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return api.DriverConfig{}, fmt.Errorf("invalid manifest driver config")
	}
	normalized, err := api.NormalizeManifestDriver(category, kind, raw)
	if err != nil {
		return api.DriverConfig{}, err
	}
	return api.DriverConfig{Type: kind, Config: normalized}, nil
}
