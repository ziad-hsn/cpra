//go:build externaljobs

package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"regexp"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	jobSchemaProfile = "cpra.schema.v1"
	jobSchemaBytes   = 64 << 10
	jobValueBytes    = 128 << 10
	jobJSONNodes     = 8192
	jobSchemaNodes   = 1024
	jobSchemaDepth   = 32
	jobSchemaWork    = 100000
	jobSchemaScan    = 8 << 20
)

var (
	ErrJobSchemaInvalid = errors.New("invalid or unsupported job schema")
	ErrJobSchemaBudget  = errors.New("job schema validation limit exceeded")
	ErrJobValueInvalid  = errors.New("job data does not satisfy its registered schema")
)

// jobSchema implements the bounded cpra.schema.v1 vocabulary. Schema and data
// parsing never fetch references, invoke custom validators, or expose input in errors.
type jobSchema struct{ root *jobSchemaNode }

type jobSchemaNode struct {
	deny                         bool
	types                        map[string]bool
	properties                   map[string]*jobSchemaNode
	additional, items, reference *jobSchemaNode
	additionalDenied             bool
	required                     []string
	enum                         []any
	constant                     any
	hasConstant                  bool
	minLength, maxLength         *int
	minItems, maxItems           *int
	minProperties, maxProperties *int
	minimum, maximum             *big.Rat
	exclusiveMin, exclusiveMax   *big.Rat
	multipleOf                   *big.Rat
	pattern                      *regexp.Regexp
	patternCost                  int
	ref                          string
}

type jobSchemaCompiler struct {
	ctx                 context.Context
	nodes               []*jobSchemaNode
	defs                map[string]*jobSchemaNode
	patternInstructions int
}

func compileJobSchema(ctx context.Context, raw []byte) (*jobSchema, error) {
	v, err := decodeJobJSON(ctx, raw, jobSchemaBytes)
	if err != nil {
		return nil, jobSchemaError(ctx, err)
	}
	c := jobSchemaCompiler{ctx: ctx, defs: make(map[string]*jobSchemaNode)}
	root, err := c.compile(v, 0)
	if err != nil {
		return nil, err
	}
	for _, n := range c.nodes {
		if n.ref != "" {
			if !strings.HasPrefix(n.ref, "#/$defs/") {
				return nil, ErrJobSchemaInvalid
			}
			name := strings.TrimPrefix(n.ref, "#/$defs/")
			// References name one root definition. URI fragments, anchors and
			// nested pointer traversal are outside this profile.
			if strings.ContainsAny(name, "/%") {
				return nil, ErrJobSchemaInvalid
			}
			name, err = jobPointerName(name)
			if err != nil || c.defs[name] == nil {
				return nil, ErrJobSchemaInvalid
			}
			n.reference = c.defs[name]
		}
	}
	visiting, heights := map[*jobSchemaNode]bool{}, map[*jobSchemaNode]int{}
	var walk func(*jobSchemaNode) (int, error)
	walk = func(n *jobSchemaNode) (int, error) {
		if n == nil {
			return 0, nil
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if visiting[n] {
			return 0, ErrJobSchemaInvalid
		}
		if height := heights[n]; height != 0 {
			return height, nil
		}
		visiting[n] = true
		height := 1
		for _, child := range []*jobSchemaNode{n.reference, n.additional, n.items} {
			h, err := walk(child)
			if err != nil {
				return 0, err
			}
			height = max(height, h+1)
		}
		for _, child := range n.properties {
			h, err := walk(child)
			if err != nil {
				return 0, err
			}
			height = max(height, h+1)
		}
		delete(visiting, n)
		if height > jobSchemaDepth+1 {
			return 0, ErrJobSchemaInvalid
		}
		heights[n] = height
		return height, nil
	}
	// Unused definitions must also be valid and nonrecursive.
	for _, n := range c.nodes {
		if _, err := walk(n); err != nil {
			return nil, err
		}
	}
	return &jobSchema{root: root}, nil
}

func jobPointerName(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '~' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i == len(s) || s[i] != '0' && s[i] != '1' {
			return "", ErrJobSchemaInvalid
		}
		if s[i] == '0' {
			b.WriteByte('~')
		} else {
			b.WriteByte('/')
		}
	}
	return b.String(), nil
}

