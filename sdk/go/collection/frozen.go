package collection

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// Location is retained without URL query strings, user credentials, or payloads.
type Location struct {
	Source   string `json:"source"`
	Document int    `json:"document"`
	Item     int    `json:"item"`
}

// String formats a source location without including its resource payload.
func (l Location) String() string {
	return fmt.Sprintf("%s document %d item %d", l.Source, l.Document, l.Item)
}

// Item is a desired resource. Frozen items carry a keyed ContentDigest and exact
// inventory position; parser-only Decode items retain a local SHA-256 digest.
// ID is the immutable identity within an apply operation, distinct from resource ID.
type Item struct {
	ID            string              `json:"id"`
	ContentDigest string              `json:"contentDigest"`
	Location      Location            `json:"location"`
	Resource      api.Resource        `json:"resource"`
	Position      commitment.Position `json:"position"`
}

func (Item) String() string               { return "private collection item (input omitted)" }
func (i Item) GoString() string           { return i.String() }
func (i Item) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(i.String())) }

type record struct {
	offset int64
	size   int
}

// Frozen holds private, plaintext, bounded client staging. Close removes all
// staging. It is not a persistent resume token and must not be included in logs
// or backups. A new Freeze observes changed files and generates a new identity;
// this instance never does. Use the returned pointer and do not copy Frozen.
type Frozen struct {
	mu                   *sync.RWMutex
	dir                  string
	file                 *os.File
	records              []record
	digest               string
	normalizationProfile string
	staged               int64
	closed               bool
	applying             bool
	admissionTicket      []byte
	key                  [commitment.KeyBytes]byte
	sourceFingerprint    [commitment.MACBytes]byte
}

// Formatting excludes private key material, source labels and staged resources.
func (Frozen) String() string               { return "private frozen collection (input omitted)" }
func (f Frozen) GoString() string           { return f.String() }
func (f Frozen) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(f.String())) }
func (Frozen) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private frozen collections cannot be serialized")
}

// Len returns the number of frozen resource records.
func (f *Frozen) Len() int {
	if f == nil || f.mu == nil {
		return 0
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.records)
}

// Digest returns the frozen collection's content identity for operation resume.
func (f *Frozen) Digest() string {
	if f == nil {
		return ""
	}
	return f.digest
}

// NormalizationProfile identifies the immutable file-normalization contract.
// Ordinary Freeze and FreezeResources return an empty profile. Closing a Frozen
// does not change this public, non-secret identity metadata.
func (f *Frozen) NormalizationProfile() string {
	if f == nil {
		return ""
	}
	return f.normalizationProfile
}

// StagedBytes reports encoded resource bytes retained in local staging.
func (f *Frozen) StagedBytes() int64 {
	if f == nil || f.mu == nil {
		return 0
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.staged
}

// Close closes and removes private plaintext staging. Calling it again is safe.
func (f *Frozen) Close() error {
	if f == nil || f.mu == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	clear(f.key[:])
	clear(f.sourceFingerprint[:])
	clear(f.admissionTicket)
	f.admissionTicket = nil
	return errors.Join(f.file.Close(), os.RemoveAll(f.dir))
}

// Item reads one bounded resource. Concurrent reads are supported until Close.
func (f *Frozen) Item(ctx context.Context, index int) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	if f == nil || f.mu == nil {
		return Item{}, errors.New("frozen collection is not initialized")
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return Item{}, fmt.Errorf("collection is closed")
	}
	if index < 0 || index >= len(f.records) {
		return Item{}, io.EOF
	}
	r := f.records[index]
	var item Item
	err := json.NewDecoder(io.NewSectionReader(f.file, r.offset, int64(r.size))).Decode(&item)
	if err != nil {
		return Item{}, errors.New("frozen resource cannot be read")
	}
	raw, err := json.Marshal(item.Resource)
	if err != nil {
		return Item{}, errors.New("frozen resource cannot be encoded")
	}
	mac, err := hex.DecodeString(item.ContentDigest)
	if err != nil || item.Position.Ordinal != uint64(index+1) || item.ID != item.Position.ID || item.ID != item.Resource.Kind+"/"+item.Resource.Metadata.ID || commitment.VerifyItem(f.key[:], item.Position, raw, mac) != nil {
		return Item{}, errors.New("frozen resource identity changed")
	}
	return item, nil
}

// Range reads at most one resource at a time in frozen source order.
func (f *Frozen) Range(ctx context.Context, visit func(Item) error) error {
	for i := 0; i < f.Len(); i++ {
		item, err := f.Item(ctx, i)
		if err != nil {
			return err
		}
		if err := visit(item); err != nil {
			return err
		}
	}
	return ctx.Err()
}

type builder struct {
	f           *Frozen
	o           Options
	seen        map[string]Location
	sources     *commitment.SourceAccumulator
	sourceToken string
	bytes       int64
	stream      func(Item) error
	emitted     int
}

