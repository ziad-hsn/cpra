package collection

import (
	"bufio"
	"context"
	"fmt"
	"io"
)

// DecodeOptions bounds decoding without writing a plaintext staging file.
// The identity index retains at most MaxResources entries; only the current
// document/resource body is decoded at a time. Zero values select the same
// limits as Freeze. SourceName is a caller-supplied non-secret diagnostic label.
type DecodeOptions struct {
	SourceName       string
	MaxBytes         int64
	MaxResourceBytes int
	MaxDocumentBytes int
	MaxResources     int
}

// Decode streams YAML/JSON resources and monitor manifests from r using Freeze's
// parser, normalization and duplicate checks. It never opens a file or URL and
// does not close r. Each callback owns its item and may encrypt it before staging.
//
// A later malformed item can fail after earlier callbacks. Callbacks MUST stage
// resources without activation when whole-input validation is required. A nil
// return establishes parse completion, not cross-resource or server validation.
// Use Freeze for ordinary clients that need a complete reusable plaintext spool.
func Decode(ctx context.Context, r io.Reader, options DecodeOptions, visit func(Item) error) error {
	if r == nil || visit == nil {
		return fmt.Errorf("source reader and staging callback are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	o, err := (Options{MaxStagingBytes: options.MaxBytes, MaxResourceBytes: options.MaxResourceBytes,
		MaxDocumentBytes: options.MaxDocumentBytes, MaxResources: options.MaxResources}).normalized()
	if err != nil {
		return err
	}
	label := options.SourceName
	if label == "" {
		label = "reader"
	}
	// Labels are metadata, never an input URL or credentials. Keep error output
	// bounded; callers should use an alias rather than a full secret-bearing path.
	if len(label) > 256 {
		return fmt.Errorf("source label exceeds limit")
	}
	b := &builder{o: o, seen: make(map[string]Location), stream: visit}
	limited := &io.LimitedReader{R: r, N: o.MaxStagingBytes + 1}
	input := bufio.NewReader(&contextReader{ctx: ctx, r: limited})
	first, err := peekNonspace(input)
	if err == io.EOF {
		err = nil
	} else if err == nil {
		if first == '{' || first == '[' {
			err = b.parseJSON(ctx, input, label)
		} else {
			err = b.parseYAML(ctx, input, label)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if limited.N == 0 {
		return fmt.Errorf("source byte limit exceeded")
	}
	return err
}