func (c *jobSchemaCompiler) compile(v any, depth int) (*jobSchemaNode, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if depth > jobSchemaDepth || len(c.nodes) >= jobSchemaNodes {
		return nil, ErrJobSchemaBudget
	}
	n := &jobSchemaNode{}
	c.nodes = append(c.nodes, n)
	if b, ok := v.(bool); ok {
		n.deny = !b
		return n, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ErrJobSchemaInvalid
	}
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := m[key]
		switch key {
		case "$schema":
			if depth != 0 || value != "https://json-schema.org/draft/2020-12/schema" {
				return nil, ErrJobSchemaInvalid
			}
		case "title", "description":
			if s, ok := value.(string); !ok || len(s) > 1024 {
				return nil, ErrJobSchemaInvalid
			}
		case "$ref":
			s, ok := value.(string)
			if !ok || s == "" || len(s) > 256 {
				return nil, ErrJobSchemaInvalid
			}
			n.ref = s
		case "$defs", "properties":
			definitions, ok := value.(map[string]any)
			if !ok || key == "$defs" && depth != 0 {
				return nil, ErrJobSchemaInvalid
			}
			children := make(map[string]*jobSchemaNode, len(definitions))
			for name, definition := range definitions {
				if len(name) > 256 {
					return nil, ErrJobSchemaInvalid
				}
				child, err := c.compile(definition, depth+1)
				if err != nil {
					return nil, err
				}
				children[name] = child
			}
			if key == "$defs" {
				c.defs = children
			} else {
				n.properties = children
			}
		case "type":
			values, ok := value.([]any)
			if !ok {
				values = []any{value}
			}
			if len(values) == 0 {
				return nil, ErrJobSchemaInvalid
			}
			n.types = map[string]bool{}
			for _, v := range values {
				s, ok := v.(string)
				if !ok || n.types[s] {
					return nil, ErrJobSchemaInvalid
				}
				switch s {
				case "object", "array", "string", "number", "integer", "boolean", "null":
					n.types[s] = true
				default:
					return nil, ErrJobSchemaInvalid
				}
			}
		case "required":
			values, ok := value.([]any)
			if !ok {
				return nil, ErrJobSchemaInvalid
			}
			seen := map[string]bool{}
			for _, v := range values {
				s, ok := v.(string)
				if !ok || len(s) > 256 || seen[s] {
					return nil, ErrJobSchemaInvalid
				}
				seen[s] = true
				n.required = append(n.required, s)
			}
		case "additionalProperties", "items":
			child, err := c.compile(value, depth+1)
			if err != nil {
				return nil, err
			}
			if key == "items" {
				n.items = child
			} else if b, ok := value.(bool); ok && !b {
				n.additionalDenied = true
			} else {
				n.additional = child
			}
		case "const":
			n.constant, n.hasConstant = value, true
		case "enum":
			values, ok := value.([]any)
			if !ok || len(values) == 0 || len(values) > 64 {
				return nil, ErrJobSchemaInvalid
			}
			n.enum = values
		case "minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties":
			r, err := jobNumber(value)
			if err != nil || !r.IsInt() || !r.Num().IsInt64() || r.Sign() < 0 || r.Num().Int64() > jobValueBytes {
				return nil, ErrJobSchemaInvalid
			}
			limit := int(r.Num().Int64())
			switch key {
			case "minLength":
				n.minLength = &limit
			case "maxLength":
				n.maxLength = &limit
			case "minItems":
				n.minItems = &limit
			case "maxItems":
				n.maxItems = &limit
			case "minProperties":
				n.minProperties = &limit
			case "maxProperties":
				n.maxProperties = &limit
			}
		case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf":
			r, err := jobNumber(value)
			if err != nil || key == "multipleOf" && r.Sign() <= 0 {
				return nil, ErrJobSchemaInvalid
			}
			switch key {
			case "minimum":
				n.minimum = r
			case "maximum":
				n.maximum = r
			case "exclusiveMinimum":
				n.exclusiveMin = r
			case "exclusiveMaximum":
				n.exclusiveMax = r
			case "multipleOf":
				n.multipleOf = r
			}
		case "pattern":
			s, ok := value.(string)
			if !ok || len(s) > 256 {
				return nil, ErrJobSchemaInvalid
			}
			tree, err := syntax.Parse(s, syntax.Perl)
			if err != nil {
				return nil, ErrJobSchemaInvalid
			}
			program, err := syntax.Compile(tree.Simplify())
			if err != nil || len(program.Inst) > 1024 {
				return nil, ErrJobSchemaBudget
			}
			c.patternInstructions += len(program.Inst)
			if c.patternInstructions > 16384 {
				return nil, ErrJobSchemaBudget
			}
			n.patternCost = max(1, len(program.Inst))
			n.pattern, err = regexp.Compile(s)
			if err != nil {
				return nil, ErrJobSchemaInvalid
			}
		default:
			return nil, ErrJobSchemaInvalid
		}
	}
	return n, nil
}

