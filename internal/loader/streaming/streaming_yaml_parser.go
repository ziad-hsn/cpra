package streaming

import (
    "bufio"
    "compress/gzip"
    "context"
    "fmt"
    "io"
    "os"
    "sync"

    "cpra/internal/loader/schema"
    "gopkg.in/yaml.v3"
    "strings"
)

// StreamingYamlParser handles true streaming parsing of a YAML file.
// It reads the file document by document, creating batches without loading the entire file into memory.
type StreamingYamlParser struct {
	filename  string
	config    ParseConfig
	batchPool *sync.Pool
}

// NewStreamingYamlParser creates a new streaming YAML parser.
func NewStreamingYamlParser(filename string, config ParseConfig) (*StreamingYamlParser, error) {
	return &StreamingYamlParser{
		filename: filename,
		config:   config,
		batchPool: &sync.Pool{
			New: func() interface{} {
				s := make([]schema.Monitor, 0, config.BatchSize)
				return &s
			},
		},
	}, nil
}

// ParseBatches streams the YAML file and sends batches of monitors over a channel.
func (p *StreamingYamlParser) ParseBatches(ctx context.Context, progressChan chan<- Progress) (<-chan MonitorBatch, <-chan error) {
	batchChan := make(chan MonitorBatch, 100)
	errorChan := make(chan error, 1)

	go func() {
		defer close(batchChan)
		defer close(errorChan)

		if err := p.parseFile(ctx, batchChan, progressChan); err != nil {
			errorChan <- err
		}
	}()

	return batchChan, errorChan
}

// parseFile performs the actual streaming YAML parsing.
// It decodes the main `monitors` list and then streams each monitor entry.
func (p *StreamingYamlParser) parseFile(ctx context.Context, batchChan chan<- MonitorBatch, progressChan chan<- Progress) error {
	file, err := os.Open(p.filename)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var r io.Reader = file
	if strings.HasSuffix(strings.ToLower(p.filename), ".gz") {
		gz, gzErr := gzip.NewReader(file)
		if gzErr != nil {
			return fmt.Errorf("failed to create gzip reader: %w", gzErr)
		}
		defer gz.Close()
		r = gz
	}
	bufr := bufio.NewReaderSize(r, 64*1024)

    decoder := yaml.NewDecoder(bufr)
    decoder.KnownFields(p.config.StrictUnknownFields)

	// The YAML file is expected to have a root structure like:
	// monitors:
	//   - name: ...
	//   - name: ...
	// We need to find the 'monitors' sequence node.

	// 1. Decode the top-level structure.
	var topLevel struct {
		Monitors yaml.Node `yaml:"monitors"`
	}
	if err := decoder.Decode(&topLevel); err != nil {
		if err == io.EOF {
			return nil // Empty file is not an error
		}
		return fmt.Errorf("failed to decode top-level 'monitors' field: %w", err)
	}

	// 2. Check if 'monitors' is a sequence.
	if topLevel.Monitors.Kind != yaml.SequenceNode {
		return fmt.Errorf("'monitors' field must be a YAML sequence")
	}

	// 3. Iterate through the sequence and decode each monitor.
	batchID := 0
	batchPtr := p.batchPool.Get().(*[]schema.Monitor)
	batch := (*batchPtr)[:0]

	for _, monitorNode := range topLevel.Monitors.Content {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			var monitor schema.Monitor
			if err := monitorNode.Decode(&monitor); err != nil {
				// Provide context for the decoding error.
				return fmt.Errorf("failed to decode monitor at line %d: %w", monitorNode.Line, err)
			}

			// Basic validation to catch empty monitors from malformed YAML (e.g., "- ").
			if monitor.Name == "" && monitor.Pulse.Type == "" {
				// This is likely an empty or malformed monitor entry, so we skip it.
				continue
			}

			batch = append(batch, monitor)

			if len(batch) >= p.config.BatchSize {
				// Send the full batch.
				batchChan <- MonitorBatch{Monitors: batch, BatchID: batchID}
				// Reset batch length to reuse slice.
				batch = batch[:0]
				batchID++
			}
		}
	}

	// 4. Send any remaining monitors in the last batch.
	if len(batch) > 0 {
		batchChan <- MonitorBatch{Monitors: batch, BatchID: batchID}
	}

	// Return slice to pool
	p.batchPool.Put(batchPtr)

	return nil
}
