package streaming

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"cpra/internal/loader/schema"
)

// StreamingJsonParser handles true streaming parsing of a JSON file.
// It reads the file object by object, creating batches without loading the entire file into memory.
type StreamingJsonParser struct {
	config   ParseConfig
	filename string
}

// NewStreamingJsonParser creates a new streaming JSON parser.
func NewStreamingJsonParser(filename string, config ParseConfig) (*StreamingJsonParser, error) {
	return &StreamingJsonParser{
		filename: filename,
		config:   normalizedParseConfig(config),
	}, nil
}

// ParseBatches streams the JSON file and sends batches of monitors over a channel.
func (p *StreamingJsonParser) ParseBatches(ctx context.Context, progressChan chan<- Progress) (<-chan MonitorBatch, <-chan error) {
	batchChan := make(chan MonitorBatch, 2) // Buffer for a few batches
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

// parseFile performs the actual streaming JSON parsing using a two-pass
// approach: first it reads the top-level endpoints/notification_groups
// metadata, then it streams the monitors array. This is required because JSON
// object key order is not guaranteed (json.Marshal sorts keys alphabetically,
// so "notification_groups" typically appears AFTER "monitors"), and monitors
// referencing a notify_group must resolve against already-populated groups.
func (p *StreamingJsonParser) parseFile(ctx context.Context, batchChan chan<- MonitorBatch, _ chan<- Progress) error {
	endpoints, groups, err := p.readMetadata(ctx)
	if err != nil {
		return err
	}
	return p.streamMonitors(ctx, batchChan, endpoints, groups)
}

// openDecoder opens the file (handling gzip) and returns a JSON decoder plus a
// cleanup function.
func (p *StreamingJsonParser) openDecoder(ctx context.Context) (*json.Decoder, func(), error) {
	file, err := os.Open(p.filename)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open file: %w", err)
	}
	cleanup := func() { _ = file.Close() }

	var reader io.Reader = file
	if strings.HasSuffix(strings.ToLower(p.filename), ".gz") {
		gz, gzErr := gzip.NewReader(file)
		if gzErr != nil {
			cleanup()
			return nil, nil, fmt.Errorf("failed to create gzip reader: %w", gzErr)
		}
		cleanup = func() { _ = gz.Close(); _ = file.Close() }
		reader = gz
	}
	bufr := bufio.NewReaderSize(&boundedReader{ctx: ctx, reader: reader, remaining: p.config.MaxMemory}, 64*1024)
	decoder := json.NewDecoder(bufr)
	if p.config.StrictUnknownFields {
		decoder.DisallowUnknownFields()
	}
	if p.config.JSONUseNumber {
		decoder.UseNumber()
	}
	return decoder, cleanup, nil
}

// readMetadata reads the top-level endpoints and notification_groups keys,
// skipping the monitors array (which is streamed in a second pass).
func (p *StreamingJsonParser) readMetadata(ctx context.Context) (map[string]schema.Endpoint, schema.NotificationGroups, error) {
	decoder, cleanup, err := p.openDecoder(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()

	t, err := decoder.Token()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read opening token: %w", err)
	}
	if t != json.Delim('{') {
		return nil, nil, fmt.Errorf("expected { at start of json file, got %v", t)
	}

	var endpoints map[string]schema.Endpoint
	var groups schema.NotificationGroups
	seen := map[string]bool{}

	for decoder.More() {
		t, err := decoder.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read token: %w", err)
		}
		key, ok := t.(string)
		if !ok {
			return nil, nil, fmt.Errorf("expected string key at top level, got %v", t)
		}
		if seen[key] {
			return nil, nil, fmt.Errorf("duplicate JSON field %q", key)
		}
		seen[key] = true
		if p.config.StrictUnknownFields && key != "monitors" && key != "version" && key != "endpoints" && key != "notification_groups" {
			return nil, nil, fmt.Errorf("unknown manifest field %q", key)
		}
		switch key {
		case "endpoints":
			if err := decodeJSONValue(decoder, &endpoints, p.config.StrictUnknownFields); err != nil {
				return nil, nil, fmt.Errorf("failed to decode endpoints: %w", err)
			}
		case "notification_groups":
			if err := decodeJSONValue(decoder, &groups, p.config.StrictUnknownFields); err != nil {
				return nil, nil, fmt.Errorf("failed to decode notification_groups: %w", err)
			}
		default:
			// Skip unknown keys (including "monitors" and "version").
			if err := skipJSONValue(ctx, decoder); err != nil {
				return nil, nil, fmt.Errorf("failed to skip top-level key %q: %w", key, err)
			}
		}
	}
	if !seen["monitors"] {
		return nil, nil, fmt.Errorf("manifest requires monitors")
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, nil, fmt.Errorf("unexpected trailing JSON data")
	}
	return endpoints, groups, nil
}

// streamMonitors streams the monitors array, attaching the pre-read metadata.
func (p *StreamingJsonParser) streamMonitors(ctx context.Context, batchChan chan<- MonitorBatch, endpoints map[string]schema.Endpoint, groups schema.NotificationGroups) error {
	decoder, cleanup, err := p.openDecoder(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	t, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("failed to read opening token: %w", err)
	}
	if t != json.Delim('{') {
		return fmt.Errorf("expected { at start of json file, got %v", t)
	}

	for decoder.More() {
		t, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("failed to read token: %w", err)
		}
		key, ok := t.(string)
		if !ok {
			return fmt.Errorf("expected string key at top level, got %v", t)
		}
		if key != "monitors" {
			if err := skipJSONValue(ctx, decoder); err != nil {
				return fmt.Errorf("failed to skip top-level key %q: %w", key, err)
			}
			continue
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
		for decoder.More() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				batch := make([]schema.Monitor, 0, p.config.BatchSize)
				for i := 0; i < p.config.BatchSize && decoder.More(); i++ {
					var monitor schema.Monitor
					if err := decodeJSONValue(decoder, &monitor, p.config.StrictUnknownFields); err != nil {
						off := decoder.InputOffset()
						return fmt.Errorf("failed to decode monitor object at byte %d: %w", off, err)
					}
					if err := schema.ValidateMonitor(&monitor); err != nil {
						off := decoder.InputOffset()
						return fmt.Errorf("invalid monitor at byte %d: %w", off, err)
					}
					batch = append(batch, monitor)
				}
				if len(batch) > 0 {
					select {
					case batchChan <- MonitorBatch{
						Monitors:           batch,
						BatchID:            batchID,
						Endpoints:          endpoints,
						NotificationGroups: groups,
					}:
					case <-ctx.Done():
						return ctx.Err()
					}
					batchID++
				}
			}
		}

		// Read the closing bracket of the array.
		t, err = decoder.Token()
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("failed to read closing token: %w", err)
		}
		if t != json.Delim(']') {
			return fmt.Errorf("expected ] at end of json file, got %v", t)
		}
	}

	if _, err := decoder.Token(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON data")
	}
	return nil
}
