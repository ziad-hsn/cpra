package persistence

import (
	"fmt"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestDispatchUpgradeFromRecoveryRulesStore(t *testing.T) {
	testDispatchStoreUpgrade(t, RecoveryRulesFormatVersion)
}

// Explicit retained envelopes exercise the real Raft log and snapshot readers.
// The fixture never constructs a scheduler, provider client or executable job.
func testDispatchStoreUpgrade(t *testing.T, format int) {
	t.Helper()
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprintf("format-%d/snapshot-%t", format, snapshot), func(t *testing.T) {
			cfg := testConfig(t)
			s, err := openReplayStore(t, cfg)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC()
			originals := make(map[string]Action)
			for _, state := range []string{"queued", "started", "unknown"} {
				m := ruleMonitor(t, 1)
				m.ID = "retained-" + state
				apply := func(c Command) {
					c.MonitorID, c.Revision = m.ID, m.Revision
					r := applyCatalogFormat(t, s, format, c)[0]
					if r.Err != nil || r.Monitor == nil {
						t.Fatal("retained command", c.Kind, r.Err)
					}
					m = *r.Monitor
				}
				apply(Command{Kind: "configure", Config: &m, At: at})
				failure, _ := api.CanonicalFailure("1", "network.connection_refused", "connect")
				apply(Command{Kind: "pulse", At: at.Add(time.Millisecond), Generation: 1, Outcome: "failure", Failure: CheckFailure(failure)})
				a := queuedRuleAction(t, m)
				if state != "queued" {
					apply(Command{Kind: "start", ActionID: a.ID, Driver: "webhook", At: at.Add(2 * time.Millisecond)})
				}
				if state == "unknown" {
					apply(Command{Kind: "result", ActionID: a.ID, At: at.Add(3 * time.Millisecond), Outcome: "failure", Ambiguous: true})
				}
				record, found, err := s.Action(a.ID)
				if err != nil || !found || record.ExecutionBudget != 0 || record.Deadline.IsZero() || m.ActionVersion != ActionFormatVersion {
					t.Fatal("fixture did not retain original timing/format", err)
				}
				originals[m.ID] = record.Action
			}
			if s.fsm.image.Version != format {
				t.Fatal("fixture upgraded prematurely")
			}
			if snapshot {
				persistPausedReviewSnapshot(t, s)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			stopped, err := OpenAdministrative(t.Context(), cfg)
			if err != nil {
				t.Fatal("retained offline reader", err)
			}
			if stopped.fsm.image.Version != format {
				t.Fatal("inspection rewrote retained format")
			}
			if err := stopped.Close(); err != nil {
				t.Fatal(err)
			}
			for round := range 2 {
				current, err := Open(t.Context(), cfg)
				if err != nil {
					t.Fatal("current recovery", err)
				}
				for monitorID, old := range originals {
					r, found, err := current.Action(old.ID)
					if err != nil || !found || r.ID != old.ID || r.Revision != old.Revision || r.Rule != old.Rule || r.Attempt != old.Attempt || r.ExecutionBudget != 0 || r.Deadline != old.Deadline || r.StartedAt != old.StartedAt || r.CatalogUID != old.CatalogUID || r.FileIncarnation != old.FileIncarnation {
						t.Fatal("recovery changed retained authorization", monitorID, err)
					}
					if old.State == Started || old.State == Unknown {
						if r.State != Unknown || !r.Held {
							t.Fatal("recovery released uncertain effect", monitorID)
						}
					} else if r.State != Queued {
						t.Fatal("unexpired historical queue disappeared", r.State)
					}
					m, _ := current.Get(monitorID)
					if m.recoveryAttempts() != 1 || !actionStateVersion(m.ActionVersion) {
						t.Fatal("attempts or migration version changed", monitorID)
					}
				}
				if round == 0 {
					if err := current.Snapshot(); err != nil {
						t.Fatal(err)
					}
				}
				if err := current.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
