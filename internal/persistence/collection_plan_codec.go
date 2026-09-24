package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"

	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

const (
	CollectionPlanCodecVersion     = 1
	CollectionPlanCompilerVersion  = "cpra.staged-prefix.v1"
	CollectionPlanMaxFragmentBytes = 1 << 20
	CollectionPlanMaxChunkEntries  = 256
	// This is an encoded-stream budget, distinct from the compiler's retained
	// metadata budget and from process RSS. It includes framing and footers.
	CollectionPlanMaxBytes = 32 << 20
)

var (
	ErrCollectionPlanInvalid = errors.New("invalid collection plan artifact")
	ErrCollectionPlanLimit   = errors.New("collection plan artifact limit exceeded")
)

const collectionPlanMagic = "CPRA-COLLECTION-PLAN\x00\x01"

// CollectionPlanHeader binds a success-only artifact precursor to one complete
// original input and one caller-selected plan identity. It contains no secrets,
// source paths, prepared mutations or authority grant. A future durable Begin
// must also bind the complete Descriptor before accepting any fragments.
// Validation failures require a separate bounded result format; they cannot be
// represented as an empty or incomplete successful plan.
type CollectionPlanHeader struct {
	CodecVersion        int    `json:"codec_version"`
	CompilerVersion     string `json:"compiler_version"`
	PlanID              string `json:"plan_id"`
	OperationID         string `json:"operation_id"`
	UploadID            string `json:"upload_id"`
	Actor               string `json:"actor"`
	IdentityFormat      string `json:"identity_format"`
	ContentDigest       string `json:"content_digest"`
	InputProgressDigest string `json:"input_progress_digest"`
	ItemCount           uint64 `json:"item_count"`
	ObservedIndex       uint64 `json:"observed_index"`
}

// CollectionPlanGuard is the original observation, never a refreshed version.
// FromOrdinal refers to that exact earlier row's future successful outcome.
// ReverseVersion preserves nil versus the valid observed zero token.
type CollectionPlanGuard struct {
	Key                CatalogKey `json:"key"`
	OriginalUID        string     `json:"original_uid"`
	OriginalRevision   string     `json:"original_revision"`
	OriginalGeneration int64      `json:"original_generation"`
	Absent             bool       `json:"absent"`
	FromOrdinal        uint64     `json:"from_ordinal"`
	ReverseVersion     *uint64    `json:"reverse_version,omitempty"`
}

type CollectionPlanRow struct {
	Ordinal       uint64              `json:"ordinal"`
	InputOrdinal  uint64              `json:"input_ordinal"`
	Source        string              `json:"source"`
	Document      uint64              `json:"document"`
	Item          uint64              `json:"item"`
	Key           CatalogKey          `json:"key"`
	Change        string              `json:"change"`
	Target        CollectionPlanGuard `json:"target"`
	GuardCount    uint64              `json:"guard_count"`
	RequiresCount uint64              `json:"requires_count"`
	TouchesCount  uint64              `json:"touches_count"`
}

// RowEnd hashes the framed row-begin and chunks, excluding itself. Footer hashes
// the magic and all preceding frames, excluding itself. The outer Descriptor
// covers the entire stream, including both kinds of footer, without a cycle.
type CollectionPlanRowEnd struct {
	Ordinal       uint64 `json:"ordinal"`
	GuardCount    uint64 `json:"guard_count"`
	RequiresCount uint64 `json:"requires_count"`
	TouchesCount  uint64 `json:"touches_count"`
	Digest        string `json:"digest"`
}

type CollectionPlanFooter struct {
	Rows      uint64 `json:"rows"`
	Fragments uint64 `json:"fragments"`
	Bytes     uint64 `json:"bytes"`
	Digest    string `json:"digest"`
}

// CollectionPlanFragment is a strict tagged union. The sole legal sequence is
// header, then each row's begin/guards/requires/touches/end, then footer. Empty
// categories have no chunk. Array chunks contain 1..256 entries. Callbacks own
// their decoded value, but must regard it as provisional until Decode succeeds.
type CollectionPlanFragment struct {
	Kind     string                `json:"kind"`
	Header   *CollectionPlanHeader `json:"header,omitempty"`
	Row      *CollectionPlanRow    `json:"row,omitempty"`
	Guards   []CollectionPlanGuard `json:"guards,omitempty"`
	Requires []uint64              `json:"requires,omitempty"`
	Touches  []CatalogKey          `json:"touches,omitempty"`
	End      *CollectionPlanRowEnd `json:"end,omitempty"`
	Footer   *CollectionPlanFooter `json:"footer,omitempty"`
}

