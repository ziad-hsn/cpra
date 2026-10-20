package loader

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/sdk/go/api"
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
func (p *StreamingJsonParser) openDecoder(ctx context.Context) (*jsonDecoder, func(), error) {
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
	if prefix, _ := bufr.Peek(3); string(prefix) == "\ufeff" {
		_, _ = bufr.Discard(3)
	}
	decoder := newJSONDecoder(bufr)
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
func (p *StreamingJsonParser) readMetadata(ctx context.Context) (map[string]manifest.Endpoint, manifest.NotificationGroups, error) {
	decoder, cleanup, err := p.openDecoder(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()

	t, err := decoder.Token()
	if err != nil {
		return nil, nil, jsonSourceError(ctx, p.filename, 0, nil, decoder.InputOffset(), jsonDecoderError(decoder, err, ""))
	}
	if t != json.Delim('{') {
		return nil, nil, jsonSourceError(ctx, p.filename, 0, nil, decoder.InputOffset(), api.InvalidField("", "objectRequired"))
	}

	var endpoints map[string]manifest.Endpoint
	var groups manifest.NotificationGroups
	seen := map[string]bool{}
	type span struct {
		raw []byte
		end int64
	}
	spans := map[string]span{}

	for decoder.More() {
		t, err := decoder.Token()
		if err != nil {
			return nil, nil, jsonSourceError(ctx, p.filename, 0, nil, decoder.InputOffset(), jsonDecoderError(decoder, err, ""))
		}
		key, ok := t.(string)
		if !ok {
			return nil, nil, api.InvalidField("", "invalidType")
		}
		if seen[key] {
			return nil, nil, api.InvalidField("", "duplicateField")
		}
		seen[key] = true
		if p.config.StrictUnknownFields && key != "monitors" && key != "version" && key != "endpoints" && key != "notification_groups" {
			return nil, nil, api.InvalidField("", "unknownField")
		}
		switch key {
		case "endpoints":
			raw, err := decodeJSONResource(decoder, &endpoints, p.config.StrictUnknownFields)
			spans[key] = span{raw, decoder.InputOffset()}
			if err != nil {
				if detail, ok := api.ValidationFieldError(err); ok && (detail.Reason == "invalidType" || detail.Reason == "unsupportedDriver" || detail.Reason == "positiveDuration") && len(raw) <= 1<<20 {
					var fields map[string]json.RawMessage
					if json.Unmarshal(raw, &fields) == nil {
						for index, name := range slices.Sorted(maps.Keys(fields)) {
							var endpoint manifest.Endpoint
							if cause := api.StrictDecode(fields[name], &endpoint); cause != nil {
								parts := append([]string{strconv.Itoa(index)}, diagnosticPath(cause)...)
								cause = api.PrefixValidationError(fmt.Sprintf("endpoints[%d]", index), cause)
								return nil, nil, jsonFieldSourceError(ctx, p.filename, 0, raw, decoder.InputOffset(), parts, cause)
							}
						}
					}
				}
				return nil, nil, jsonSourceError(ctx, p.filename, 0, raw, decoder.InputOffset(), err)
			}
		case "notification_groups":
			raw, err := decodeJSONResource(decoder, &groups, p.config.StrictUnknownFields)
			spans[key] = span{raw, decoder.InputOffset()}
			if err != nil {
				return nil, nil, jsonSourceError(ctx, p.filename, 0, raw, decoder.InputOffset(), err)
			}
		case "monitors":
			token, err := decoder.Token()
			if err != nil {
				return nil, nil, jsonSourceError(ctx, p.filename, 0, nil, decoder.InputOffset(), jsonDecoderError(decoder, err, "monitors"))
			}
			if token != json.Delim('[') {
				return nil, nil, jsonSourceError(ctx, p.filename, 0, nil, decoder.InputOffset(), api.InvalidField("monitors", "invalidType"))
			}
			for item := 1; decoder.More(); item++ {
				if err := skipJSONValue(ctx, decoder.Decoder); err != nil {
					return nil, nil, jsonSourceError(ctx, p.filename, item, nil, decoder.InputOffset(), jsonDecoderError(decoder, err, ""))
				}
			}
			if _, err := decoder.Token(); err != nil {
				return nil, nil, jsonSourceError(ctx, p.filename, 0, nil, decoder.InputOffset(), jsonDecoderError(decoder, err, "monitors"))
			}
		case "version":
			var version int
			raw, err := decodeJSONResource(decoder, &version, p.config.StrictUnknownFields)
			if err != nil {
				return nil, nil, jsonSourceError(ctx, p.filename, 0, raw, decoder.InputOffset(), err)
			}
		default:
			// Skip unknown keys (including "monitors" and "version").
			if err := skipJSONValue(ctx, decoder.Decoder); err != nil {
				return nil, nil, jsonSourceError(ctx, p.filename, 0, nil, decoder.InputOffset(), jsonDecoderError(decoder, err, ""))
			}
		}
	}
	if !seen["monitors"] {
		return nil, nil, fmt.Errorf("manifest requires monitors")
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, jsonSourceError(ctx, p.filename, 0, nil, decoder.InputOffset(), jsonDecoderError(decoder, err, ""))
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, nil, fmt.Errorf("unexpected trailing JSON data")
	}
	if err := manifest.Validate(&manifest.Manifest{Endpoints: endpoints, NotificationGroups: groups}); err != nil {
		parts := diagnosticPath(err)
		if len(parts) > 0 {
			if source, ok := spans[parts[0]]; ok {
				return nil, nil, jsonFieldSourceError(ctx, p.filename, 0, source.raw, source.end, parts[1:], err)
			}
		}
		return nil, nil, (MonitorSource{Offset: -1}).Error(ctx, p.filename, err)
	}
	return endpoints, groups, nil
}

// streamMonitors streams the monitors array, attaching the pre-read metadata.
func (p *StreamingJsonParser) streamMonitors(ctx context.Context, batchChan chan<- MonitorBatch, endpoints map[string]manifest.Endpoint, groups manifest.NotificationGroups) error {
	decoder, cleanup, err := p.openDecoder(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	t, err := decoder.Token()
	if err != nil {
		return jsonDecoderError(decoder, err, "")
	}
	if t != json.Delim('{') {
		return api.InvalidField("", "objectRequired")
	}

	for decoder.More() {
		t, err := decoder.Token()
		if err != nil {
			return jsonDecoderError(decoder, err, "")
		}
		key, ok := t.(string)
		if !ok {
			return api.InvalidField("", "invalidType")
		}
		if key != "monitors" {
			if err := skipJSONValue(ctx, decoder.Decoder); err != nil {
				return jsonDecoderError(decoder, err, "")
			}
			continue
		}

		// Read the opening bracket of the array.
		t, err = decoder.Token()
		if err != nil {
			return jsonDecoderError(decoder, err, "")
		}
		if t != json.Delim('[') {
			return api.InvalidField("monitors", "invalidType")
		}

		batchID := 0
		for decoder.More() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				batch := make([]manifest.Monitor, 0, p.config.BatchSize)
				sources := make([]MonitorSource, 0, p.config.BatchSize)
				for i := 0; i < p.config.BatchSize && decoder.More(); i++ {
					var monitor manifest.Monitor
					item := batchID*p.config.BatchSize + i + 1
					raw, err := decodeJSONResource(decoder, &monitor, p.config.StrictUnknownFields)
					end := decoder.InputOffset()
					if err != nil {
						return jsonSourceError(ctx, p.filename, item, raw, end, err)
					}
					if err := manifest.ValidateMonitor(&monitor); err != nil {
						return jsonSourceError(ctx, p.filename, item, raw, end, err)
					}
					if err := manifest.ValidateMonitorReferences(&monitor, endpoints, groups); err != nil {
						return jsonSourceError(ctx, p.filename, item, raw, end, err)
					}
					sources = append(sources, MonitorSource{Item: item, Offset: end - int64(len(raw))})
					batch = append(batch, monitor)
				}
				if len(batch) > 0 {
					select {
					case batchChan <- MonitorBatch{
						Monitors:           batch,
						Sources:            sources,
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
			return jsonDecoderError(decoder, err, "")
		}
		if t != json.Delim(']') {
			return api.InvalidField("monitors", "invalidJSON")
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
