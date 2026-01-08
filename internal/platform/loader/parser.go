package loader

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cpra/internal/platform/loader/schema"

	"go.yaml.in/yaml/v3"
)

// stringBuilderPool reduces allocations by reusing strings.Builder instances.
// Each builder is pre-allocated with capacity for a typical monitor (~1KB).
var stringBuilderPool = sync.Pool{
	New: func() interface{} {
		b := &strings.Builder{}
		b.Grow(1024) // Pre-allocate for typical monitor size
		return b
	},
}

func getStringBuilder() *strings.Builder {
	return stringBuilderPool.Get().(*strings.Builder)
}

func putStringBuilder(b *strings.Builder) {
	b.Reset()
	stringBuilderPool.Put(b)
}

// readYAMLNodes reads the YAML file and sends raw nodes to the channel.
// Uses streaming mode if configured, otherwise loads full yaml.Node tree.
func (p *Pipeline) readYAMLNodes(ctx context.Context, filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Get file size for progress reporting
	var totalSize int64
	if stat, err := file.Stat(); err == nil {
		totalSize = stat.Size()
	}

	var r io.Reader = file
	isGzip := strings.HasSuffix(strings.ToLower(filename), ".gz")
	if isGzip {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer gz.Close()
		r = gz
		totalSize = 0 // Can't know decompressed size
	}

	// Use streaming mode for large files to avoid OOM
	if p.config.StreamingMode {
		return p.readYAMLStreaming(ctx, r, totalSize)
	}

	// Traditional mode: load full yaml.Node tree
	return p.readYAMLTraditional(ctx, r, totalSize)
}

