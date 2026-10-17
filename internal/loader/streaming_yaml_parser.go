package loader

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"gopkg.in/yaml.v3"
)

// StreamingYamlParser spools block-sequence monitor entries to a private temp
// file, then decodes one entry at a time after all metadata is available.
// Each entry and the metadata are limited to 1 MiB. Cross-entry aliases and
// flow-style root monitor arrays are rejected; JSON supports compact arrays.
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
	lineNumber, entryLine := 0, 0
	flushEntry := func() error {
		if entryBuffer.Len() == 0 {
			return nil
		}
		var header [16]byte
		binary.BigEndian.PutUint64(header[:8], uint64(entryLine))
		binary.BigEndian.PutUint64(header[8:], uint64(entryBuffer.Len()))
		if _, err := spool.Write(header[:]); err != nil {
			return err
		}
		_, err := spool.Write(entryBuffer.Bytes())
		entryBuffer.Reset()
		return err
	}
	writeMetadata := func(line string) {
		metadata.WriteString(line + "\n")
		metadataLines = append(metadataLines, lineNumber)
	}
	inMonitors, found, entry := false, false, false
	indent, entryBytes := -1, 0
	for scan.Scan() {
		lineNumber++
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scan.Text()
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
	var metaNode yaml.Node
	var meta struct {
		Version   int                          `yaml:"version"`
		Endpoints map[string]manifest.Endpoint `yaml:"endpoints"`
		Groups    manifest.NotificationGroups  `yaml:"notification_groups"`
	}
	if metadata.Len() > 0 {
		d := yaml.NewDecoder(&metadata)
		d.KnownFields(p.config.StrictUnknownFields)
		decodeErr := d.Decode(&metaNode)
		if decodeErr != nil && decodeErr != io.EOF {
			return yamlSyntaxError(ctx, p.filename, 0, decodeErr, func(line int) int {
				if line > 0 && line <= len(metadataLines) {
					return metadataLines[line-1]
				}
				return 0
			})
		}
		// Leading/trailing comments and blank lines are collected separately
		// from monitors. A comments-only buffer has no YAML document to decode.
		if decodeErr == nil {
			adjustYAMLLines(&metaNode, func(line int) int {
				if line > 0 && line <= len(metadataLines) {
					return metadataLines[line-1]
				}
				return line
			})
			if err := decodeYAMLValue(&metaNode, &meta, p.config.StrictUnknownFields); err != nil {
				return yamlSourceError(ctx, p.filename, 0, &metaNode, yamlMetadataError(&metaNode, err))
			}
		}
		var extra interface{}
		if err := d.Decode(&extra); err != io.EOF {
			return fmt.Errorf("manifest must contain one YAML document")
		}
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
		if size == 0 || size > 1<<20 || start == 0 || start > uint64(lineNumber) {
			return fmt.Errorf("invalid monitor spool record")
		}
		raw := make([]byte, int(size))
		if _, err := io.ReadFull(spool, raw); err != nil {
			return err
		}
		entryIndex++
		// A new parser bounds anchors to this entry and releases previous trees.
		if err := yaml.Unmarshal(raw, &monitorNode); err != nil {
			return yamlSyntaxError(ctx, p.filename, entryIndex, err, func(line int) int { return line + int(start) - 1 })
		}
		adjustYAMLLines(&monitorNode, func(line int) int { return line + int(start) - 1 })
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
