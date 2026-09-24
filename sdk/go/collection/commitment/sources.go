package commitment

import (
	"fmt"
	"hash"
)

// SourceAccumulator fingerprints ordered raw sources incrementally. It keeps
// neither paths nor source bodies. Begin/Write/End define unambiguous boundaries.
// Its byte quota covers all source bytes, including an open source. The zero value
// is invalid. It is single-owner and must not be copied after initialization.
type SourceAccumulator struct {
	key                                    [KeyBytes]byte
	mac, source                            hash.Hash
	count, ended, total, current, maxBytes uint64
	token                                  string
	finished, failed                       bool
}

func (SourceAccumulator) String() string               { return "private source inventory (key and input omitted)" }
func (s SourceAccumulator) GoString() string           { return s.String() }
func (SourceAccumulator) MarshalJSON() ([]byte, error) { return nil, ErrSerialization }
func (s SourceAccumulator) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(s.String())) }

// Close clears the owned raw key and drops MAC references, including an open
// source. It is safe to defer or call repeatedly. Go/HMAC copies cannot be
// guaranteed erased from memory.
func (s *SourceAccumulator) Close() error {
	if s != nil {
		clear(s.key[:])
		s.mac, s.source, s.token = nil, nil, ""
		s.finished = true
	}
	return nil
}

func (s *SourceAccumulator) fail(err error) error {
	s.failed = true
	_ = s.Close()
	return err
}

// NewSourceAccumulator accepts a source count and explicit total byte quota.
// Zero sources and a zero quota are valid for an empty local collection; nonempty
// sources may be empty files. No resource or source randomness is generated here.
func NewSourceAccumulator(key []byte, sourceCount, maxBytes uint64) (*SourceAccumulator, error) {
	if len(key) != KeyBytes {
		return nil, ErrKey
	}
	if sourceCount > MaxSources || maxBytes > MaxSourceBytes {
		return nil, ErrBounds
	}
	s := &SourceAccumulator{count: sourceCount, maxBytes: maxBytes}
	copy(s.key[:], key)
	s.mac = beginMAC(s.key[:], "cpra.collection.sources.v1")
	number(s.mac, sourceCount)
	return s, nil
}

// Begin opens the next source. Tokens must exactly match SourceToken(1), (2),
// and so on; no identity set is needed to detect duplicate/reordered tokens.
func (s *SourceAccumulator) Begin(token string) error {
	if s == nil || s.mac == nil || s.finished || s.failed {
		return ErrState
	}
	if s.source != nil || s.ended >= s.count {
		return s.fail(ErrState)
	}
	expected, err := SourceToken(s.ended + 1)
	if err != nil || token != expected {
		return s.fail(ErrPosition)
	}
	s.source = beginMAC(s.key[:], "cpra.collection.source-bytes.v1")
	s.token, s.current = token, 0
	return nil
}

// Write consumes part of the open source. Chunk boundaries do not affect the
// fingerprint. A quota failure consumes no bytes and invalidates the accumulator.
func (s *SourceAccumulator) Write(data []byte) (int, error) {
	if s == nil || s.mac == nil || s.finished || s.failed {
		return 0, ErrState
	}
	if s.source == nil {
		return 0, s.fail(ErrState)
	}
	n := uint64(len(data))
	if n > s.maxBytes-s.total {
		return 0, s.fail(ErrBounds)
	}
	_, _ = s.source.Write(data)
	s.total += n
	s.current += n
	return len(data), nil
}

// End closes a source. Its byte length and keyed source MAC enter the outer
// fingerprint as separate length-framed fields, so concatenation is unambiguous.
func (s *SourceAccumulator) End() error {
	if s == nil || s.mac == nil || s.finished || s.failed {
		return ErrState
	}
	if s.source == nil {
		return s.fail(ErrState)
	}
	digest := sum(s.source)
	number(s.mac, s.ended+1)
	field(s.mac, []byte(s.token))
	number(s.mac, s.current)
	field(s.mac, digest[:])
	s.ended++
	s.source, s.token, s.current = nil, "", 0
	return nil
}

// Finish succeeds once after all sources are closed. It clears the explicitly
// retained key buffer but cannot guarantee erasure of HMAC/runtime key copies.
func (s *SourceAccumulator) Finish() ([MACBytes]byte, error) {
	if s == nil || s.mac == nil || s.finished || s.failed {
		return [MACBytes]byte{}, ErrState
	}
	if s.source != nil || s.ended != s.count {
		return [MACBytes]byte{}, s.fail(ErrState)
	}
	result := sum(s.mac)
	_ = s.Close()
	return result, nil
}