// CollectionPlanDescriptor is returned only after complete successful encoding
// or decoding. Callers can dry-write the frozen plan to io.Discard, bind this
// descriptor, then emit the same plan again. Resume must use those exact bytes
// and descriptor; recompiling against later live resources is not a resume.
type CollectionPlanDescriptor struct {
	CodecVersion int    `json:"codec_version"`
	Fragments    uint64 `json:"fragments"`
	Bytes        uint64 `json:"bytes"`
	Digest       string `json:"digest"`
}

type collectionPlanState struct {
	header                 CollectionPlanHeader
	row                    *CollectionPlanRow
	rows, guards, requires uint64
	touches, lastRequired  uint64
	lastGuard, lastTouch   CatalogKey
	keys                   map[CatalogKey]collectionPlanPrior
	inputs                 map[uint64]struct{}
	needed                 map[uint64]struct{}
	stream, rowHash        hash.Hash
	fragments, bytes       uint64
	finished               bool
}

type collectionPlanPrior struct {
	ordinal uint64
	changed bool
}

// CollectionPlanEncoder is scoped to EncodeCollectionPlan's callback and has
// one owner. It retains only identity indexes, the current row and running
// digests, never complete rows or their guards. No method invokes providers,
// storage or authorization. All errors are sticky even if the callback ignores
// them. Reader/Writer cancellation itself remains the caller's responsibility.
type CollectionPlanEncoder struct {
	ctx   context.Context
	w     io.Writer
	state collectionPlanState
	err   error
	done  bool
}

func EncodeCollectionPlan(ctx context.Context, w io.Writer, header CollectionPlanHeader, emit func(*CollectionPlanEncoder) error) (CollectionPlanDescriptor, error) {
	if ctx == nil || w == nil || emit == nil {
		return CollectionPlanDescriptor{}, ErrCollectionPlanInvalid
	}
	e := &CollectionPlanEncoder{ctx: ctx, w: w, state: newCollectionPlanState()}
	defer func() { e.done = true; e.w = nil; e.state.release() }()
	if err := e.ctx.Err(); err != nil {
		return CollectionPlanDescriptor{}, err
	}
	if err := header.validate(); err != nil {
		return CollectionPlanDescriptor{}, err
	}
	if err := e.write([]byte(collectionPlanMagic)); err != nil {
		return CollectionPlanDescriptor{}, err
	}
	if err := e.fragment(CollectionPlanFragment{Kind: "header", Header: &header}); err != nil {
		return CollectionPlanDescriptor{}, err
	}
	if err := emit(e); err != nil {
		return CollectionPlanDescriptor{}, err
	}
	if e.err != nil {
		return CollectionPlanDescriptor{}, e.err
	}
	if err := e.ctx.Err(); err != nil {
		return CollectionPlanDescriptor{}, err
	}
	f := e.state.footer()
	if err := e.fragment(CollectionPlanFragment{Kind: "footer", Footer: &f}); err != nil {
		return CollectionPlanDescriptor{}, err
	}
	return e.state.descriptor(), nil
}

func (e *CollectionPlanEncoder) BeginRow(row CollectionPlanRow) error {
	return e.fragment(CollectionPlanFragment{Kind: "row", Row: &row})
}
func (e *CollectionPlanEncoder) WriteGuards(guards []CollectionPlanGuard) error {
	return e.fragment(CollectionPlanFragment{Kind: "guards", Guards: guards})
}
func (e *CollectionPlanEncoder) WriteRequires(requires []uint64) error {
	return e.fragment(CollectionPlanFragment{Kind: "requires", Requires: requires})
}
func (e *CollectionPlanEncoder) WriteTouches(touches []CatalogKey) error {
	return e.fragment(CollectionPlanFragment{Kind: "touches", Touches: touches})
}
func (e *CollectionPlanEncoder) EndRow() error {
	if e == nil || e.done || e.state.row == nil {
		return e.fail(ErrCollectionPlanInvalid)
	}
	f := e.state.rowEnd()
	return e.fragment(CollectionPlanFragment{Kind: "end", End: &f})
}

