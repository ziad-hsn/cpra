package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestLocalActionCLIExclusiveReviewAndNoRemoteRequests(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = directory
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var calls atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(remote.Close)
	invoke := func(args ...string) (string, error) {
		t.Helper()
		args = append([]string{"--server", remote.URL, "local"}, args...)
		args = append(args, "--data-dir", directory)
		out, _, err := runLocalAuth(t, args...)
		return out, err
	}
	if _, err := invoke("actions"); err == nil {
		t.Fatal("CLI opened a live store")
	}
	at := time.Now().UTC()
	m := persistence.Monitor{ID: "file-monitor", Name: "File monitor", Revision: "revision", Policy: persistence.Policy{Enabled: true, Interval: time.Second, Unhealthy: 1, Healthy: 1, Endpoints: map[string]int{"red": 1}}}
	apply := func(command persistence.Command) persistence.Result {
		t.Helper()
		rows, err := store.Submit(t.Context(), []persistence.Command{command})
		if err != nil || len(rows) != 1 || rows[0].Err != nil {
			t.Fatal("fixture commit", err, rows)
		}
		return rows[0]
	}
	apply(persistence.Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
	r := apply(persistence.Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: at, Outcome: "failure"})
	if len(r.Monitor.Actions) != 1 {
		t.Fatal("missing fixture action")
	}
	var id string
	for key := range r.Monitor.Actions {
		id = key
	}
	handle, err := store.BeginLocalAction(t.Context(), persistence.Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: at})
	if err != nil {
		t.Fatal(err)
	}
	apply(persistence.Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: at, Outcome: "failure", Ambiguous: true})
	if err = handle.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	read := func() localadmin.LocalAction {
		t.Helper()
		out, err := invoke("actions", "--monitor-id", m.ID, "--limit", "1")
		var page localadmin.LocalActionPage
		if err != nil || json.Unmarshal([]byte(out), &page) != nil || len(page.Items) != 1 {
			t.Fatal("CLI action page", err)
		}
		return page.Items[0]
	}
	before := read()
	for _, args := range [][]string{
		{"review", "action/" + id, "--revision", before.Revision, "--resolution", "accepted", "--reason", "Inspected receiver"},
		{"review", "action/" + id, "--revision", before.Revision, "--resolution", "accepted", "--actor", "administrator"},
	} {
		if _, err := invoke(args...); err == nil {
			t.Fatal("missing audit input accepted")
		}
	}
	if _, err := invoke("review", "action/"+id, "--revision", "stale", "--resolution", "accepted", "--actor", "administrator", "--reason", "Inspected receiver"); !errors.Is(err, persistence.ErrActionReviewConflict) {
		t.Fatal("stale observation accepted", err)
	}
	out, err := invoke("review", "action/"+id, "--revision", before.Revision, "--resolution", "accepted", "--actor", "administrator", "--reason", "Inspected receiver")
	var report localadmin.LocalActionReviewReport
	if err != nil || json.Unmarshal([]byte(out), &report) != nil || report.Outcome != "committed" {
		t.Fatal("CLI review", err)
	}
	after := read()
	if !before.Held || after.Held || after.State != persistence.Unknown || after.Outcome != before.Outcome || after.Review == nil || after.Review.Actor != "administrator" || after.Review.Reason != "Inspected receiver" || after.Revision == before.Revision {
		t.Fatal("CLI review changed provider facts or lost audit data")
	}
	if calls.Load() != 0 {
		t.Fatal("local command used remote server")
	}
}
