package cpra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"net/http"
	"testing"
)

func TestIncidentIteratorFollowsAdvancingEmptyPages(t *testing.T) {
	calls := 0
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "open" {
			t.Error("filter lost")
		}
		if calls > 0 && r.URL.Query().Get("cursor") != fmt.Sprint(calls) {
			t.Error("cursor lost")
		}
		calls++
		page := api.IncidentList{Items: []api.Incident{}, NextCursor: fmt.Sprint(calls)}
		if calls == 4 {
			page.NextCursor = ""
			page.Items = []api.Incident{{MonitorID: "matched"}}
		}
		_ = json.NewEncoder(w).Encode(page)
	}, nil)
	it := c.Incidents.Iterate(ListOptions{IncidentState: "open"})
	if calls != 0 || !it.Next(t.Context()) || it.Value().MonitorID != "matched" || calls != 4 {
		t.Fatal("sparse iteration", calls, it.Err())
	}
	if it.Next(t.Context()) || it.Err() != nil || calls != 4 {
		t.Fatal("terminal page", it.Err())
	}
}

func TestIncidentIteratorBoundsEmptyPagesAndHonorsCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		it := Iterator[int]{allowEmptyPages: true, fetch: func(context.Context, string) ([]int, string, error) {
			calls++
			if cancelled {
				cancel()
			}
			return nil, fmt.Sprint(calls), nil
		}}
		if it.Next(ctx) || it.Err() == nil {
			t.Fatal("unbounded empty iteration")
		}
		if cancelled && (calls != 1 || !errors.Is(it.Err(), context.Canceled)) || !cancelled && calls != 1024 {
			t.Fatal(calls, it.Err())
		}
		cancel()
	}
}
