//go:build externaljobs

package persistence

import (
	"context"
	"encoding/json"
	"sort"
)

// JobTypeSnapshot captures sorted active Current encrypted descriptors under
// one cancellable metadata lock. The returned charge is their exact canonical
// JSON encoding plus four framing bytes per row, bounded by 64 MiB; it is an
// encoded-retention budget, not a Go heap estimate. Callers must reserve capacity
// before capture and account separately for their indexing/descriptor overhead.
// No immutable version inventories, decrypted bodies or transactions escape.
func (s *Store) JobTypeSnapshot(ctx context.Context) ([]JobTypeVersion, int64, error) {
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return nil, 0, err
	}
	defer unlock()
	if s.fsm.image.JobTypes == nil {
		return nil, 0, ctx.Err()
	}
	ids := make([]string, 0, len(s.fsm.image.JobTypes.Records))
	for id, state := range s.fsm.image.JobTypes.Records {
		if !state.Current.Record.Removed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	items := make([]JobTypeVersion, 0, len(ids))
	var used int64
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		v := s.fsm.image.JobTypes.Records[id].Current
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, 0, ErrJobTypeUnavailable
		}
		cost := int64(len(raw)) + 4
		if cost > MaxJobTypeTotalBytes-used {
			return nil, 0, ErrJobTypeQuota
		}
		used += cost
		items = append(items, v.Clone())
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return items, used, nil
}
