package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"cpra/internal/loader/schema"
)

// StreamingJsonParser handles true streaming parsing of a JSON file.
// It reads the file object by object, creating batches without loading the entire file into memory.
type StreamingJsonParser struct {
	filename string
	config   ParseConfig
}

// NewStreamingJsonParser creates a new streaming JSON parser.
func NewStreamingJsonParser(filename string, config ParseConfig) (*StreamingJsonParser, error) {
	return &StreamingJsonParser{
		filename: filename,
		config:   config,
	}, nil
}

// ParseBatches streams the JSON file and sends batches of monitors over a channel.
func (p *StreamingJsonParser) ParseBatches(ctx context.Context, progressChan chan<- Progress) (<-chan MonitorBatch, <-chan error) {
	batchChan := make(chan MonitorBatch, 100) // Buffer for a few batches
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

// parseFile performs the actual streaming JSON parsing.
func (p *StreamingJsonParser) parseFile(ctx context.Context, batchChan chan<- MonitorBatch, progressChan chan<- Progress) error {
	file, err := os.Open(p.filename)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)

	// Read the opening bracket of the object.
	t, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("failed to read opening token: %w", err)
	}
	if t != json.Delim('{') {
		return fmt.Errorf("expected { at start of json file, got %v", t)
	}

	// Find the "monitors" key
	for decoder.More() {
		t, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("failed to read token: %w", err)
		}
		if s, ok := t.(string); ok && s == "monitors" {
			break
		}
	}

	// Read the opening bracket of the array.
	t, err = decoder.Token()
	if err != nil {
		return fmt.Errorf("failed to read opening token: %w", err)
	}
	if t != json.Delim('[') {
		return fmt.Errorf("expected [ after 'monitors' key, got %v", t)
	}

	batchID := 0
	// Loop while there are more objects in the array.
	for decoder.More() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			batch := make([]schema.Monitor, 0, p.config.BatchSize)
			// Fill a batch
			for i := 0; i < p.config.BatchSize && decoder.More(); i++ {
				var monitor schema.Monitor
				if err := decoder.Decode(&monitor); err != nil {
					return fmt.Errorf("failed to decode monitor object: %w", err)
				}
				batch = append(batch, monitor)
			}

			// Send the batch to the creator
			batchChan <- MonitorBatch{
				Monitors: batch,
				BatchID:  batchID,
			}
			batchID++
		}
	}

	// Read the closing bracket of the array.
	t, err = decoder.Token()
	if err != nil && err != io.EOF {
		return fmt.Errorf("failed to read closing token: %w", err)
	}
	if t != json.Delim(']') {
		return fmt.Errorf("expected ] at end of json file, got %v", t)
	}

	return nil
}
