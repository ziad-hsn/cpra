package loader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func normalizedParseConfig(c ParseConfig) ParseConfig {
	// Operational manifests have one strict contract. An omitted setting must
	// not silently turn misspelled fields into ignored configuration.
	c.StrictUnknownFields = true
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
	if c.BatchSize > 10000 {
		c.BatchSize = 10000
	}
	if c.MaxMemory <= 0 {
		c.MaxMemory = 64 << 20
	}
	if c.MaxExpandedBytes <= 0 {
		c.MaxExpandedBytes = c.MaxMemory
	}
	if c.MaxBatchBytes <= 0 || c.MaxBatchBytes > 4<<20 {
		c.MaxBatchBytes = 4 << 20
	}
	return c
}

// Measure the decoded representation, not the compact source or alias token.
// Only one temporary encoding is held at a time. This bounds payload retention,
// not total Go heap: parser syntax, indexes and shared metadata have own limits.
func decodedMonitorBytes(monitor manifest.Monitor, limit int64) (int64, error) {
	raw, err := json.Marshal(monitor)
	if err != nil {
		return 0, fmt.Errorf("decoded monitor cannot be encoded")
	}
	size := int64(len(raw))
	clear(raw)
	if size > limit {
		return 0, fmt.Errorf("decoded monitor exceeds batch byte limit")
	}
	return size, nil
}

// boundedReader caps decompressed input and checks cancellation on every read.
// MaxMemory is an input budget, not an exact Go heap limit.
type boundedReader struct {
	ctx       context.Context
	reader    io.Reader
	remaining int64
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.remaining <= 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n == 0 {
			return 0, err
		}
		return 0, fmt.Errorf("manifest exceeds decompressed input budget")
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

// Skip one value token by token. In particular, a monitor array is
// never decoded into one RawMessage just to discover metadata after it.
func skipJSONValue(ctx context.Context, d *json.Decoder) error {
	depth := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := t.(json.Delim); ok {
			switch delimiter {
			case '[', '{':
				depth++
			case ']', '}':
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
		if depth > 100 {
			return fmt.Errorf("manifest nesting exceeds 100 levels")
		}
	}
}
