//go:build externaljobs

package management

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// JobTypeReadView owns one encrypted generation. HTTP authorization and retained
// snapshot quotas belong to the caller; Page performs no durable mutation.
type JobTypeReadView struct {
	catalog *Catalog
	values  []persistence.JobTypeVersion
	bytes   int64
}

func (c *Catalog) JobTypeSnapshot(ctx context.Context) (JobTypeReadView, error) {
	if err := c.readyContext(ctx); err != nil {
		return JobTypeReadView{}, err
	}
	values, encoded, err := c.store.JobTypeSnapshot(ctx)
	if err != nil {
		return JobTypeReadView{}, err
	}
	return JobTypeReadView{catalog: c, values: values, bytes: encoded + int64(len(values))*1024}, nil
}

// EstimatedBytes charges encrypted encoding and bounded descriptor overhead;
// it does not estimate total Go heap or process RSS.
func (v JobTypeReadView) EstimatedBytes() int64 { return v.bytes }

func (v JobTypeReadView) Page(ctx context.Context, kind, after string, limit int) ([]api.JobType, string, error) {
	if v.catalog == nil || kind != "JobType" || limit < 1 || limit > 500 {
		return nil, "", ErrValidation
	}
	if err := v.catalog.readyContext(ctx); err != nil {
		return nil, "", err
	}
	start := sort.Search(len(v.values), func(i int) bool { return v.values[i].Record.Key.ID > after })
	items := make([]api.JobType, 0, min(limit, 16))
	bytes := 1024
	for i := start; i < len(v.values) && len(items) < limit; i++ {
		resource, err := v.catalog.openJobType(ctx, v.values[i])
		if err != nil {
			return nil, "", err
		}
		raw, err := json.Marshal(resource)
		if err != nil {
			return nil, "", ErrUnavailable
		}
		size := len(raw) + 1
		clear(raw)
		if bytes+size > 8<<20 {
			if len(items) == 0 {
				return nil, "", ErrUnavailable
			}
			return items, items[len(items)-1].Metadata.ID, nil
		}
		bytes += size
		items = append(items, resource)
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if start+len(items) < len(v.values) {
		return items, items[len(items)-1].Metadata.ID, nil
	}
	return items, "", nil
}
