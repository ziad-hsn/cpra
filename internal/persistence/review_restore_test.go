package persistence

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

func TestReviewFactsCannotAdmitNotificationsAcrossExplicitRestore(t *testing.T) {
	for _, phase := range []string{"open", "close"} {
		t.Run(phase, func(t *testing.T) {
			config := testConfig(t)
			s := openAuthenticationAdmin(t, config)
			// The administrative opener deliberately omits the background reconciler.
			// Drive the same committed runtime commands to freeze the exact pending phase.
			s.administrative = false
			if err := s.migrateActionProjection(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CommitAuthentication(t.Context(), authenticationBootstrap()); err != nil {
				t.Fatal(err)
			}
			m := testMonitor()
			m.Policy.Intervention = true
			m.Policy.Endpoints = map[string]int{"blue": 1}
			at := time.Now().UTC()
			mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
			pulse := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at})[0]
			if pulse.Err != nil || pulse.Monitor == nil {
				t.Fatal("outage observation", pulse.Err)
			}
			m = *pulse.Monitor
			recovery := interventionAction(t, m)
			mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, At: at})
			result := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, Outcome: "failure", Ambiguous: true, At: at.Add(time.Millisecond)})[0]
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			if phase == "close" {
				m = reconcileReviewFacts(t, s, m.ID, at.Add(2*time.Millisecond))
				opening := notificationByColor(t, m, "blue", Queued)
				mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, NoticeRevision: ActionNoticeRevision(opening), At: at.Add(3 * time.Millisecond)})
				submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, Outcome: "success", At: at.Add(4 * time.Millisecond)})
				for n := 2; n <= 3; n++ {
					submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: uint64(n), Outcome: "success", At: at.Add(time.Duration(n)*time.Millisecond + 5*time.Millisecond)})
				}
			}
			pending, err := s.fsm.collections.reviewPage(reviewPrefix("fact", m.ID), "", 64)
			if err != nil || len(pending) == 0 {
				t.Fatal("fixture did not stop at pending fact", err)
			}
			persistPausedReviewSnapshot(t, s)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			// Copy the complete stopped directory, as the backup/restore boundary requires.
			destination := filepath.Join(t.TempDir(), "restored")
			copyRestoreFixture(t, config.Storage.Directory, destination)
			config.Storage.Directory = destination
			if err := MarkRestored(destination, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			admin := openAuthenticationAdmin(t, config)
			authority, err := admin.Authentication()
			if err != nil {
				t.Fatal(err)
			}
			provision := authenticationBootstrap()
			provision.Mode = "provision"
			provision.Epoch = authority.Epoch
			provision.ExpectedEpoch = authority.Epoch
			provision.ExpectedRevision = authority.Revision
			provision.Principals[0].TokenSHA256 = authenticationVerifier("new-private-restored-fixture-token")
			if _, err = admin.CommitAuthentication(t.Context(), provision); err != nil {
				t.Fatal(err)
			}
			if err = admin.Close(); err != nil {
				t.Fatal(err)
			}
			live, err := Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer live.Close()
			current := reconcileReviewFacts(t, live, m.ID, time.Now().UTC())
			if !current.RecoveryHeld() || !retainedAction(t, live, recovery.ID).Held() {
				t.Fatal("restore released unknown recovery")
			}
			pin, err := live.fsm.collections.pinActions()
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			rows, _, err := pin.Page(t.Context(), m.ID, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				a := row.Action
				if a.ReviewNotice != nil && a.ReviewNotice.Phase == phase {
					if a.State != Cancelled || a.Outcome != "explicit_restore" || !a.StartedAt.IsZero() {
						t.Fatal("old assessment gained execution permission", a.State, a.Outcome)
					}
					if a.ExecutionBudget == 0 && !time.Now().Before(a.Deadline) {
						t.Fatal("fixture accidentally tested expiry instead of restore fencing")
					}
					found = true
				}
			}
			if !found {
				t.Fatal("restore dropped condition or failed to record canceled delivery")
			}
			if err := live.Snapshot(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Freeze at a deterministic boundary before the runtime reconciler starts. The
// administrative snapshot store remains read-only; this fixture uses a separate
// runtime store and the production FSM serializer with the committed Raft index.
func persistPausedReviewSnapshot(t *testing.T, s *Store) {
	t.Helper()
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	index := frozen.(*frozenSnapshot).image.Index
	var entry raft.Log
	if err = s.log.GetLog(index, &entry); err != nil {
		t.Fatal(err)
	}
	// Read the committed configuration entry: GetConfiguration in the pinned
	// Raft release does not populate its future's configuration index.
	var configuration raft.Configuration
	var configurationIndex uint64
	for n := uint64(1); n <= index; n++ {
		var entry raft.Log
		if err = s.log.GetLog(n, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Type == raft.LogConfiguration {
			configuration, configurationIndex = raft.DecodeConfiguration(entry.Data), n
		}
	}
	if configurationIndex == 0 || len(configuration.Servers) != 1 {
		t.Fatal("fixture lacks committed single-node membership")
	}
	store, err := newSnapshotStore(s.config.Storage.Directory, s.config.Storage.SnapshotRetain, false)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := store.Create(raft.SnapshotVersionMax, index, entry.Term, configuration, configurationIndex, s.transport)
	if err != nil {
		t.Fatal(err)
	}
	if err = frozen.Persist(sink); err != nil {
		t.Fatal(err)
	}
}

// Explicit fixture persistence covers frozen administrative state without
// granting the stopped administrative snapshot store runtime sink ownership.
func persistStoreFixture(t *testing.T, s *Store) error {
	t.Helper()
	if !s.administrative {
		return s.Snapshot()
	}
	persistPausedReviewSnapshot(t, s)
	return nil
}
