package commitment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	Format                  = "cpra.collection.hmac-sha256-json-bytes.v1"
	KeyBytes                = 32
	MACBytes                = sha256.Size
	MaxResourceBytes        = 1 << 20
	MaxItems         uint64 = 10_000_000
	MaxSources       uint64 = 1_000_000
	MaxSourceBytes   uint64 = 1 << 40
)

var (
	ErrKey           = errors.New("inventory requires a 32-byte key")
	ErrBounds        = errors.New("inventory limit exceeded or invalid count")
	ErrPosition      = errors.New("invalid inventory identity or position")
	ErrState         = errors.New("invalid inventory accumulator state or order")
	ErrMismatch      = errors.New("inventory commitment does not match")
	ErrSerialization = errors.New("private inventory accumulators cannot be serialized")
)

// SourcePosition identifies a location without disclosing the local source name.
// Document and Item are one-based and bounded by MaxItems.
type SourcePosition struct {
	Token    string
	Document uint64
	Item     uint64
}

// Position identifies one input item. ID is Kind/ID; Ordinal is one-based.
type Position struct {
	Ordinal uint64
	ID      string
	Source  SourcePosition
}

// SourceToken returns the sole canonical spelling of an ordered opaque token.
func SourceToken(ordinal uint64) (string, error) {
	if ordinal == 0 || ordinal > MaxSources {
		return "", ErrPosition
	}
	digits := strconv.FormatUint(ordinal, 10)
	return "source." + strings.Repeat("0", 20-len(digits)) + digits, nil
}

func validToken(token string) bool {
	if len(token) != 27 || !strings.HasPrefix(token, "source.") {
		return false
	}
	for _, c := range token[7:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(token[7:], 10, 64)
	return err == nil && n > 0 && n <= MaxSources
}

func validPosition(p Position) bool {
	if p.Ordinal == 0 || p.Ordinal > MaxItems || !validToken(p.Source.Token) || p.Source.Document == 0 || p.Source.Document > MaxItems || p.Source.Item == 0 || p.Source.Item > MaxItems {
		return false
	}
	kind, id, ok := strings.Cut(p.ID, "/")
	if !ok || len(kind) == 0 || len(kind) > 64 || len(id) == 0 || len(id) > 256 {
		return false
	}
	for i, c := range kind {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	// Keep the shared-resource ID envelope aligned with management validID.
	// Narrower kind-specific rules (for example Monitor IDs) are schema policy.
	return utf8.ValidString(id) && id != "." && id != ".." && !strings.ContainsAny(id, "/\\?#%\x00\r\n")
}

func field(h hash.Hash, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(value)
}
func number(h hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	field(h, encoded[:])
}
func beginMAC(key []byte, domain string) hash.Hash {
	h := hmac.New(sha256.New, key)
	field(h, []byte(domain))
	return h
}
func sum(h hash.Hash) [MACBytes]byte {
	var result [MACBytes]byte
	copy(result[:], h.Sum(nil))
	return result
}

// ItemMAC commits to the exact transmitted resource-object bytes. It deliberately
// does not parse or re-encode JSON. Receivers verify before separately decoding
// and validating resource identity and semantics.
func ItemMAC(key []byte, position Position, resourceJSON []byte) ([MACBytes]byte, error) {
	if len(key) != KeyBytes {
		return [MACBytes]byte{}, ErrKey
	}
	if !validPosition(position) {
		return [MACBytes]byte{}, ErrPosition
	}
	if len(resourceJSON) == 0 || len(resourceJSON) > MaxResourceBytes {
		return [MACBytes]byte{}, ErrBounds
	}
	h := beginMAC(key, "cpra.collection.item.v1")
	number(h, position.Ordinal)
	field(h, []byte(position.ID))
	field(h, []byte(position.Source.Token))
	number(h, position.Source.Document)
	number(h, position.Source.Item)
	field(h, resourceJSON)
	return sum(h), nil
}

// VerifyItem compares a recomputed item MAC without a data-dependent comparison.
// It reports no input material in its errors.
func VerifyItem(key []byte, position Position, resourceJSON, expected []byte) error {
	actual, err := ItemMAC(key, position, resourceJSON)
	if err != nil {
		return err
	}
	if len(expected) != MACBytes || !hmac.Equal(actual[:], expected) {
		return ErrMismatch
	}
	return nil
}

// Accumulator retains constant-size MAC state, never a resource identity index.
// The zero value is invalid. Use one owner; do not copy an initialized value.
type Accumulator struct {
	mac              hash.Hash
	count, added     uint64
	finished, failed bool
}

func (Accumulator) String() string               { return "private collection inventory (key and input omitted)" }
func (a Accumulator) GoString() string           { return a.String() }
func (Accumulator) MarshalJSON() ([]byte, error) { return nil, ErrSerialization }
func (a Accumulator) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(a.String())) }

// Close discards the accumulator's MAC references. It is safe to defer and call
// repeatedly, including after Finish. It does not promise erasure of Go memory.
func (a *Accumulator) Close() error {
	if a != nil {
		a.mac = nil
		a.finished = true
	}
	return nil
}

func (a *Accumulator) fail(err error) error {
	a.failed = true
	_ = a.Close()
	return err
}

// NewAccumulator creates an ordered item inventory, including a private source
// fingerprint. An empty inventory is well-defined for local no-op identities.
func NewAccumulator(key []byte, itemCount uint64, sourceFingerprint [MACBytes]byte) (*Accumulator, error) {
	if len(key) != KeyBytes {
		return nil, ErrKey
	}
	if itemCount > MaxItems {
		return nil, ErrBounds
	}
	h := beginMAC(key, "cpra.collection.inventory.v1")
	number(h, itemCount)
	field(h, sourceFingerprint[:])
	return &Accumulator{mac: h, count: itemCount}, nil
}

// Add appends exactly the next ordinal. The stage, not this accumulator, must
// reject repeated resource IDs at different ordinals. An error invalidates this
// accumulator, so a caller cannot accidentally finalize a partially checked set.
func (a *Accumulator) Add(position Position, itemMAC [MACBytes]byte) error {
	if a == nil || a.mac == nil || a.finished || a.failed {
		return ErrState
	}
	if a.added >= a.count || position.Ordinal != a.added+1 {
		return a.fail(ErrState)
	}
	if !validPosition(position) {
		return a.fail(ErrPosition)
	}
	number(a.mac, position.Ordinal)
	field(a.mac, []byte(position.ID))
	field(a.mac, itemMAC[:])
	a.added++
	return nil
}

// Finish succeeds once, only after the declared count. A premature attempt
// invalidates the accumulator. Callers retain the returned digest themselves.
func (a *Accumulator) Finish() ([MACBytes]byte, error) {
	if a == nil || a.mac == nil || a.finished || a.failed {
		return [MACBytes]byte{}, ErrState
	}
	if a.added != a.count {
		return [MACBytes]byte{}, a.fail(ErrState)
	}
	result := sum(a.mac)
	_ = a.Close()
	return result, nil
}

// Verify finalizes this accumulator and checks the expected inventory MAC.
func (a *Accumulator) Verify(expected []byte) error {
	actual, err := a.Finish()
	if err != nil {
		return err
	}
	if len(expected) != MACBytes || !hmac.Equal(actual[:], expected) {
		return ErrMismatch
	}
	return nil
}
