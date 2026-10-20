package loader

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"gopkg.in/yaml.v3"
)

// StreamingYamlParser spools block-sequence monitor entries to a private temp
// file, then decodes one entry at a time after all metadata is available.
// Raw entries and metadata are limited to 1 MiB. Same-document anchors are
// bounded and resolved before spooling; JSON supports compact root arrays.
type StreamingYamlParser struct {
	config   ParseConfig
	filename string
}

func NewStreamingYamlParser(filename string, config ParseConfig) (*StreamingYamlParser, error) {
	return &StreamingYamlParser{normalizedParseConfig(config), filename}, nil
}
func (p *StreamingYamlParser) ParseBatches(ctx context.Context, progress chan<- Progress) (<-chan MonitorBatch, <-chan error) {
	batches := make(chan MonitorBatch, 2)
	errs := make(chan error, 1)
	go func() {
		defer close(batches)
		defer close(errs)
		if err := p.parseFile(ctx, batches, progress); err != nil {
			errs <- err
		}
	}()
	return batches, errs
}

var monitorsHeader = regexp.MustCompile(`^(monitors|'monitors'|"monitors"):\s*(#.*)?$`)

func (p *StreamingYamlParser) parseFile(ctx context.Context, out chan<- MonitorBatch, _ chan<- Progress) error {
	file, err := os.Open(p.filename)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	var input io.Reader = file
	if strings.HasSuffix(strings.ToLower(p.filename), ".gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		input = gz
	}
	input = &boundedReader{ctx: ctx, reader: input, remaining: p.config.MaxMemory}
	spool, err := os.CreateTemp("", "cpra-monitors-*.yaml")
	if err != nil {
		return err
	}
	defer func() { _ = spool.Close(); _ = os.Remove(spool.Name()) }()
	scan := bufio.NewScanner(input)
	scan.Buffer(make([]byte, 64<<10), 1<<20)
	var metadata, entryBuffer bytes.Buffer
	var metadataLines []int
	lineNumber, entryLine, syntaxItem := 0, 0, 0
	var anchors collection.YAMLAnchors
	var spoolBytes int64
	metaNode := yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	metadataOffset, metadataLineOffset := 0, 0
	metadataKeys := make(map[string]bool)
	syntaxError := func(err error, item int) error {
		var source *collection.SourceError
		if errors.As(err, &source) {
			if source.Location.Column == 0 && source.Location.Line > 0 {
				return (MonitorSource{Item: item, Offset: -1}).Error(ctx, p.filename, fmt.Errorf("near line %d (parser context; column unavailable): %w", source.Location.Line, source.Cause))
			}
			return (MonitorSource{Item: item, Line: source.Location.Line, Column: source.Location.Column, Offset: -1}).Error(ctx, p.filename, source.Cause)
		}
		return err
	}
	flushMetadata := func() error {
		if metadata.Len() == metadataOffset {
			return nil
		}
		root, err := anchors.Parse(metadata.Bytes()[metadataOffset:], func(n int) int {
			if n > 0 && n+metadataLineOffset <= len(metadataLines) {
				return metadataLines[n+metadataLineOffset-1]
			}
			return 0
		})
		if err != nil {
			return syntaxError(err, 0)
		}
		if len(root.Content) > 0 {
			if root.Content[0].Kind != yaml.MappingNode {
				return fmt.Errorf("manifest metadata must be a mapping")
			}
			for n := 0; n+1 < len(root.Content[0].Content); n += 2 {
				key := root.Content[0].Content[n]
				if metadataKeys[key.Value] {
					return yamlSourceError(ctx, p.filename, 0, key, api.InvalidField("", "duplicateField"))
				}
				metadataKeys[key.Value] = true
				if key.Value != "version" && key.Value != "endpoints" && key.Value != "notification_groups" && key.Tag != "!!merge" {
					// Non-strict mode retains its old acceptance of unknown fields,
					// but their syntax is not retained after alias registration.
					if p.config.StrictUnknownFields {
						return yamlSourceError(ctx, p.filename, 0, key, api.InvalidField("", "unknownField"))
					}
					continue
				}
				metaNode.Content[0].Content = append(metaNode.Content[0].Content, root.Content[0].Content[n:n+2]...)
			}
			// Sections are bounded individually and cannot occur twice.

		}
		metadataOffset, metadataLineOffset = metadata.Len(), len(metadataLines)
		return nil
	}
	flushEntry := func() error {
		if entryBuffer.Len() == 0 {
			return nil
		}
		if err := flushMetadata(); err != nil {
			return err
		}
		syntaxItem++
		root, err := anchors.Parse(entryBuffer.Bytes(), func(n int) int { return n + entryLine - 1 })
		if err != nil {
			return syntaxError(err, syntaxItem)
		}
		raw := entryBuffer.Bytes()
		start := uint64(entryLine)
		if anchors.ExternalAliases() {
			raw, err = json.Marshal(root)
			if err != nil {
				return fmt.Errorf("monitor spool encoding failed")
			}
			spoolBytes += int64(len(raw)) + 16
			if len(raw) > 16<<20 || spoolBytes > p.config.MaxExpandedBytes {
				return fmt.Errorf("expanded monitor spool exceeds limit")
			}
			start |= uint64(1) << 63
		}
		var header [16]byte
		binary.BigEndian.PutUint64(header[:8], start)
		binary.BigEndian.PutUint64(header[8:], uint64(len(raw)))
		if _, err := spool.Write(header[:]); err != nil {
			return err
		}
		_, err = spool.Write(raw)
		entryBuffer.Reset()
		return err
	}
	writeMetadata := func(line string) {
		metadata.WriteString(line + "\n")
		metadataLines = append(metadataLines, lineNumber)
	}
	inMonitors, found, entry := false, false, false
	documentStarted, documentEnded := false, false
	indent, entryBytes := -1, 0
	for scan.Scan() {
		lineNumber++
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scan.Text()
		if lineNumber == 1 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			if inMonitors && entry {
				entryBytes += len(line) + 1
				if entryBytes > 1<<20 {
					return fmt.Errorf("one monitor exceeds 1 MiB")
				}
				if _, err := entryBuffer.WriteString(line + "\n"); err != nil {
					return err
				}
			} else {
				if metadata.Len()+len(line) > 1<<20 {
					return fmt.Errorf("manifest metadata exceeds 1 MiB")
				}
				writeMetadata(line)
			}
			continue
		}
		if strings.HasPrefix(line, "---") && (trim == "---" || strings.HasPrefix(trim, "--- #")) {
			if documentStarted {
				return fmt.Errorf("manifest must contain one YAML document")
			}
			documentStarted = true
			continue
		}
		if line == "..." {
			if err := flushEntry(); err != nil {
				return err
			}
			documentEnded = true
			inMonitors = false
			continue
		}
		if documentEnded {
			return fmt.Errorf("manifest must contain one YAML document")
		}
		documentStarted = true
		leading := len(line) - len(strings.TrimLeft(line, " "))
		if monitorsHeader.MatchString(line) || (leading == 0 && trim == "monitors: []") {
			if found {
				return fmt.Errorf("duplicate monitors field")
			}
			found = true
			inMonitors = trim != "monitors: []"
			continue
		}
		if inMonitors && leading == 0 && !strings.HasPrefix(line, "- ") && line != "-" {
			if err := flushEntry(); err != nil {
				return err
			}
			inMonitors = false
		}
		if !inMonitors {
			if strings.HasPrefix(trim, "monitors:") || strings.HasPrefix(trim, "\"monitors\":") {
				return fmt.Errorf("YAML monitors require a block sequence; use JSON for flow arrays")
			}
			if metadata.Len()+len(line) > 1<<20 {
				return fmt.Errorf("manifest metadata exceeds 1 MiB")
			}
			writeMetadata(line)
			continue
		}
		isItem := strings.HasPrefix(trim, "- ") || trim == "-"
		if indent < 0 {
			if !isItem {
				return fmt.Errorf("monitors must contain a block sequence")
			}
			indent = leading
		}
		if leading < indent {
			return fmt.Errorf("invalid monitor indentation")
		}
		if isItem && leading == indent {
			if err := flushEntry(); err != nil {
				return err
			}
			entry = true
			entryBytes = 0
			entryLine = lineNumber
		}
		if !entry {
			return fmt.Errorf("invalid monitor entry")
		}
		entryBytes += len(line) + 1
		if entryBytes > 1<<20 {
			return fmt.Errorf("one monitor exceeds 1 MiB")
		}
		if _, err := entryBuffer.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	if err := flushEntry(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("manifest requires a monitors block sequence")
	}
	if err := flushMetadata(); err != nil {
		return err
	}
	var meta struct {
		Version   int                          `yaml:"version"`
		Endpoints map[string]manifest.Endpoint `yaml:"endpoints"`
		Groups    manifest.NotificationGroups  `yaml:"notification_groups"`
	}
	if err := decodeYAMLValue(&metaNode, &meta, p.config.StrictUnknownFields); err != nil {
		return yamlSourceError(ctx, p.filename, 0, &metaNode, yamlMetadataError(&metaNode, err))
	}
	if err := manifest.Validate(&manifest.Manifest{Endpoints: meta.Endpoints, NotificationGroups: meta.Groups}); err != nil {
		return yamlSourceError(ctx, p.filename, 0, &metaNode, err)
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return err
	}
	entryIndex := 0
	batch := make([]manifest.Monitor, 0, p.config.BatchSize)
	batchID := 0
	sources := make([]MonitorSource, 0, p.config.BatchSize)
	send := func() error {
		if len(batch) == 0 {
			return nil
		}
		select {
		case out <- MonitorBatch{Monitors: batch, Sources: sources, BatchID: batchID, Endpoints: meta.Endpoints, NotificationGroups: meta.Groups}:
			batch = make([]manifest.Monitor, 0, p.config.BatchSize)
			sources = make([]MonitorSource, 0, p.config.BatchSize)
			batchID++
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var monitors []manifest.Monitor
		var monitorNode yaml.Node
		var header [16]byte
		if _, err := io.ReadFull(spool, header[:]); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		start, size := binary.BigEndian.Uint64(header[:8]), binary.BigEndian.Uint64(header[8:])
		expanded := start&(uint64(1)<<63) != 0
		start &^= uint64(1) << 63
		if size == 0 || size > 16<<20 || start == 0 || start > uint64(lineNumber) {
			return fmt.Errorf("invalid monitor spool record")
		}
		raw := make([]byte, int(size))
		if _, err := io.ReadFull(spool, raw); err != nil {
			return err
		}
		entryIndex++
		if expanded {
			if err := json.Unmarshal(raw, &monitorNode); err != nil {
				return fmt.Errorf("invalid monitor spool syntax")
			}
		} else {
			if err := yaml.Unmarshal(raw, &monitorNode); err != nil {
				return fmt.Errorf("invalid monitor spool syntax")
			}
			adjustYAMLLines(&monitorNode, func(n int) int { return n + int(start) - 1 })
		}
		if err := decodeYAMLValue(&monitorNode, &monitors, p.config.StrictUnknownFields); err != nil {
			node := &monitorNode
			if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
				node = node.Content[0]
			}
			if node.Kind == yaml.SequenceNode && len(node.Content) > 0 {
				node = node.Content[0]
			}
			return yamlSourceError(ctx, p.filename, entryIndex, node, err)
		}
		if len(monitors) != 1 {
			return fmt.Errorf("expected one monitor per block entry")
		}
		if err := manifest.ValidateMonitor(&monitors[0]); err != nil {
			return yamlSourceError(ctx, p.filename, entryIndex, monitorNode.Content[0].Content[0], err)
		}
		if err := manifest.ValidateMonitorReferences(&monitors[0], meta.Endpoints, meta.Groups); err != nil {
			return yamlSourceError(ctx, p.filename, entryIndex, monitorNode.Content[0].Content[0], err)
		}
		point := monitorNode.Content[0].Content[0]
		sources = append(sources, MonitorSource{Item: entryIndex, Line: point.Line, Column: point.Column, Offset: -1})
		batch = append(batch, monitors[0])
		if len(batch) >= p.config.BatchSize {
			if err := send(); err != nil {
				return err
			}
		}
	}
	return send()
}

// Content owns the syntax tree; alias pointers refer back into it and must not
// be traversed again (or an alias cycle could recurse indefinitely).
func adjustYAMLLines(node *yaml.Node, line func(int) int) {
	if node.Line > 0 {
		node.Line = line(node.Line)
	}
	for _, child := range node.Content {
		adjustYAMLLines(child, line)
	}
}