func (e *CollectionPlanEncoder) fail(err error) error {
	if e == nil {
		return ErrCollectionPlanInvalid
	}
	if e.err == nil {
		e.err = err
	}
	return e.err
}

func (e *CollectionPlanEncoder) fragment(f CollectionPlanFragment) error {
	if e == nil || e.done || e.ctx == nil || e.w == nil || e.state.stream == nil {
		return e.fail(ErrCollectionPlanInvalid)
	}
	if e.err != nil {
		return e.err
	}
	if err := e.ctx.Err(); err != nil {
		return e.fail(err)
	}
	// Check field and count bounds before Marshal can allocate from caller data.
	if err := f.validate(); err != nil {
		return e.fail(err)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return e.fail(ErrCollectionPlanInvalid)
	}
	if len(raw) > CollectionPlanMaxFragmentBytes || uint64(4+len(raw)) > CollectionPlanMaxBytes-e.state.bytes {
		return e.fail(ErrCollectionPlanLimit)
	}
	if err := e.state.accept(e.ctx, f); err != nil {
		return e.fail(err)
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
	if err := e.write(size[:]); err != nil {
		return e.fail(err)
	}
	if err := e.write(raw); err != nil {
		return e.fail(err)
	}
	e.state.fragments++
	return nil
}

func (e *CollectionPlanEncoder) write(raw []byte) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	n, err := e.w.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return err
	}
	e.state.observe(raw)
	return e.ctx.Err()
}

// DecodeCollectionPlan validates exact canonical frames and every sequence,
// identity, count, digest and final EOF before returning a descriptor. Callbacks
// can stream into unpublished staging, but must discard it on ANY error. A
// successful codec result is structural evidence, never permission to execute.
// Checks surround I/O/callbacks; a blocking reader must support caller-managed
// cancellation. Limits are checked before allocating a declared frame or array.
func DecodeCollectionPlan(ctx context.Context, r io.Reader, visit func(CollectionPlanFragment) error) (CollectionPlanDescriptor, error) {
	if ctx == nil || r == nil || visit == nil {
		return CollectionPlanDescriptor{}, ErrCollectionPlanInvalid
	}
	s := newCollectionPlanState()
	defer s.release()
	read := func(raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := io.ReadFull(r, raw)
		if err != nil {
			return errors.Join(ErrCollectionPlanInvalid, err)
		}
		return ctx.Err()
	}
	magic := make([]byte, len(collectionPlanMagic))
	if err := read(magic); err != nil {
		return CollectionPlanDescriptor{}, err
	}
	if string(magic) != collectionPlanMagic {
		return CollectionPlanDescriptor{}, ErrCollectionPlanInvalid
	}
	s.observe(magic)
	for !s.finished {
		var size [4]byte
		if err := read(size[:]); err != nil {
			return CollectionPlanDescriptor{}, err
		}
		n := binary.BigEndian.Uint32(size[:])
		if n == 0 || n > CollectionPlanMaxFragmentBytes || uint64(n)+4 > CollectionPlanMaxBytes-s.bytes {
			return CollectionPlanDescriptor{}, ErrCollectionPlanLimit
		}
		raw := make([]byte, n)
		if err := read(raw); err != nil {
			return CollectionPlanDescriptor{}, err
		}
		if err := collectionPlanJSON(ctx, raw); err != nil {
			return CollectionPlanDescriptor{}, err
		}
		var f CollectionPlanFragment
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&f) != nil {
			return CollectionPlanDescriptor{}, ErrCollectionPlanInvalid
		}
		if err := f.validate(); err != nil {
			return CollectionPlanDescriptor{}, err
		}
		canonical, err := json.Marshal(f)
		if err != nil || !bytes.Equal(raw, canonical) {
			return CollectionPlanDescriptor{}, ErrCollectionPlanInvalid
		}
		if err := s.accept(ctx, f); err != nil {
			return CollectionPlanDescriptor{}, err
		}
		s.observe(size[:])
		s.observe(raw)
		s.fragments++
		if err := ctx.Err(); err != nil {
			return CollectionPlanDescriptor{}, err
		}
		if err := visit(f); err != nil {
			return CollectionPlanDescriptor{}, err
		}
		if err := ctx.Err(); err != nil {
			return CollectionPlanDescriptor{}, err
		}
	}
	var extra [1]byte
	n, err := io.ReadFull(r, extra[:])
	if ctx.Err() != nil {
		return CollectionPlanDescriptor{}, ctx.Err()
	}
	if n != 0 || err != io.EOF {
		return CollectionPlanDescriptor{}, ErrCollectionPlanInvalid
	}
	return s.descriptor(), nil
}

