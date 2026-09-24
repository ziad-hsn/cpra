package management

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

const reselectionRawSourceLimit = 64 << 20

var (
	errReselectionSources     = errors.New("encrypted reselection sources unavailable")
	errReselectionSourceOrder = errors.New("reselection source part does not match expected position")
)

type reselectionSourcesProgress struct {
	SourceCount      int
	SourcesCompleted int
	RawBytes         int64
	Complete         bool
}

type reselectionSourcePart struct {
	ref reselectionSpoolRecord
	end bool
}

// Source order and completion live only in this process. The enclosing attempt
// owns expiry, authorization and the spool lifetime; this object never closes
// the spool. mu serializes uploads and freezes the frame indexes at completion.
// No end-user resume capability is provided by this staging primitive.
type reselectionSources struct {
	mu          *sync.Mutex
	spool       *reselectionSpool
	maxRawBytes int64
	state       reselectionSourcesProgress
	frames      [][]reselectionSpoolRecord
	partCount   int
	nextOffset  uint64
	last        *reselectionSourcePart
}

func (reselectionSources) String() string               { return "private encrypted reselection sources" }
func (s reselectionSources) GoString() string           { return s.String() }
func (s reselectionSources) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(s.String())) }
func (reselectionSources) MarshalJSON() ([]byte, error) { return nil, errReselectionSources }

func newReselectionSources(spool *reselectionSpool, sourceCount int, maxRawBytes int64) (*reselectionSources, error) {
	if spool == nil || sourceCount < 1 || sourceCount > 1000 || maxRawBytes < 1 || maxRawBytes > reselectionRawSourceLimit {
		return nil, ErrValidation
	}
	if _, err := spool.accounting(); err != nil {
		return nil, err
	}
	return &reselectionSources{
		mu: &sync.Mutex{}, spool: spool, maxRawBytes: maxRawBytes,
		state:  reselectionSourcesProgress{SourceCount: sourceCount},
		frames: make([][]reselectionSpoolRecord, sourceCount),
	}, nil
}

// append accepts the next ordered part, or a byte-identical retry of the last
// accepted part. Retrying reads that original ciphertext; it neither appends
// another frame nor changes completion/accounting. Empty end parts are stored
// as authenticated empty frames. No digest of private input is exposed.
func (s *reselectionSources) append(ctx context.Context, source, offset uint64, end bool, data []byte) (bool, error) {
	if s == nil || s.mu == nil || ctx == nil || source == 0 || len(data) > reselectionFrameLimit || len(data) == 0 && !end {
		return false, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if source > uint64(s.state.SourceCount) {
		return false, ErrValidation
	}
	if last := s.last; last != nil && source == last.ref.source && offset == last.ref.position {
		if end != last.end || len(data) != last.ref.length {
			return false, errReselectionSourceOrder
		}
		err := s.spool.withRecord(ctx, last.ref, reselectionSource, func(original []byte) error {
			if !bytes.Equal(original, data) {
				return errReselectionSourceOrder
			}
			return nil
		})
		return err == nil, err
	}
	if s.state.Complete || source != uint64(s.state.SourcesCompleted+1) || offset != s.nextOffset {
		return false, errReselectionSourceOrder
	}
	if int64(len(data)) > s.maxRawBytes-s.state.RawBytes || s.partCount >= reselectionRecordLimit {
		return false, errReselectionSpoolQuota
	}
	ref, err := s.spool.appendSource(ctx, source, offset, data)
	if err != nil {
		return false, err
	}
	s.frames[source-1] = append(s.frames[source-1], ref)
	s.partCount++
	s.last = &reselectionSourcePart{ref: ref, end: end}
	s.state.RawBytes += int64(len(data))
	s.nextOffset += uint64(len(data))
	if end {
		s.state.SourcesCompleted++
		s.nextOffset = 0
		s.state.Complete = s.state.SourcesCompleted == s.state.SourceCount
	}
	return false, nil
}

func (s *reselectionSources) progress() (reselectionSourcesProgress, error) {
	if s == nil || s.mu == nil {
		return reselectionSourcesProgress{}, errReselectionSources
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.spool.accounting(); err != nil {
		return reselectionSourcesProgress{}, err
	}
	return s.state, nil
}

func (s *reselectionSources) reader(ctx context.Context, source uint64) (io.ReadCloser, error) {
	if s == nil || s.mu == nil || ctx == nil || source == 0 {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if source > uint64(s.state.SourceCount) {
		return nil, ErrValidation
	}
	if !s.state.Complete {
		return nil, errReselectionSources
	}
	if _, err := s.spool.accounting(); err != nil {
		return nil, err
	}
	// Completion freezes these indexes. Readers borrow immutable references,
	// never a concatenated source or a copy of every attempt's frame index.
	return &reselectionSourceReader{mu: &sync.Mutex{}, ctx: ctx, spool: s.spool, refs: s.frames[source-1]}, nil
}

// Read releases spool locks before returning bytes to a parser. The parser can
// append normalized suffix frames to that same spool without lock reentry.
// A reader owns at most one frame of scratch, serializes Read/Close with mu,
// and clears its scratch on exhaustion, error, cancellation or Close. Callers
// retain ownership of the bytes copied into their Read buffer.
type reselectionSourceReader struct {
	mu       *sync.Mutex
	ctx      context.Context
	spool    *reselectionSpool
	refs     []reselectionSpoolRecord
	next     int
	scratch  []byte
	position int
	terminal error
	closed   bool
}

func (reselectionSourceReader) String() string               { return "private encrypted reselection source reader" }
func (r reselectionSourceReader) GoString() string           { return r.String() }
func (r reselectionSourceReader) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(r.String())) }
func (reselectionSourceReader) MarshalJSON() ([]byte, error) { return nil, errReselectionSources }

func (r *reselectionSourceReader) discard() {
	clear(r.scratch)
	r.scratch = nil
	r.position = 0
}

func (r *reselectionSourceReader) fail(err error) (int, error) {
	r.discard()
	r.terminal = err
	return 0, err
}

func (r *reselectionSourceReader) Read(p []byte) (int, error) {
	if r == nil || r.mu == nil {
		return 0, errReselectionSources
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, errReselectionSources
	}
	if err := r.ctx.Err(); err != nil {
		return r.fail(err)
	}
	if r.terminal != nil {
		return 0, r.terminal
	}
	if len(p) == 0 {
		return 0, nil
	}
	for r.position == len(r.scratch) {
		r.discard()
		if r.next == len(r.refs) {
			return r.fail(io.EOF)
		}
		if err := r.spool.withRecord(r.ctx, r.refs[r.next], reselectionSource, func(frame []byte) error {
			r.scratch = append([]byte(nil), frame...)
			return nil
		}); err != nil {
			return r.fail(err)
		}
		r.next++
	}
	n := copy(p, r.scratch[r.position:])
	r.position += n
	if r.position == len(r.scratch) {
		r.discard()
	}
	return n, nil
}

func (r *reselectionSourceReader) Close() error {
	if r == nil || r.mu == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.discard()
	r.refs = nil
	r.ctx = nil
	r.spool = nil
	return nil
}