func newBuilder(o Options, sourceCount uint64) (*builder, error) {
	var key [commitment.KeyBytes]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, errors.New("collection identity key generation failed")
	}
	defer clear(key[:])
	sources, err := commitment.NewSourceAccumulator(key[:], sourceCount, uint64(o.MaxSourceBytes))
	if err != nil {
		return nil, err
	}
	dir, err := privateDirectory(o.TempDir)
	if err != nil {
		_ = sources.Close()
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "resources.jsonl"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = sources.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &builder{f: &Frozen{mu: new(sync.RWMutex), dir: dir, file: file, key: key}, o: o, seen: make(map[string]Location), sources: sources}, nil
}

func (f *Frozen) creationRequest() (api.OperationCreateRequest, error) {
	if f == nil || f.mu == nil {
		return api.OperationCreateRequest{}, errors.New("frozen collection is not initialized")
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return api.OperationCreateRequest{}, errors.New("collection is closed")
	}
	return api.OperationCreateRequest{NormalizationProfile: f.normalizationProfile, AdmissionTicket: string(f.admissionTicket), IdentityFormat: api.OperationCreateRequestIdentityFormat(commitment.Format), IdentityKey: api.Pointer(hex.EncodeToString(f.key[:])), SourceFingerprint: api.Pointer(hex.EncodeToString(f.sourceFingerprint[:])), ContentDigest: f.digest, ItemCount: int64(len(f.records))}, nil
}

// beginApply serializes the whole preparation/upload workflow without holding
// a lock across network calls or blocking Close. Read-only preflight can overlap.
func (f *Frozen) beginApply() (func(), error) {
	if f == nil || f.mu == nil {
		return nil, errors.New("frozen collection is not initialized")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errors.New("collection is closed")
	}
	if f.applying {
		return nil, ErrApplyInProgress
	}
	f.applying = true
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.applying = false
	}, nil
}

func (f *Frozen) retainAdmission(admission api.CollectionAdmission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("collection is closed")
	}
	if len(f.admissionTicket) != 0 {
		return errors.New("collection already has its original admission ticket")
	}
	if admission.ExpiresAt.IsZero() || len(admission.Ticket) == 0 || len(admission.Ticket) > 128<<10 {
		return errors.New("invalid collection admission response")
	}
	f.admissionTicket = []byte(admission.Ticket)
	return nil
}

func (b *builder) finish(ctx context.Context) error {
	fingerprint, err := b.sources.Finish()
	if err != nil {
		return err
	}
	b.f.sourceFingerprint = fingerprint
	a, err := commitment.NewAccumulator(b.f.key[:], uint64(len(b.f.records)), fingerprint)
	if err != nil {
		return err
	}
	defer a.Close()
	if err = b.f.Range(ctx, func(item Item) error {
		mac, err := hex.DecodeString(item.ContentDigest)
		if err != nil || len(mac) != commitment.MACBytes {
			return errors.New("invalid frozen item commitment")
		}
		return a.Add(item.Position, [commitment.MACBytes]byte(mac))
	}); err != nil {
		return err
	}
	digest, err := a.Finish()
	if err != nil {
		return err
	}
	b.f.digest = hex.EncodeToString(digest[:])
	b.f.staged = b.bytes
	return nil
}

// Freeze reads all inputs before returning. Any parse, duplicate, quota, or
// cancellation error removes the complete staging directory. Remote permission,
// capability, schema, and live-reference validation still require Preflight.
func Freeze(ctx context.Context, sources []Source, options Options) (_ *Frozen, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o, err := options.normalized()
	if err != nil {
		return nil, err
	}
	expanded, err := expand(ctx, sources, o.Recursive)
	if err != nil {
		return nil, err
	}
	b, err := newBuilder(o, uint64(len(expanded)))
	if err != nil {
		return nil, err
	}
	defer b.sources.Close()
	defer func() {
		if err != nil {
			_ = b.f.Close()
		}
	}()
	for index, source := range expanded {
		b.sourceToken, err = commitment.SourceToken(uint64(index + 1))
		if err != nil {
			return nil, err
		}
		if err = b.source(ctx, source); err != nil {
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

// ResourceSource supplies typed API resources without constructing a fleet-sized
// slice. io.EOF ends the source. Resource values are serialized before advancing.
type ResourceSource interface {
	Next(context.Context) (api.Resource, error)
}

// FreezeResources reads, validates, and privately stages a typed resource stream.
// It returns only after all resources have been frozen; failure removes staging.
func FreezeResources(ctx context.Context, source ResourceSource, options Options) (_ *Frozen, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fmt.Errorf("resource source is required")
	}
	o, err := options.normalized()
	if err != nil {
		return nil, err
	}
	b, err := newBuilder(o, 1)
	if err != nil {
		return nil, err
	}
	defer b.sources.Close()
	b.sourceToken, _ = commitment.SourceToken(1)
	if err = b.sources.Begin(b.sourceToken); err != nil {
		_ = b.f.Close()
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = b.f.Close()
		}
	}()
	for index := 1; ; index++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var resource api.Resource
		resource, err = source.Next(ctx)
		if errors.Is(err, io.EOF) {
			err = nil
			break
		}
		if err != nil {
			return nil, err
		}
		if err = b.add(resource, Location{Source: "resources", Document: 1, Item: index}); err != nil {
			return nil, err
		}
		raw, marshalErr := json.Marshal(resource)
		if marshalErr != nil {
			return nil, errors.New("typed source cannot be encoded")
		}
		if _, err = b.sources.Write(raw); err != nil {
			return nil, err
		}
		if _, err = b.sources.Write([]byte{'\n'}); err != nil {
			return nil, err
		}
	}
	if err = b.sources.End(); err != nil {
		return nil, err
	}
	if err = b.finish(ctx); err != nil {
		return nil, err
	}
	return b.f, nil
}