// readYAMLTraditional loads the full yaml.Node tree into memory.
// Fast but uses ~500MB+ for 1M monitors - may OOM.
func (p *Pipeline) readYAMLTraditional(ctx context.Context, r io.Reader, totalSize int64) error {
	bufSize := p.config.BufferSize
	if bufSize <= 0 {
		bufSize = 64 * 1024
	}
	bufr := bufio.NewReaderSize(r, bufSize)

	decoder := yaml.NewDecoder(bufr)
	decoder.KnownFields(p.config.StrictUnknownFields)

	// Decode top-level structure
	var topLevel struct {
		Monitors yaml.Node `yaml:"monitors"`
	}
	if err := decoder.Decode(&topLevel); err != nil {
		if err == io.EOF {
			return nil // Empty file is not an error
		}
		return fmt.Errorf("failed to decode top-level: %w", err)
	}

	if topLevel.Monitors.Kind != yaml.SequenceNode {
		return fmt.Errorf("'monitors' field must be a YAML sequence")
	}

	// Send each monitor node to the workers
	for _, node := range topLevel.Monitors.Content {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			raw := RawMonitor{Node: node, Line: node.Line}
			select {
			case p.rawChan <- raw:
				atomic.AddInt64(&p.rawParsed, 1)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	return nil
}

// readYAMLStreaming parses YAML line-by-line to minimize memory usage.
// Accumulates lines for each monitor (between `-` markers) and sends raw bytes.
// Uses ~10MB for 1M monitors instead of 500MB+.
func (p *Pipeline) readYAMLStreaming(ctx context.Context, r io.Reader, totalSize int64) error {
	// Create counting reader for progress
	cr := &countingReader{reader: r, totalSize: totalSize}

	// Use large buffer for better I/O performance
	bufSize := p.config.BufferSize
	if bufSize <= 0 {
		bufSize = 4 * 1024 * 1024 // 4MB default for streaming
	}
	scanner := bufio.NewScanner(cr)
	scanner.Buffer(make([]byte, bufSize), bufSize)

	var (
		currentMonitor = getStringBuilder() // Use pooled builder
		inMonitors     bool                 // True after seeing "monitors:" line
		inMonitor      bool                 // True when accumulating a monitor
		lineNum        int
		monitorLine    int
		lastProgress   time.Time
		progressEvery  = p.config.ProgressInterval
		gcCounter      int // Counter for periodic GC hints
	)
	defer putStringBuilder(currentMonitor)

	if progressEvery <= 0 {
		progressEvery = 250 * time.Millisecond
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Check for context cancellation and report progress periodically
		if lineNum%10000 == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// Report progress
			if p.config.ProgressCallback != nil && time.Since(lastProgress) >= progressEvery {
				lastProgress = time.Now()
				p.config.ProgressCallback(LoadProgress{
					BytesRead:      cr.bytesRead,
					TotalBytes:     totalSize,
					MonitorsParsed: atomic.LoadInt64(&p.rawParsed),
					Elapsed:        time.Since(p.startTime),
					Stage:          "reading",
				})
			}
		}

		trimmed := strings.TrimSpace(line)

		// Look for "monitors:" to start parsing
		if !inMonitors {
			if trimmed == "monitors:" || strings.HasPrefix(trimmed, "monitors:") {
				inMonitors = true
			}
			continue
		}

		// Detect start of a new monitor (line starting with "- " at proper indent)
		// A monitor entry starts with "  - " (2 space indent + dash)
		if len(line) >= 2 && line[0] == ' ' && line[1] == ' ' {
			restTrimmed := strings.TrimLeft(line[2:], " ")
			if strings.HasPrefix(restTrimmed, "- ") || restTrimmed == "-" {
				// Flush previous monitor
				if inMonitor && currentMonitor.Len() > 0 {
					raw := RawMonitor{
						RawBytes: []byte(currentMonitor.String()),
						Line:     monitorLine,
					}
					select {
					case p.rawChan <- raw:
						atomic.AddInt64(&p.rawParsed, 1)
						gcCounter++
						// Hint GC every 50k monitors to reclaim memory
						if gcCounter%50000 == 0 {
							runtime.GC()
						}
					case <-ctx.Done():
						return ctx.Err()
					}
					currentMonitor.Reset()
				}
				inMonitor = true
				monitorLine = lineNum
			}
		}

		// Accumulate lines for current monitor
		if inMonitor {
			currentMonitor.WriteString(line)
			currentMonitor.WriteByte('\n')
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	// Flush last monitor
	if inMonitor && currentMonitor.Len() > 0 {
		raw := RawMonitor{
			RawBytes: []byte(currentMonitor.String()),
			Line:     monitorLine,
		}
		select {
		case p.rawChan <- raw:
			atomic.AddInt64(&p.rawParsed, 1)
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Final progress report
	if p.config.ProgressCallback != nil {
		p.config.ProgressCallback(LoadProgress{
			BytesRead:      cr.bytesRead,
			TotalBytes:     totalSize,
			MonitorsParsed: atomic.LoadInt64(&p.rawParsed),
			Elapsed:        time.Since(p.startTime),
			Stage:          "reading",
		})
	}

	return nil
}

// parseMonitorFromBytes parses a single monitor from raw YAML bytes.
// The input is expected to be a list item like:
//
//   - name: foo
//     pulse:
//     type: http
//     ...
//
// We convert it to a proper YAML document for parsing.
func (p *Pipeline) parseMonitorFromBytes(rawBytes []byte, monitor *schema.Monitor) error {
	// The raw bytes contain a list item starting with "  - "
	// We need to convert it to a standalone YAML document
	// by stripping the leading "  - " and reducing indentation

	lines := strings.Split(string(rawBytes), "\n")
	if len(lines) == 0 {
		return fmt.Errorf("empty monitor bytes")
	}

	var normalized strings.Builder
	normalized.Grow(len(rawBytes))

	// Detect indentation level from the first line
	var indentLen int

	for i, line := range lines {
		if len(line) == 0 {
			normalized.WriteByte('\n')
			continue
		}

		if i == 0 {
			// Calculate indent length based on "- " position
			trimmed := strings.TrimLeft(line, " ")
			leadingSpaces := len(line) - len(trimmed)

			if strings.HasPrefix(trimmed, "- ") {
				// Base indentation for body is leading spaces + 2 (length of "- ")
				indentLen = leadingSpaces + 2

				// "- name: foo" -> "name: foo"
				normalized.WriteString(trimmed[2:])
			} else if trimmed == "-" {
				// Just "-", next lines have the content
				indentLen = leadingSpaces + 2
				// Don't write anything for just "-"
			} else {
				// Fallback: assume no "- " (shouldn't happen given call site logic?)
				// But we handle it:
				normalized.WriteString(trimmed)
				indentLen = leadingSpaces
			}
			normalized.WriteByte('\n')
		} else {
			// Strip 'indentLen' spaces from subsequent lines
			if len(line) >= indentLen {
				// Check if it actually has spaces
				// (optimistic slicing)
				normalized.WriteString(line[indentLen:])
			} else {
				// Line is shorter than indent? Maybe empty or weird.
				// Just trim left.
				normalized.WriteString(strings.TrimLeft(line, " "))
			}
			normalized.WriteByte('\n')
		}
	}

	decoder := yaml.NewDecoder(strings.NewReader(normalized.String()))
	decoder.KnownFields(true)
	return decoder.Decode(monitor)
}

// countingReader wraps an io.Reader to track bytes read.
type countingReader struct {
	reader    io.Reader
	bytesRead int64
	totalSize int64
}

func (c *countingReader) Read(p []byte) (n int, err error) {
	n, err = c.reader.Read(p)
	c.bytesRead += int64(n)
	return n, err
}
