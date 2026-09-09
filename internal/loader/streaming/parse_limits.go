package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

func normalizedParseConfig(c ParseConfig) ParseConfig {
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
	if c.BatchSize > 10000 {
		c.BatchSize = 10000
	}
	if c.MaxMemory <= 0 {
		c.MaxMemory = 64 << 20
	}
	return c
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
