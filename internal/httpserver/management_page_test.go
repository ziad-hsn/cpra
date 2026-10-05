package httpserver

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type timedResourceView struct {
	items   []api.Resource
	latency time.Duration
	calls   int
}

func (v *timedResourceView) Page(ctx context.Context, _ string, after string, limit int) ([]api.Resource, string, error) {
	v.calls++
	var items []api.Resource
	for _, resource := range v.items {
		if resource.Metadata.ID <= after {
			continue
		}
		if len(items) == limit {
			return items, items[len(items)-1].Metadata.ID, nil
		}
		if v.latency > 0 {
			select {
			case <-time.After(v.latency):
			case <-ctx.Done():
				return nil, "", ctx.Err()
			}
		}
		items = append(items, resource)
	}
	return items, "", nil
}

func TestManagementPageContinuesAfterSlowRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		view := &timedResourceView{latency: 5 * time.Second, items: []api.Resource{
			managementResource("Monitor", "a", api.MonitorSpec{}),
			managementResource("Monitor", "b", api.MonitorSpec{}),
			managementResource("Monitor", "c", api.MonitorSpec{}),
		}}
		items, after, err := managementPage(ctx, view, "Monitor", "", 100)
		if err != nil || len(items) != 1 || after != "a" || ctx.Err() != nil || view.calls != 1 {
			t.Fatalf("bounded page = %v, %q, %v; reads=%d", items, after, err, view.calls)
		}
		view.latency = 0
		rest, after, err := managementPage(ctx, view, "Monitor", after, 100)
		if err != nil || len(rest) != 2 || rest[0].Metadata.ID != "b" || rest[1].Metadata.ID != "c" || after != "" {
			t.Fatalf("continuation = %v, %q, %v", rest, after, err)
		}
	})
}

func TestManagementPagePropagatesReadFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		view := &timedResourceView{latency: 2 * time.Second, items: []api.Resource{
			managementResource("Monitor", "a", api.MonitorSpec{}),
		}}
		items, after, err := managementPage(ctx, view, "Monitor", "", 100)
		if err != context.DeadlineExceeded || len(items) != 0 || after != "" {
			t.Fatalf("failed read = %v, %q, %v", items, after, err)
		}
	})
}