func jobSchemaError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrJobSchemaBudget) {
		return err
	}
	return ErrJobSchemaInvalid
}

func decodeJobJSON(ctx context.Context, raw []byte, maxBytes int) (any, error) {
	if ctx == nil {
		return nil, ErrJobSchemaInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(raw) > maxBytes {
		return nil, ErrJobSchemaBudget
	}
	if !utf8.Valid(raw) {
		return nil, ErrJobSchemaInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	nodes := 0
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		nodes++
		if depth > jobSchemaDepth || nodes > jobJSONNodes {
			return nil, ErrJobSchemaBudget
		}
		t, err := d.Token()
		if err != nil {
			return nil, ErrJobSchemaInvalid
		}
		switch t {
		case json.Delim('{'):
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, ErrJobSchemaInvalid
				}
				name, ok := key.(string)
				if !ok {
					return nil, ErrJobSchemaInvalid
				}
				if _, exists := m[name]; exists {
					return nil, ErrJobSchemaInvalid
				}
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				m[name] = v
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, ErrJobSchemaInvalid
			}
			return m, nil
		case json.Delim('['):
			var a []any
			for d.More() {
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, ErrJobSchemaInvalid
			}
			return a, nil
		}
		if _, ok := t.(json.Delim); ok {
			return nil, ErrJobSchemaInvalid
		}
		if _, ok := t.(json.Number); ok {
			if _, err := jobNumber(t); err != nil {
				return nil, err
			}
		}
		return t, nil
	}
	v, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrJobSchemaInvalid
	}
	return v, nil
}

func jobNumber(v any) (*big.Rat, error) {
	n, ok := v.(json.Number)
	if !ok || len(n) > 128 {
		return nil, ErrJobSchemaInvalid
	}
	if at := strings.IndexAny(string(n), "eE"); at >= 0 {
		exp, err := strconv.Atoi(string(n)[at+1:])
		if err != nil || exp < -308 || exp > 308 {
			return nil, ErrJobSchemaInvalid
		}
	}
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return nil, ErrJobSchemaInvalid
	}
	return r, nil
}

type jobSchemaBudget struct {
	ctx          context.Context
	steps, bytes int
}

func (b *jobSchemaBudget) use(scanned int) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	b.steps++
	b.bytes += scanned
	if b.steps > jobSchemaWork || b.bytes > jobSchemaScan {
		return ErrJobSchemaBudget
	}
	return nil
}

func (s *jobSchema) validate(ctx context.Context, raw []byte) error {
	v, err := decodeJobJSON(ctx, raw, jobValueBytes)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrJobSchemaBudget) {
			return err
		}
		return ErrJobValueInvalid
	}
	if s == nil || s.root == nil {
		return ErrJobSchemaInvalid
	}
	return s.root.validate(v, &jobSchemaBudget{ctx: ctx}, 0)
}

func jobLength(n int, min, max *int) bool {
	return (min == nil || n >= *min) && (max == nil || n <= *max)
}

