package collection

import (
	"context"
	"errors"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"io"
	"os"
	"path/filepath"
)

const reselectionRawBytes int64 = 64 << 20

// The spool owns raw bytes and at most 1000 offset records, never an inventory key.
type reselectionSources struct {
	dir     string
	file    *os.File
	records []record
}

func freezeReselectionSources(ctx context.Context, sources []Source, options Options) (_ *reselectionSources, err error) {
	if len(sources) == 0 || len(sources) > 1000 {
		return nil, errors.New("select between one and 1000 original sources")
	}
	if options.MaxSourceBytes == 0 {
		options.MaxSourceBytes = reselectionRawBytes
	}
	if options.MaxStagingBytes == 0 {
		options.MaxStagingBytes = reselectionRawBytes
	}
	if options.MaxSourceBytes > reselectionRawBytes {
		return nil, errors.New("original raw sources exceed the supported quota")
	}
	o, err := options.normalized()
	if err != nil {
		return nil, err
	}
	expanded, err := expandLimit(ctx, sources, o.Recursive, 1000)
	if err != nil {
		return nil, err
	}
	if len(expanded) == 0 {
		return nil, errors.New("at least one original source is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := privateDirectory(o.TempDir)
	if err != nil {
		return nil, err
	}
	s := &reselectionSources{dir: dir}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.close())
		}
	}()
	s.file, err = os.OpenFile(filepath.Join(dir, "sources"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	buffer := make([]byte, 64<<10)
	defer clear(buffer)
	limit := min(o.MaxSourceBytes, o.MaxStagingBytes, reselectionRawBytes)
	var total int64
	for _, source := range expanded {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r, _, openErr := source.open(ctx, o)
		if openErr != nil {
			return nil, openErr
		}
		n, readErr := io.CopyBuffer(s.file, &contextReader{ctx: ctx, r: io.LimitReader(r, limit-total+1)}, buffer)
		closeErr := r.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.Join(readErr, closeErr)
		}
		if n > limit-total {
			return nil, errors.New("original raw sources exceed the supported quota")
		}
		s.records = append(s.records, record{offset: total, size: int(n)})
		total += n
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *reselectionSources) close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.file != nil {
		err = s.file.Close()
		err = errors.Join(err, os.Remove(s.file.Name()))
		s.file = nil
	}
	if s.dir != "" {
		err = errors.Join(err, os.Remove(s.dir))
		s.dir = ""
	}
	return err
}
func (s *reselectionSources) check(a api.CollectionReselectionAttempt) error {
	if s == nil || s.file == nil || a.SourceCount != int64(len(s.records)) || a.SourcesCompleted < 0 || a.SourcesCompleted > a.SourceCount {
		return ErrReselectionObservation
	}
	var completed int64
	if a.SourcesCompleted > 0 {
		last := s.records[a.SourcesCompleted-1]
		completed = last.offset + int64(last.size)
	}
	if a.SourcesCompleted == a.SourceCount {
		if a.NextSource != 0 || a.NextOffset != 0 || a.RawBytes != completed {
			return ErrReselectionObservation
		}
	} else if a.NextSource != a.SourcesCompleted+1 || a.NextOffset < 0 || a.NextOffset > int64(s.records[a.NextSource-1].size) || a.RawBytes != completed+a.NextOffset {
		return ErrReselectionObservation
	}
	return nil
}
func (s *reselectionSources) part(a api.CollectionReselectionAttempt) (cpra.ReselectionSourcePart, error) {
	if err := s.check(a); err != nil {
		return cpra.ReselectionSourcePart{}, err
	}
	if a.NextSource == 0 {
		return cpra.ReselectionSourcePart{}, ErrReselectionObservation
	}
	record := s.records[a.NextSource-1]
	n := min(int64(record.size)-a.NextOffset, int64(cpra.MaxReselectionSourcePartBytes))
	part := cpra.ReselectionSourcePart{Source: a.NextSource, Offset: a.NextOffset, End: a.NextOffset+n == int64(record.size), Data: make([]byte, int(n))}
	if n != 0 {
		if _, err := s.file.ReadAt(part.Data, record.offset+a.NextOffset); err != nil {
			clear(part.Data)
			return cpra.ReselectionSourcePart{}, err
		}
	}
	return part, nil
}
func (s *reselectionSources) covers(a api.CollectionReselectionAttempt, part cpra.ReselectionSourcePart) error {
	if err := s.check(a); err != nil {
		return err
	}
	if a.SourcesCompleted >= part.Source || !part.End && a.NextSource == part.Source && a.NextOffset >= part.Offset+int64(len(part.Data)) {
		return nil
	}
	return ErrReselectionObservation
}