func newCollectionPlanState() collectionPlanState {
	return collectionPlanState{stream: sha256.New(), keys: make(map[CatalogKey]collectionPlanPrior), inputs: make(map[uint64]struct{})}
}

func (s *collectionPlanState) release() {
	s.keys, s.inputs, s.needed, s.row, s.stream, s.rowHash = nil, nil, nil, nil, nil, nil
}

func (s *collectionPlanState) observe(raw []byte) {
	_, _ = s.stream.Write(raw)
	if s.rowHash != nil {
		_, _ = s.rowHash.Write(raw)
	}
	s.bytes += uint64(len(raw))
}

func (s *collectionPlanState) descriptor() CollectionPlanDescriptor {
	return CollectionPlanDescriptor{CodecVersion: CollectionPlanCodecVersion, Fragments: s.fragments, Bytes: s.bytes, Digest: hex.EncodeToString(s.stream.Sum(nil))}
}
func (s *collectionPlanState) footer() CollectionPlanFooter {
	return CollectionPlanFooter{Rows: s.rows, Fragments: s.fragments, Bytes: s.bytes, Digest: hex.EncodeToString(s.stream.Sum(nil))}
}
func (s *collectionPlanState) rowEnd() CollectionPlanRowEnd {
	return CollectionPlanRowEnd{Ordinal: s.row.Ordinal, GuardCount: s.guards, RequiresCount: s.requires, TouchesCount: s.touches,
		Digest: hex.EncodeToString(s.rowHash.Sum(nil))}
}

func (s *collectionPlanState) accept(ctx context.Context, f CollectionPlanFragment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.finished {
		return ErrCollectionPlanInvalid
	}
	if s.fragments == 0 {
		if f.Kind != "header" {
			return ErrCollectionPlanInvalid
		}
		s.header = *f.Header
		return nil
	}
	switch f.Kind {
	case "row":
		r := *f.Row
		if s.row != nil || r.Ordinal != s.rows+1 || r.Ordinal > s.header.ItemCount || r.InputOrdinal > s.header.ItemCount ||
			r.RequiresCount >= r.Ordinal || r.Target.FromOrdinal != 0 || r.Target.Key != r.Key {
			return ErrCollectionPlanInvalid
		}
		if _, exists := s.inputs[r.InputOrdinal]; exists {
			return ErrCollectionPlanInvalid
		}
		if _, exists := s.keys[r.Key]; exists {
			return ErrCollectionPlanInvalid
		}
		if r.Target.ReverseVersion != nil {
			copy := *r.Target.ReverseVersion
			r.Target.ReverseVersion = &copy
		}
		s.row, s.rowHash = &r, sha256.New()
		s.guards, s.requires, s.touches, s.lastRequired = 0, 0, 0, 0
		s.lastGuard, s.lastTouch = CatalogKey{}, CatalogKey{}
		s.needed = make(map[uint64]struct{})
		s.inputs[r.InputOrdinal] = struct{}{}
	case "guards":
		if s.row == nil || s.requires != 0 || s.touches != 0 || uint64(len(f.Guards)) > s.row.GuardCount-s.guards {
			return ErrCollectionPlanInvalid
		}
		for _, g := range f.Guards {
			if err := ctx.Err(); err != nil {
				return err
			}
			if g.Key == s.row.Key || s.guards > 0 && g.Key.indexKey() <= s.lastGuard.indexKey() || g.FromOrdinal >= s.row.Ordinal {
				return ErrCollectionPlanInvalid
			}
			prior, included := s.keys[g.Key]
			if included {
				if prior.changed && g.FromOrdinal != prior.ordinal {
					return ErrCollectionPlanInvalid
				}
				// Earlier unchanged inputs also require their original conditional
				// success. Existing bytes cannot stand in for a failed included row.
				s.needed[prior.ordinal] = struct{}{}
			}
			if g.FromOrdinal != 0 {
				if !included || prior.ordinal != g.FromOrdinal || !prior.changed {
					return ErrCollectionPlanInvalid
				}
			} else if g.Absent {
				return ErrCollectionPlanInvalid
			}
			s.lastGuard, s.guards = g.Key, s.guards+1
		}
	case "requires":
		if s.row == nil || s.guards != s.row.GuardCount || s.touches != 0 || uint64(len(f.Requires)) > s.row.RequiresCount-s.requires {
			return ErrCollectionPlanInvalid
		}
		for _, ordinal := range f.Requires {
			if err := ctx.Err(); err != nil {
				return err
			}
			if ordinal <= s.lastRequired || ordinal >= s.row.Ordinal {
				return ErrCollectionPlanInvalid
			}
			delete(s.needed, ordinal)
			s.lastRequired, s.requires = ordinal, s.requires+1
		}
	case "touches":
		if s.row == nil || s.guards != s.row.GuardCount || s.requires != s.row.RequiresCount || uint64(len(f.Touches)) > s.row.TouchesCount-s.touches {
			return ErrCollectionPlanInvalid
		}
		for _, key := range f.Touches {
			if err := ctx.Err(); err != nil {
				return err
			}
			if key == s.row.Key || s.touches > 0 && key.indexKey() <= s.lastTouch.indexKey() {
				return ErrCollectionPlanInvalid
			}
			s.lastTouch, s.touches = key, s.touches+1
		}
	case "end":
		if s.row == nil || s.guards != s.row.GuardCount || s.requires != s.row.RequiresCount || s.touches != s.row.TouchesCount ||
			len(s.needed) != 0 || *f.End != s.rowEnd() {
			return ErrCollectionPlanInvalid
		}
		s.keys[s.row.Key] = collectionPlanPrior{ordinal: s.row.Ordinal, changed: s.row.Change != "unchanged"}
		s.rows++
		s.row, s.rowHash, s.needed = nil, nil, nil
	case "footer":
		if s.row != nil || s.rows != s.header.ItemCount || *f.Footer != s.footer() {
			return ErrCollectionPlanInvalid
		}
		s.finished = true
	default:
		return ErrCollectionPlanInvalid
	}
	return nil
}

