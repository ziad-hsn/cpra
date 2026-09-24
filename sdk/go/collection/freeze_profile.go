package collection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// FreezeProfile freezes explicitly selected files, directories, readers or URLs
// using the named file-normalization contract. Only FileNormalizationProfile is
// supported. Unsupported profiles fail before reading any source. This function
// does not contact CPRa; Apply must be invoked separately after all input freezes.
//
// Source order follows the explicit list and lexical directory traversal, with
// duplicate file paths included once. Raw comments, whitespace and empty files
// participate in the source commitment. Exact NormalizeFile resource bytes and
// coordinates participate in the item commitment; files are never read again.
//
// The base profile permits at most 1,000 expanded sources, 64 MiB cumulative raw
// input, 10,000 resources, 1 MiB per resource, 16 MiB document metadata accounting
// and 512 MiB plaintext staging. Options may tighten but never expand these
// limits. Ordinary Freeze and FreezeResources keep their separate contracts.
func FreezeProfile(ctx context.Context, sources []Source, profile string, options Options) (_ *Frozen, err error) {
	if profile != FileNormalizationProfile {
		return nil, ErrUnsupportedNormalization
	}
	if ctx == nil {
		return nil, errors.New("collection context is required")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	o, err := fileProfileOptions(options)
	if err != nil {
		return nil, err
	}
	expanded, err := expandLimit(ctx, sources, o.Recursive, 1000)
	if err != nil {
		return nil, err
	}
	b, err := newBuilder(o, uint64(len(expanded)))
	if err != nil {
		return nil, err
	}
	b.f.normalizationProfile = profile
	defer b.sources.Close()
	defer func() {
		if err != nil {
			if closeErr := b.f.Close(); closeErr != nil {
				err = errors.Join(err, errors.New("private collection staging cleanup failed"))
			}
		}
	}()
	for index, source := range expanded {
		b.sourceToken, err = commitment.SourceToken(uint64(index + 1))
		if err != nil {
			return nil, err
		}
		if err = b.profileSource(ctx, source, profile); err != nil {
			return nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = b.finish(ctx); err != nil {
		return nil, err
	}
	return b.f, nil
}

func fileProfileOptions(options Options) (Options, error) {
	if options.MaxStagingBytes == 0 {
		options.MaxStagingBytes = 512 << 20
	}
	if options.MaxSourceBytes == 0 {
		options.MaxSourceBytes = 64 << 20
	}
	if options.MaxResources == 0 {
		options.MaxResources = 10_000
	}
	o, err := options.normalized()
	if err != nil {
		return o, err
	}
	if o.MaxStagingBytes > 512<<20 || o.MaxSourceBytes > 64<<20 || o.MaxResources > 10_000 || o.MaxDocumentBytes > 16<<20 {
		return o, errors.New("file normalization profile limits exceeded")
	}
	return o, nil
}

func (b *builder) profileSource(ctx context.Context, source Source, profile string) (resultErr error) {
	r, label, err := source.open(ctx, b.o)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	defer func() {
		if err := r.Close(); err != nil && resultErr == nil {
			resultErr = errors.New("collection source close failed")
		}
	}()
	if err = b.sources.Begin(b.sourceToken); err != nil {
		return err
	}
	// SourceAccumulator checks the cumulative raw quota before releasing bytes
	// to the decoder. The bounded normalizer cannot turn discarded comments or
	// empty sources into an unaccounted gap in the source inventory.
	input := &profileSourceReader{ctx: ctx, r: r, sources: b.sources}
	err = NormalizeFile(ctx, input, profile, DecodeOptions{SourceName: label, MaxBytes: b.o.MaxSourceBytes, MaxResourceBytes: b.o.MaxResourceBytes, MaxDocumentBytes: b.o.MaxDocumentBytes, MaxResources: b.o.MaxResources}, func(item NormalizedItem) error {
		defer clear(item.JSON)
		return b.addNormalized(item)
	})
	if err != nil {
		return err
	}
	return b.sources.End()
}

type profileSourceReader struct {
	ctx     context.Context
	r       io.Reader
	sources *commitment.SourceAccumulator
}

func (r *profileSourceReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if n > 0 {
		if _, writeErr := r.sources.Write(p[:n]); writeErr != nil {
			clear(p[:n])
			return 0, errors.New("collection raw source quota exceeded")
		}
	}
	if err != nil && !errors.Is(err, io.EOF) {
		if r.ctx.Err() != nil {
			return n, r.ctx.Err()
		}
		return n, errors.New("collection source read failed")
	}
	return n, err
}

func (b *builder) addNormalized(normalized NormalizedItem) error {
	if len(b.f.records) >= b.o.MaxResources {
		return fmt.Errorf("%s: resource-count limit exceeded", normalized.Location)
	}
	if len(normalized.JSON) == 0 || len(normalized.JSON) > b.o.MaxResourceBytes {
		return errors.New("normalized resource exceeds limit")
	}
	var resource api.Resource
	if json.Unmarshal(normalized.JSON, &resource) != nil {
		return errors.New("normalized resource cannot be read")
	}
	defer func() { clear(resource.Spec); clear(resource.Status) }()
	if normalized.ID != resource.Kind+"/"+resource.Metadata.ID {
		return errors.New("normalized resource identity differs")
	}
	if previous, ok := b.seen[normalized.ID]; ok {
		return fmt.Errorf("duplicate %s at %s; first defined at %s", normalized.ID, normalized.Location, previous)
	}
	// Frozen.Item and upload envelopes use the public Resource representation.
	// Prove that representation retains the normalizer's exact committed bytes;
	// never silently substitute a second encoding for the normalized JSON.
	roundtrip, err := json.Marshal(resource)
	defer clear(roundtrip)
	if err != nil || !bytes.Equal(roundtrip, normalized.JSON) {
		return errors.New("normalized resource encoding differs")
	}
	return b.stage(Item{ID: normalized.ID, Location: normalized.Location, Resource: resource}, normalized.JSON)
}
