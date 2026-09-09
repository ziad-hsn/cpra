package streaming

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"cpra/internal/loader/schema"
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
	var metadata bytes.Buffer
	inMonitors, found, entry := false, false, false
	indent, entryBytes := -1, 0
	for scan.Scan() {
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
				if _, err := io.WriteString(spool, line+"\n"); err != nil {
					return err
				}
			} else {
				if metadata.Len()+len(line) > 1<<20 {
					return fmt.Errorf("manifest metadata exceeds 1 MiB")
				}
				metadata.WriteString(line + "\n")
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
			metadata.WriteString(line + "\n")
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
			if _, err := io.WriteString(spool, "---\n"); err != nil {
				return err
			}
			entry = true
			entryBytes = 0
		}
		if !entry {
			return fmt.Errorf("invalid monitor entry")
		}
		entryBytes += len(line) + 1
		if entryBytes > 1<<20 {
			return fmt.Errorf("one monitor exceeds 1 MiB")
		}
		if _, err := io.WriteString(spool, line+"\n"); err != nil {
			return err
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("manifest requires a monitors block sequence")
	}
	var meta struct {
		Version   int                        `yaml:"version"`
		Endpoints map[string]schema.Endpoint `yaml:"endpoints"`
		Groups    schema.NotificationGroups  `yaml:"notification_groups"`
	}
	if metadata.Len() > 0 {
		d := yaml.NewDecoder(&metadata)
		d.KnownFields(p.config.StrictUnknownFields)
		var metaNode yaml.Node
		if err := d.Decode(&metaNode); err != nil {
			return fmt.Errorf("invalid manifest metadata: %w", err)
		}
		if err := decodeYAMLValue(&metaNode, &meta, p.config.StrictUnknownFields); err != nil {
			return fmt.Errorf("invalid manifest metadata: %w", err)
		}
		var extra interface{}
		if err := d.Decode(&extra); err != io.EOF {
			return fmt.Errorf("manifest must contain one YAML document")
		}
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return err
	}
	decoder := yaml.NewDecoder(spool)
	decoder.KnownFields(p.config.StrictUnknownFields)
	batch := make([]schema.Monitor, 0, p.config.BatchSize)
	batchID := 0
	send := func() error {
		if len(batch) == 0 {
			return nil
		}
		select {
		case out <- MonitorBatch{Monitors: batch, BatchID: batchID, Endpoints: meta.Endpoints, NotificationGroups: meta.Groups}:
			batch = make([]schema.Monitor, 0, p.config.BatchSize)
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
		var monitors []schema.Monitor
		var monitorNode yaml.Node
		if err := decoder.Decode(&monitorNode); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("invalid monitor (aliases must stay within an entry): %w", err)
		}
		if err := decodeYAMLValue(&monitorNode, &monitors, p.config.StrictUnknownFields); err != nil {
			return fmt.Errorf("invalid monitor: %w", err)
		}
		if len(monitors) != 1 {
			return fmt.Errorf("expected one monitor per block entry")
		}
		if err := schema.ValidateMonitor(&monitors[0]); err != nil {
			return err
		}
		batch = append(batch, monitors[0])
		if len(batch) >= p.config.BatchSize {
			if err := send(); err != nil {
				return err
			}
		}
	}
	return send()
}
