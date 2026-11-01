package persistence

import (
	"context"
	"errors"
)

// IncidentFilter applies to the frozen latest episode per monitor. It does not
// infer severity from pending notification colors or enumerate historical events.
type IncidentFilter struct {
	State, Acknowledged, Dismissed, Order string
}

func (q IncidentFilter) Valid() bool {
	return (q.State == "" || q.State == "open" || q.State == "pending" || q.State == "closed") &&
		(q.Acknowledged == "" || q.Acknowledged == "true" || q.Acknowledged == "false") &&
		(q.Dismissed == "" || q.Dismissed == "true" || q.Dismissed == "false") &&
		(q.Order == "" || q.Order == "triage" || q.Order == "id")
}
func incidentState(r IncidentRecord) string {
	if r.Active {
		return "open"
	}
	if r.ClosedAt.IsZero() {
		return "pending"
	}
	return "closed"
}
func incidentTriageLess(a, b IncidentRecord) bool {
	rank := func(r IncidentRecord) int {
		if r.Active {
			return 0
		}
		if r.ClosedAt.IsZero() {
			return 1
		}
		return 2
	}
	if rank(a) != rank(b) {
		return rank(a) < rank(b)
	}
	if a.Dismissed != b.Dismissed {
		return !a.Dismissed
	}
	if (a.AcknowledgedBy == "") != (b.AcknowledgedBy == "") {
		return a.AcknowledgedBy == ""
	}
	if !a.FirstFailureAt.Equal(b.FirstFailureAt) {
		return a.FirstFailureAt.Before(b.FirstFailureAt)
	}
	return a.ID < b.ID
}
func (q IncidentFilter) Matches(r IncidentRecord) bool {
	return (q.State == "" || q.State == incidentState(r)) &&
		(q.Acknowledged == "" || (q.Acknowledged == "true") == (r.AcknowledgedBy != "")) &&
		(q.Dismissed == "" || (q.Dismissed == "true") == r.Dismissed)
}

// FilteredPage scans at most 4096 compact records. A sparse filter can return an
// empty page with an advancing cursor; no fleet sort runs on a request path.
func (v IncidentView) FilteredPage(ctx context.Context, after string, limit int, q IncidentFilter) ([]IncidentRecord, string, error) {
	if !q.Valid() || v.tree == nil || v.triage == nil || limit < 1 || limit > 500 {
		return nil, "", errors.New("invalid incident query")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	var start IncidentRecord
	if after != "" {
		var ok bool
		start, ok = v.Get(after)
		if !ok {
			return nil, "", errors.New("invalid incident continuation")
		}
	}
	tree := v.triage
	if q.Order == "id" {
		tree = v.tree
	}
	rows := make([]IncidentRecord, 0, limit)
	scanned, last, next := 0, "", ""
	visit := func(r IncidentRecord) bool {
		if r.ID == after {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if scanned >= 4096 || len(rows) == limit {
			next = last
			return false
		}
		scanned++
		last = r.ID
		if q.Matches(r) {
			rows = append(rows, r)
		}
		return true
	}
	if after == "" {
		tree.Ascend(visit)
	} else {
		tree.AscendGreaterOrEqual(start, visit)
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return rows, next, nil
}