// Slice adapts a small caller-owned typed collection. FreezeResources copies its
// serialized values; callers must not mutate the slice during freezing.
func Slice(resources []api.Resource) ResourceSource { return &sliceSource{items: resources} }

type sliceSource struct {
	items []api.Resource
	next  int
}

func (s *sliceSource) Next(ctx context.Context) (api.Resource, error) {
	if err := ctx.Err(); err != nil {
		return api.Resource{}, err
	}
	if s.next == len(s.items) {
		return api.Resource{}, io.EOF
	}
	r := s.items[s.next]
	s.next++
	return r, nil
}

func (b *builder) source(ctx context.Context, source Source) error {
	r, label, err := source.open(ctx, b.o)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	defer r.Close()
	input, err := os.OpenFile(filepath.Join(b.f.dir, "source"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close(); _ = os.Remove(input.Name()) }()
	if err = b.sources.Begin(b.sourceToken); err != nil {
		return err
	}
	remaining := b.o.MaxStagingBytes - b.bytes
	n, err := io.Copy(io.MultiWriter(input, b.sources), &contextReader{ctx: ctx, r: io.LimitReader(r, remaining+1)})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s: source read failed", label)
	}
	if n > remaining {
		return fmt.Errorf("%s: collection staging quota exceeded", label)
	}
	if err = b.sources.End(); err != nil {
		return err
	}
	b.bytes += n
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(input)
	first, err := peekNonspace(reader)
	if err == io.EOF {
		b.bytes -= n
		return nil
	}
	if err != nil {
		return err
	}
	if first == '{' || first == '[' {
		err = b.parseJSON(ctx, reader, label)
	} else {
		err = b.parseYAML(ctx, reader, label)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	// Source bytes are no longer needed after all resources have been frozen.
	b.bytes -= n
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (b *builder) add(resource api.Resource, location Location) error {
	count := b.emitted
	if b.f != nil {
		count = len(b.f.records)
	}
	if count >= b.o.MaxResources {
		return fmt.Errorf("%s: resource-count limit exceeded", location)
	}
	if err := api.ValidateResource(resource); err != nil {
		return fmt.Errorf("%s: %w", location, err)
	}
	key := resource.Kind + "/" + resource.Metadata.ID
	if previous, ok := b.seen[key]; ok {
		return fmt.Errorf("duplicate %s at %s; first defined at %s", key, location, previous)
	}
	raw, err := json.Marshal(resource)
	if err != nil {
		return fmt.Errorf("%s: cannot encode resource", location)
	}
	if len(raw) > b.o.MaxResourceBytes {
		return fmt.Errorf("%s: resource exceeds %d bytes", location, b.o.MaxResourceBytes)
	}
	item := Item{ID: key, Location: location, Resource: resource}
	if b.stream != nil {
		digest := sha256.Sum256(raw)
		item.ContentDigest = hex.EncodeToString(digest[:])
		if err := b.stream(item); err != nil {
			return err
		}
		b.seen[key] = location
		b.emitted++
		return nil
	}
	return b.stage(item, raw)
}

// stage writes one resource whose exact serialized bytes have already been
// checked by add or addNormalized. It does not normalize or parse raw again.
func (b *builder) stage(item Item, raw []byte) error {
	location, key := item.Location, item.ID
	item.Position = commitment.Position{Ordinal: uint64(len(b.f.records) + 1), ID: key, Source: commitment.SourcePosition{Token: b.sourceToken, Document: uint64(location.Document), Item: uint64(location.Item)}}
	if item.Position.Source.Item == 0 {
		item.Position.Source.Item = 1
	}
	digest, err := commitment.ItemMAC(b.f.key[:], item.Position, raw)
	if err != nil {
		return fmt.Errorf("%s: invalid collection inventory position", location)
	}
	item.ContentDigest = hex.EncodeToString(digest[:])
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	defer clear(payload)
	if int64(len(payload)) > b.o.MaxStagingBytes-b.bytes {
		return fmt.Errorf("%s: collection staging quota exceeded", location)
	}
	offset, err := b.f.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err = b.f.file.Write(payload); err != nil {
		return err
	}
	b.bytes += int64(len(payload))
	b.seen[key] = location
	b.f.records = append(b.f.records, record{offset: offset, size: len(payload)})
	return nil
}
