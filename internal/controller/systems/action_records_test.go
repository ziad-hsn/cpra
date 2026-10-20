package systems

import (
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

// monitorWithActionHistory is an assertion helper for retained provider facts.
// Runtime Get intentionally returns only current execution references. Tests
// of that bounded projection use Get directly; history assertions page the
// independently indexed ledger just like operator observations do.
func monitorWithActionHistory(t *testing.T, s *persistence.Store, id string) (persistence.Monitor, bool) {
	t.Helper()
	m, ok := s.Get(id)
	if !ok {
		return m, false
	}
	view, err := s.ActionSnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	m.Actions = make(map[string]persistence.Action)
	after := ""
	for {
		rows, next, err := view.PageByMonitor(id, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			m.Actions[row.ID] = row.Action
		}
		if next == "" {
			break
		}
		after = next
	}
	return m, true
}