func (h CollectionPlanHeader) validate() error {
	_, _, err := ParseOperationHandle(h.OperationID)
	if h.CodecVersion != CollectionPlanCodecVersion || h.CompilerVersion != CollectionPlanCompilerVersion ||
		!validOperationEpoch(h.PlanID) || err != nil || !validOperationEpoch(h.UploadID) || !catalogIdentifier(h.Actor, 128) ||
		h.IdentityFormat != commitment.Format || !bootstrapHash(h.ContentDigest) || !bootstrapHash(h.InputProgressDigest) ||
		h.ItemCount == 0 || h.ItemCount > maxCollectionItems {
		return ErrCollectionPlanInvalid
	}
	return nil
}

func collectionPlanKeyValid(key CatalogKey) bool {
	if bootstrapOrder(key) == "" || key.validate() != nil {
		return false
	}
	var zero [commitment.KeyBytes]byte
	_, err := commitment.ItemMAC(zero[:], commitment.Position{Ordinal: 1, ID: key.Kind + "/" + key.ID,
		Source: commitment.SourcePosition{Token: "source.00000000000000000001", Document: 1, Item: 1}}, []byte("{}"))
	return err == nil
}

func (g CollectionPlanGuard) validate() error {
	if !collectionPlanKeyValid(g.Key) || g.FromOrdinal > maxCollectionItems {
		return ErrCollectionPlanInvalid
	}
	if g.Absent {
		if g.OriginalUID != "" || g.OriginalRevision != "" || g.OriginalGeneration != 0 || g.ReverseVersion != nil {
			return ErrCollectionPlanInvalid
		}
	} else if !catalogIdentifier(g.OriginalUID, 256) || !catalogIdentifier(g.OriginalRevision, 256) || g.OriginalGeneration <= 0 {
		return ErrCollectionPlanInvalid
	}
	return nil
}

