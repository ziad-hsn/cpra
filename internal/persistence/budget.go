package persistence

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Bound the encoded Raft unit independently of its command count. Ciphertext
	// is base64 in JSON, so a one-MiB resource costs more than one MiB on the log.
	maxCommitBytes        = 4 << 20
	maxPendingCommitBytes = 32 << 20
)

// CommandLimits describes the admission limits of this initialized store.
// Callers staging a large bootstrap can build bounded batches without depending
// on private runtime configuration or assuming the default command count.
func (s *Store) CommandLimits() (maxCommands, maxEncodedBytes int) {
	return s.config.Storage.BatchSize, maxCommitBytes
}

// CommandEncodedBound returns the same conservative wire-size bound used during
// Submit. Sum these bounds plus 64 bytes for the versioned envelope when batching.
// This checks size only; Submit still validates every command and precondition.
func CommandEncodedBound(command Command) (int, error) { return encodedBound(command) }

type commitBudget struct {
	mu      sync.Mutex
	used    int
	changed chan struct{}
}

func (b *commitBudget) acquire(ctx context.Context, stop <-chan struct{}, size int) error {
	if size < 1 || size > maxCommitBytes {
		return errors.New("durable submission exceeds encoded byte limit")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stop:
			return errors.New("durable store closed")
		default:
		}
		b.mu.Lock()
		if b.changed == nil {
			b.changed = make(chan struct{})
		}
		if b.used+size <= maxPendingCommitBytes {
			b.used += size
			b.mu.Unlock()
			return nil
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stop:
			return errors.New("durable store closed")
		case <-changed:
		}
	}
}

func (b *commitBudget) release(size int) {
	if size == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if size < 0 || size > b.used {
		panic("durable commit budget released without a reservation")
	}
	b.used -= size
	if b.changed != nil {
		close(b.changed)
	}
	b.changed = make(chan struct{})
}

// encodedBound walks the fixed Command wire types before cloning or marshaling
// any payload. The conservative bound limits encoder scratch allocations as
// well as queued bytes; encoding/json alone buffers before writing to a Writer.
// The inputs contain no interfaces or recursive types. Future wire types must
// extend this check explicitly rather than bypass admission accounting.
func encodedBound(value any) (int, error) {
	remaining := maxCommitBytes
	if !consumeJSON(reflect.ValueOf(value), &remaining) {
		return 0, errors.New("durable submission exceeds encoded byte limit")
	}
	return maxCommitBytes - remaining, nil
}

var jsonTimeType = reflect.TypeFor[time.Time]()

func consumeJSON(v reflect.Value, remaining *int) bool {
	use := func(n int) bool {
		if n < 0 || n > *remaining {
			return false
		}
		*remaining -= n
		return true
	}
	if !v.IsValid() {
		return use(4)
	}
	if v.Type() == jsonTimeType {
		return use(64) // More than the longest valid quoted RFC3339Nano value.
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return use(4)
		}
		return consumeJSON(v.Elem(), remaining)
	case reflect.String:
		if !use(2) {
			return false
		}
		for _, c := range v.String() {
			n := 1
			switch {
			case c == '"' || c == '\\':
				n = 2
			case c < 0x20 || c == '<' || c == '>' || c == '&' || c > 0x7f:
				n = 6 // Bounds HTML escaping, invalid UTF-8 and Unicode separators.
			}
			if !use(n) {
				return false
			}
		}
		return true
	case reflect.Bool:
		return use(5)
	case reflect.Float32, reflect.Float64:
		// SLO pause exposure uses fractional seconds. encoding/json rejects
		// nonfinite values; all finite float32/64 decimal or exponent forms
		// fit in this conservative allowance, including subnormals.
		value := v.Float()
		return !math.IsNaN(value) && !math.IsInf(value, 0) && use(32)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var scratch [20]byte
		return use(len(strconv.AppendInt(scratch[:0], v.Int(), 10)))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var scratch [20]byte
		return use(len(strconv.AppendUint(scratch[:0], v.Uint(), 10)))
	case reflect.Array, reflect.Slice:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return use(4)
		}
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			if v.Len() > maxCommitBytes {
				return false
			}
			return use(2 + (v.Len()+2)/3*4)
		}
		if !use(2 + v.Len()) {
			return false
		}
		for i := 0; i < v.Len(); i++ {
			if !consumeJSON(v.Index(i), remaining) {
				return false
			}
		}
		return true
	case reflect.Map:
		if v.IsNil() {
			return use(4)
		}
		if v.Type().Key().Kind() != reflect.String || !use(2+2*v.Len()) {
			return false
		}
		iterator := v.MapRange()
		for iterator.Next() {
			if !consumeJSON(iterator.Key(), remaining) || !consumeJSON(iterator.Value(), remaining) {
				return false
			}
		}
		return true
	case reflect.Struct:
		if !use(2) {
			return false
		}
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if !field.IsExported() {
				// encoding/json traverses exported children of private anonymous
				// value structs. Charge them too before any encoder allocation.
				if field.Anonymous && field.Type.Kind() == reflect.Struct && field.Tag.Get("json") != "-" {
					if !consumeJSON(v.Field(i), remaining) {
						return false
					}
				}
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			// Keeping omitted fields in the bound is intentionally conservative.
			if !use(4+len(name)) || !consumeJSON(v.Field(i), remaining) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
