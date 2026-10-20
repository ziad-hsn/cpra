package collection

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

// YAMLAnchors decodes fragments of one YAML document in source order. It retains
// only owned anchor values, bounded to 1 MiB and 65,536 expanded nodes. The zero
// value is ready for use. Start a new value at each document boundary. It is not
// concurrent-safe and does not validate CPRa resources or activate configuration.
// Returned trees own their expanded content and preserve original coordinates.
type YAMLAnchors struct {
	values              map[string]yamlAnchor
	nodes, bytes        int
	allowUnusedOverflow bool
	saturated           bool
	external            bool
}

type yamlAnchor struct {
	node         *yaml.Node
	nodes, bytes int
}

// Parse decodes one complete mapping or sequence fragment. line maps each
// fragment line to its one-based original source line. No files are opened.
// An error contains a safe cause and original coordinates, never source values.
func (a *YAMLAnchors) Parse(data []byte, line func(int) int) (*yaml.Node, error) {
	a.external = false
	if line == nil {
		line = func(n int) int { return n }
	}
	root, err := parseYAMLFragment(data)
	prefix := 0
	placeholders := map[*yaml.Node]string{}
	if err != nil && strings.HasPrefix(err.Error(), "yaml: unknown anchor '") {
		if a.saturated {
			return nil, yamlLimitError(line(1), 0)
		}
		// The scanner permits these characters in names. False positives in
		// comments/quoted strings only create unused null placeholders.
		names := yamlAliasNames(data)
		if len(names) > 65536 {
			return nil, yamlLimitError(0, 0)
		}
		var input bytes.Buffer
		input.WriteString("anchors:\n")
		for _, name := range names {
			fmt.Fprintf(&input, "  - &%s null\n", name)
		}
		input.WriteString("value:\n")
		prefix = len(names) + 2
		started, ended := false, false
		for _, row := range bytes.Split(data, []byte{'\n'}) {
			trim := strings.TrimSpace(string(row))
			marker := len(row) > 0 && row[0] != ' ' && (trim == "---" || strings.HasPrefix(trim, "--- #") || trim == "...")
			if marker {
				if trim != "..." && started || ended {
					return nil, &SourceError{Cause: api.InvalidField("", "invalidYAML")}
				}
				if trim == "..." {
					ended = true
				}
				started = true
				row = nil
			} else if trim != "" && !strings.HasPrefix(trim, "#") {
				if ended {
					return nil, &SourceError{Cause: api.InvalidField("", "invalidYAML")}
				}
				started = true
			}
			input.WriteString("  ")
			input.Write(row)
			input.WriteByte('\n')
		}
		a.external = true
		root, err = parseYAMLFragment(input.Bytes())
		if err == nil {
			if len(root.Content) != 1 || len(root.Content[0].Content) != 4 {
				return nil, yamlLimitError(0, 0)
			}
			for n, node := range root.Content[0].Content[1].Content {
				placeholders[node] = names[n]
			}
			root = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root.Content[0].Content[3]}}
		}
	}
	if err != nil {
		return nil, parseSourceError(yamlSyntaxError(err), Location{Line: line(1)}, func(n int) int { return line(max(n-prefix, 1)) })
	}
	var anchors []*yaml.Node
	var rebind func(*yaml.Node, int) error
	rebind = func(n *yaml.Node, depth int) error {
		// This walk only follows syntax children, not alias targets. Leave the
		// original value decoder in charge of its 64-level semantic limit;
		// document wrappers and mapping keys also occur in this traversal.
		if depth > 128 {
			return yamlLimitError(line(max(n.Line-prefix, 1)), n.Column)
		}
		if n.Line > 0 {
			n.Line = line(n.Line - prefix)
			if prefix > 0 {
				n.Column = max(1, n.Column-2)
			}
		}
		if name, ok := placeholders[n.Alias]; n.Kind == yaml.AliasNode && ok {
			v, exists := a.values[name]
			if !exists {
				return &SourceError{Location: Location{Line: n.Line, Column: n.Column}, Cause: api.InvalidField("", "unknownYAMLAlias")}
			}
			n.Alias = v.node
		}
		if n.Anchor != "" {
			anchors = append(anchors, n)
		}
		for _, child := range n.Content {
			if err := rebind(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := rebind(root, -1); err != nil {
		return nil, err
	}
	expanded := root
	if yamlExpansion(root, 0) && (!a.allowUnusedOverflow || prefix > 0) {
		budget := yamlNodeBudget{nodes: 65536, bytes: 1 << 20, active: map[*yaml.Node]bool{}}
		expanded, err = budget.copy(root, 0)
		if err != nil {
			return nil, err
		}
	}
	// Publish only after every expansion and quota check succeeds. Definitions
	// are visited in serialization order; repeated names replace earlier ones.
	if a.saturated {
		return expanded, nil
	}
	updates := make(map[string]yamlAnchor)
	overflow := func(err error) (*yaml.Node, error) {
		if !a.allowUnusedOverflow {
			return nil, err
		}
		// Existing unprofiled inputs permitted unlimited unused definitions
		// within their raw metadata quota. Keep their unchanged first-parse
		// behavior; later cross-entry lookups fail with an explicit quota error.
		a.values = nil
		a.nodes, a.bytes = 0, 0
		a.saturated = true
		return expanded, nil
	}
	nodes, size := a.nodes, a.bytes
	for _, node := range anchors {
		copyBudget := yamlNodeBudget{nodes: 65536, bytes: 1 << 20, active: map[*yaml.Node]bool{}}
		frozen, err := copyBudget.copy(node, 0)
		if err != nil {
			return overflow(err)
		}
		old, ok := updates[node.Anchor]
		if !ok {
			old = a.values[node.Anchor]
		}
		next := yamlAnchor{node: frozen, nodes: 65536 - copyBudget.nodes, bytes: (1 << 20) - copyBudget.bytes + len(node.Anchor)}
		nodes += next.nodes - old.nodes
		size += next.bytes - old.bytes
		if nodes > 65536 || size > 1<<20 {
			return overflow(yamlLimitError(node.Line, node.Column))
		}
		updates[node.Anchor] = next
	}
	if a.values == nil {
		a.values = make(map[string]yamlAnchor)
	}
	for name, value := range updates {
		a.values[name] = value
	}
	a.nodes, a.bytes = nodes, size
	return expanded, nil
}

func parseYAMLFragment(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		if err == io.EOF {
			return &root, nil
		}
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("one YAML document required")
	}
	return &root, nil
}

func yamlAliasNames(data []byte) []string {
	names := make(map[string]bool)
	for n := 0; n < len(data); n++ {
		if data[n] != '*' {
			continue
		}
		end := n + 1
		for end < len(data) {
			c := data[end]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				break
			}
			end++
		}
		if end > n+1 {
			names[string(data[n+1:end])] = true
			if len(names) > 65536 {
				break
			}
		}
		n = end - 1
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

type yamlNodeBudget struct {
	nodes, bytes int
	active       map[*yaml.Node]bool
}

func (b *yamlNodeBudget) copy(n *yaml.Node, depth int) (*yaml.Node, error) {
	if n == nil || depth > 64 || b.active[n] {
		return nil, yamlLimitError(0, 0)
	}
	b.nodes--
	b.bytes -= len(n.Tag) + len(n.Value) + len(n.Anchor)
	if b.nodes < 0 || b.bytes < 0 {
		return nil, yamlLimitError(n.Line, n.Column)
	}
	b.active[n] = true
	defer delete(b.active, n)
	if n.Kind == yaml.AliasNode {
		value, err := b.copy(n.Alias, depth+1)
		if value != nil {
			value.Line, value.Column = n.Line, n.Column
		}
		return value, err
	}
	out := &yaml.Node{Kind: n.Kind, Style: n.Style, Tag: n.Tag, Value: n.Value, Line: n.Line, Column: n.Column}
	for _, child := range n.Content {
		c, err := b.copy(child, depth+1)
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, c)
	}
	return out, nil
}
func yamlLimitError(line, column int) error {
	return &SourceError{Location: Location{Line: line, Column: column}, Cause: api.InvalidField("", "yamlExpansionLimit")}
}

// ExternalAliases reports whether the last successful Parse required definitions
// from an earlier fragment. Callers can retain the original raw fragment when
// false; true requires preserving the resolved tree and its coordinates.
func (a *YAMLAnchors) ExternalAliases() bool { return a.external }