func (r CollectionPlanRow) validate() error {
	if r.Ordinal == 0 || r.Ordinal > maxCollectionItems || r.InputOrdinal == 0 || r.InputOrdinal > maxCollectionItems ||
		!collectionPlanKeyValid(r.Key) || r.Target.validate() != nil || r.Target.Key != r.Key || r.Target.FromOrdinal != 0 ||
		r.GuardCount > maxCollectionItems || r.RequiresCount > maxCollectionItems || r.TouchesCount > maxCollectionItems {
		return ErrCollectionPlanInvalid
	}
	if r.Change != "create" && r.Change != "update" && r.Change != "unchanged" || (r.Change == "create") != r.Target.Absent ||
		r.Change == "unchanged" && r.TouchesCount != 0 || r.Change != "create" && r.Target.ReverseVersion == nil {
		return ErrCollectionPlanInvalid
	}
	var zero [commitment.KeyBytes]byte
	_, err := commitment.ItemMAC(zero[:], commitment.Position{Ordinal: r.InputOrdinal, ID: r.Key.Kind + "/" + r.Key.ID,
		Source: commitment.SourcePosition{Token: r.Source, Document: r.Document, Item: r.Item}}, []byte("{}"))
	if err != nil {
		return ErrCollectionPlanInvalid
	}
	return nil
}

func (f CollectionPlanFragment) validate() error {
	fields := 0
	for _, set := range []bool{f.Header != nil, f.Row != nil, f.Guards != nil, f.Requires != nil, f.Touches != nil, f.End != nil, f.Footer != nil} {
		if set {
			fields++
		}
	}
	if fields != 1 {
		return ErrCollectionPlanInvalid
	}
	switch f.Kind {
	case "header":
		if f.Header != nil {
			return f.Header.validate()
		}
	case "row":
		if f.Row != nil {
			return f.Row.validate()
		}
	case "guards":
		if len(f.Guards) == 0 || len(f.Guards) > CollectionPlanMaxChunkEntries {
			return ErrCollectionPlanLimit
		}
		for _, g := range f.Guards {
			if err := g.validate(); err != nil {
				return err
			}
		}
		return nil
	case "requires":
		if len(f.Requires) == 0 || len(f.Requires) > CollectionPlanMaxChunkEntries {
			return ErrCollectionPlanLimit
		}
		for _, ordinal := range f.Requires {
			if ordinal == 0 || ordinal > maxCollectionItems {
				return ErrCollectionPlanInvalid
			}
		}
		return nil
	case "touches":
		if len(f.Touches) == 0 || len(f.Touches) > CollectionPlanMaxChunkEntries {
			return ErrCollectionPlanLimit
		}
		for _, key := range f.Touches {
			if !collectionPlanKeyValid(key) {
				return ErrCollectionPlanInvalid
			}
		}
		return nil
	case "end":
		if f.End != nil && f.End.Ordinal > 0 && f.End.Ordinal <= maxCollectionItems && bootstrapHash(f.End.Digest) &&
			f.End.GuardCount <= maxCollectionItems && f.End.RequiresCount <= maxCollectionItems && f.End.TouchesCount <= maxCollectionItems {
			return nil
		}
	case "footer":
		if f.Footer != nil && f.Footer.Rows > 0 && f.Footer.Rows <= maxCollectionItems && f.Footer.Fragments > 0 &&
			f.Footer.Fragments <= CollectionPlanMaxBytes/4 && f.Footer.Bytes <= CollectionPlanMaxBytes && bootstrapHash(f.Footer.Digest) {
			return nil
		}
	}
	return ErrCollectionPlanInvalid
}

// Preflight token counts before decoding typed slices. The frame itself has
// already been bounded. Canonical re-encoding subsequently rejects duplicate,
// case-aliased, omitted/extra, reordered and trailing JSON members exactly.
func collectionPlanJSON(ctx context.Context, raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 8 {
			return ErrCollectionPlanInvalid
		}
		token, err := d.Token()
		if err != nil {
			return ErrCollectionPlanInvalid
		}
		if s, ok := token.(string); ok && len(s) > 1024 {
			return ErrCollectionPlanLimit
		}
		delim, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		if delim != '[' && delim != '{' {
			return ErrCollectionPlanInvalid
		}
		count := 0
		for d.More() {
			count++
			if delim == '[' && count > CollectionPlanMaxChunkEntries || delim == '{' && count > 32 {
				return ErrCollectionPlanLimit
			}
			if delim == '{' {
				key, err := d.Token()
				if err != nil {
					return ErrCollectionPlanInvalid
				}
				name, ok := key.(string)
				if !ok || len(name) > 64 {
					return ErrCollectionPlanInvalid
				}
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || delim == '[' && end != json.Delim(']') || delim == '{' && end != json.Delim('}') {
			return ErrCollectionPlanInvalid
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrCollectionPlanInvalid
	}
	return nil
}
