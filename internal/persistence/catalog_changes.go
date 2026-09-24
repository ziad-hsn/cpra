package persistence

import (
	"context"
	"errors"
)

const catalogChangeCapacity = 8192

// ErrCatalogChangeGap requests a new bounded catalog scan. No caller may skip a
// gap and claim its operational projection is current.
var ErrCatalogChangeGap = errors.New("catalog change cursor expired; rebuild from a fresh catalog view")

// CatalogCursor is process-local and bound to one Store. It is never a durable
// resume identity or public API cursor. Initial cursors come from CatalogSnapshot.
type CatalogCursor struct {
	owner    *machine
	epoch    uint64
	position uint64
}

func (CatalogCursor) MarshalJSON() ([]byte, error) {
	return nil, errors.New("process-local catalog cursor cannot be serialized")
}

// CatalogChange contains only identity/version metadata. The background
// reconciler obtains resource bodies from an immutable CatalogSnapshot, outside
// the Ark owner loop. Removals retain their tombstone identity for exact fencing.
type CatalogChange struct {
	Key            CatalogKey
	UID            string
	Revision       string
	Generation     uint64
	CommittedIndex uint64
	Removed        bool
}

type CatalogChanges struct {
	Changes []CatalogChange
	Next    CatalogCursor
	More    bool
}

func (f *machine) noteCatalogChange(record CatalogRecord) {
	if f.catalogChanges == nil {
		f.catalogChanges = make([]CatalogChange, catalogChangeCapacity)
	}
	f.catalogSequence++
	f.catalogChanges[(f.catalogSequence-1)%catalogChangeCapacity] = CatalogChange{
		Key: record.Key, UID: record.UID, Revision: record.Revision, Generation: record.Generation,
		CommittedIndex: record.CommittedIndex, Removed: record.Removed,
	}
}

// CatalogChangesSince copies at most 500 compact changes, including multiple
// resource writes in one Raft commit. The bounded ring is advisory: a slow reader
// gets an explicit gap and must rescan. It cannot delay commits or retain blobs.
func (s *Store) CatalogChangesSince(cursor CatalogCursor, limit int) (CatalogChanges, error) {
	return s.CatalogChangesSinceContext(context.Background(), cursor, limit)
}

// CatalogChangesSinceContext reads one catalog-change page within ctx.
func (s *Store) CatalogChangesSinceContext(ctx context.Context, cursor CatalogCursor, limit int) (CatalogChanges, error) {
	if limit < 1 || limit > 500 {
		return CatalogChanges{}, errors.New("invalid catalog change page size")
	}
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return CatalogChanges{}, err
	}
	defer unlock()
	f := s.fsm
	if cursor.owner != f || cursor.epoch != f.catalogEpoch || cursor.position > f.catalogSequence || f.catalogSequence-cursor.position > catalogChangeCapacity {
		return CatalogChanges{}, ErrCatalogChangeGap
	}
	count := min(uint64(limit), f.catalogSequence-cursor.position)
	result := CatalogChanges{Changes: make([]CatalogChange, 0, count), Next: cursor, More: count < f.catalogSequence-cursor.position}
	for range count {
		result.Changes = append(result.Changes, f.catalogChanges[result.Next.position%catalogChangeCapacity])
		result.Next.position++
	}
	if err := ctx.Err(); err != nil {
		return CatalogChanges{}, err
	}
	return result, nil
}