func (n *jobSchemaNode) validate(v any, b *jobSchemaBudget, depth int) error {
	if err := b.use(0); err != nil {
		return err
	}
	if depth > jobSchemaDepth {
		return ErrJobSchemaBudget
	}
	if n.deny {
		return ErrJobValueInvalid
	}
	if n.reference != nil {
		if err := n.reference.validate(v, b, depth+1); err != nil {
			return err
		}
	}
	kind := "null"
	switch v.(type) {
	case map[string]any:
		kind = "object"
	case []any:
		kind = "array"
	case string:
		kind = "string"
	case bool:
		kind = "boolean"
	case json.Number:
		kind = "number"
	}
	if len(n.types) != 0 && !n.types[kind] {
		if kind != "number" || !n.types["integer"] {
			return ErrJobValueInvalid
		}
		r, _ := jobNumber(v)
		if !r.IsInt() {
			return ErrJobValueInvalid
		}
	}
	if n.hasConstant {
		ok, err := jobEqual(v, n.constant, b)
		if err != nil {
			return err
		}
		if !ok {
			return ErrJobValueInvalid
		}
	}
	if n.enum != nil {
		found := false
		for _, candidate := range n.enum {
			equal, err := jobEqual(v, candidate, b)
			if err != nil {
				return err
			}
			if equal {
				found = true
				break
			}
		}
		if !found {
			return ErrJobValueInvalid
		}
	}
	switch value := v.(type) {
	case map[string]any:
		if !jobLength(len(value), n.minProperties, n.maxProperties) {
			return ErrJobValueInvalid
		}
		for _, required := range n.required {
			if err := b.use(len(required)); err != nil {
				return err
			}
			if _, ok := value[required]; !ok {
				return ErrJobValueInvalid
			}
		}
		for key, item := range value {
			if err := b.use(len(key)); err != nil {
				return err
			}
			child := n.properties[key]
			if child == nil {
				if n.additionalDenied {
					return ErrJobValueInvalid
				}
				child = n.additional
			}
			if child != nil {
				if err := child.validate(item, b, depth+1); err != nil {
					return err
				}
			}
		}
	case []any:
		if !jobLength(len(value), n.minItems, n.maxItems) {
			return ErrJobValueInvalid
		}
		if n.items != nil {
			for _, item := range value {
				if err := n.items.validate(item, b, depth+1); err != nil {
					return err
				}
			}
		}
	case string:
		if err := b.use(len(value) * max(1, n.patternCost)); err != nil {
			return err
		}
		if !jobLength(utf8.RuneCountInString(value), n.minLength, n.maxLength) {
			return ErrJobValueInvalid
		}
		if n.pattern != nil && !n.pattern.MatchString(value) {
			return ErrJobValueInvalid
		}
	case json.Number:
		if err := b.use(len(value)); err != nil {
			return err
		}
		r, _ := jobNumber(value)
		if n.minimum != nil && r.Cmp(n.minimum) < 0 || n.maximum != nil && r.Cmp(n.maximum) > 0 || n.exclusiveMin != nil && r.Cmp(n.exclusiveMin) <= 0 || n.exclusiveMax != nil && r.Cmp(n.exclusiveMax) >= 0 {
			return ErrJobValueInvalid
		}
		if n.multipleOf != nil && !new(big.Rat).Quo(r, n.multipleOf).IsInt() {
			return ErrJobValueInvalid
		}
	}
	return nil
}

func jobEqual(a, c any, b *jobSchemaBudget) (bool, error) {
	if err := b.use(0); err != nil {
		return false, err
	}
	switch v := a.(type) {
	case nil:
		return c == nil, nil
	case bool:
		other, ok := c.(bool)
		return ok && other == v, nil
	case string:
		other, ok := c.(string)
		if err := b.use(len(v)); err != nil {
			return false, err
		}
		return ok && other == v, nil
	case json.Number:
		other, ok := c.(json.Number)
		if !ok {
			return false, nil
		}
		if err := b.use(len(v) + len(other)); err != nil {
			return false, err
		}
		x, _ := jobNumber(v)
		y, _ := jobNumber(other)
		return x.Cmp(y) == 0, nil
	case []any:
		other, ok := c.([]any)
		if !ok || len(v) != len(other) {
			return false, nil
		}
		for i, x := range v {
			equal, err := jobEqual(x, other[i], b)
			if err != nil || !equal {
				return equal, err
			}
		}
		return true, nil
	case map[string]any:
		other, ok := c.(map[string]any)
		if !ok || len(v) != len(other) {
			return false, nil
		}
		for key, x := range v {
			if err := b.use(len(key)); err != nil {
				return false, err
			}
			y, ok := other[key]
			if !ok {
				return false, nil
			}
			equal, err := jobEqual(x, y, b)
			if err != nil || !equal {
				return equal, err
			}
		}
		return true, nil
	}
	return false, ErrJobValueInvalid
}
