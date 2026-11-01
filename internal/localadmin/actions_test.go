package localadmin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestLocalActionsRequireStoppedExistingStore(t *testing.T) {
	directory := filepath.Join(backupCollectionRoot(t), "missing")
	if _, err := ReadStoppedActions(t.Context(), directory, "", "", 100); err == nil {
		t.Fatal("created missing store")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("action inspection created state", err)
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = directory
	s, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := ReadStoppedActions(t.Context(), directory, "", "", 100); err == nil {
		t.Fatal("inspected live store without exclusive ownership")
	}
	at := time.Now().UTC()
	m := persistence.Monitor{ID: "file-monitor", Name: "File monitor", Revision: "file-revision", Policy: persistence.Policy{Enabled: true, Interval: time.Second, Unhealthy: 1, Healthy: 1, Endpoints: map[string]int{"red": 1}}}
	apply := func(c persistence.Command) persistence.Result {
		t.Helper()
		rows, err := s.Submit(t.Context(), []persistence.Command{c})
		if err != nil || len(rows) != 1 || rows[0].Err != nil {
			t.Fatal("fixture commit", err, rows)
		}
		return rows[0]
	}
	apply(persistence.Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
	r := apply(persistence.Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: at, Outcome: "failure"})
	var id string
	for key := range r.Monitor.Actions {
		id = key
	}
	h, err := s.BeginLocalAction(t.Context(), persistence.Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: at})
	if err != nil {
		t.Fatal(err)
	}
	apply(persistence.Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: at, Outcome: "failure", Ambiguous: true})
	if err = h.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := ReadStoppedActions(t.Context(), directory, m.ID, "", 100)
	if err != nil || len(page.Items) != 1 || !page.Items[0].Held {
		t.Fatal("missing held action", err)
	}
	request := LocalActionReviewRequest{DataDirectory: directory, ActionID: id, ExpectedRevision: "stale", Actor: "administrator", Reason: "Inspected delivery at receiver", Resolution: "accepted"}
	if _, err = ReviewStoppedAction(t.Context(), request); !errors.Is(err, persistence.ErrActionReviewConflict) {
		t.Fatal("stale review accepted", err)
	}
	request.ExpectedRevision = page.Items[0].Revision
	report, err := ReviewStoppedAction(t.Context(), request)
	if err != nil || report.Outcome != "committed" || report.Action == nil || report.Action.State != persistence.Unknown || report.Action.Held || report.Action.Outcome != "external_outcome_unknown" {
		t.Fatal("stopped local assertion changed provider facts", err)
	}
	page, err = ReadStoppedActions(t.Context(), directory, m.ID, "", 100)
	if err != nil || len(page.Items) != 1 || page.Items[0].Review == nil || page.Items[0].Review.Revision != report.IntendedRevision {
		t.Fatal("review did not persist", err)
	}
}

func TestLocalActionsRejectAmbiguousDirectoryPaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err = os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err = os.Symlink(outside, link); err != nil {
		t.Skip("native symbolic links unavailable")
	}
	for _, path := range []string{link, link + string(filepath.Separator) + ".." + string(filepath.Separator) + "missing"} {
		if store, err := openActionStore(t.Context(), path); err == nil {
			store.Close()
			t.Fatal("accepted symbolic or non-canonical directory")
		}
	}
	if _, err = os.Stat(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatal("local inspection initialized another store")
	}
}
