package durable

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpra/internal/runtimeconfig"
)

func testConfig(t *testing.T) runtimeconfig.Config {
	t.Helper()
	c := runtimeconfig.Default()
	c.Storage.Directory = t.TempDir()
	return c
}
func testMonitor() Monitor {
	return Monitor{ID: "stable-one", Revision: "r1", Name: "label", Policy: Policy{Interval: time.Second, Unhealthy: 1, Healthy: 2, Enabled: true, Cooldown: time.Minute, RecoveryBypass: true, Endpoints: map[string]int{"red": 2, "green": 1}}}
}
func submit(t *testing.T, s *Store, commands ...Command) []Result {
	t.Helper()
	r, err := s.Submit(context.Background(), commands)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func configure(t *testing.T, s *Store) {
	t.Helper()
	m := testMonitor()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: time.Now().UTC()})
}

func TestRestartSnapshotHistoryAndPartialDelivery(t *testing.T) {
	c := testConfig(t)
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	configure(t, s)
	now := time.Now().UTC()
	submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: now, Scheduled: now, Outcome: "failure"})
	m, _ := s.Get("stable-one")
	ids := sortedActions(m.Actions)
	if len(ids) != 2 || !m.Incident {
		t.Fatalf("missing incident/outbox: %+v", m)
	}
	for _, id := range ids {
		r := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: now})
		if !r[0].Allowed {
			t.Fatal("start not admitted")
		}
	}
	submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: ids[0], At: now, Outcome: "success"})
	before, err := s.History().Page(m.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, _ = s.Get(m.ID)
	if m.Actions[ids[0]].State != Succeeded || m.Actions[ids[1]].State != Unknown {
		t.Fatalf("unsafe recovered actions: %+v", m.Actions)
	}
	after, err := s.History().Page(m.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events)+1 {
		t.Fatalf("replayed duplicate or lost history: %d -> %d", len(before.Events), len(after.Events))
	}
	for _, id := range ids {
		r := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: now.Add(time.Second)})
		if r[0].Allowed {
			t.Fatal("repeated an already started action")
		}
	}
}

func TestLockedDirectoryAndMissingIdentityFail(t *testing.T) {
	c := testConfig(t)
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Open(context.Background(), c); err == nil {
		other.Close()
		t.Fatal("two processes opened one store")
	}
	configure(t, s)
	s.Close()
	if err = os.Remove(filepath.Join(c.Storage.Directory, "identity.json")); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(context.Background(), c); err == nil {
		other.Close()
		t.Fatal("bootstrapped over existing state")
	}
}

func TestConfigurationChangeCancelsOnlyUnsent(t *testing.T) {
	c := testConfig(t)
	c.Storage.Mode = "memory"
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	configure(t, s)
	now := time.Now().UTC()
	submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: now, Outcome: "failure"})
	m, _ := s.Get("stable-one")
	ids := sortedActions(m.Actions)
	submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: ids[0], At: now})
	newConfig := testMonitor()
	newConfig.Revision = "r2"
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: "r2", Config: &newConfig, At: now})
	m, _ = s.Get(m.ID)
	if m.Actions[ids[0]].State != Unknown || m.Actions[ids[1]].State != Cancelled {
		t.Fatal(m.Actions)
	}
	for _, revision := range []string{"r1", "r2"} {
		r := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: revision, ActionID: ids[1], At: now})
		if r[0].Allowed {
			t.Fatal("redirected obsolete action")
		}
	}
}

func TestProcessCrashHelper(t *testing.T) {
	dir := os.Getenv("CPRA_CRASH_TEST_DIRECTORY")
	if dir == "" {
		return
	}
	c := runtimeconfig.Default()
	c.Storage.Directory = dir
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	configure(t, s)
	fmt.Println("COMMITTED")
	select {}
}

func TestCommittedRecordSurvivesForcedProcessTermination(t *testing.T) {
	c := testConfig(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessCrashHelper$")
	cmd.Env = append(os.Environ(), "CPRA_CRASH_TEST_DIRECTORY="+c.Storage.Directory)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan bool, 1)
	go func() {
		scan := bufio.NewScanner(out)
		for scan.Scan() {
			if scan.Text() == "COMMITTED" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child failed before commit")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("child did not commit")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if m, ok := s.Get("stable-one"); !ok || m.Revision != "r1" {
		t.Fatal("lost committed state")
	}
}

func TestIncompatibleSnapshotFailsExplicitly(t *testing.T) {
	c := testConfig(t)
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	configure(t, s)
	if err = s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	paths, err := filepath.Glob(filepath.Join(c.Storage.Directory, "snapshots", "*", "state.bin"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("snapshot files: %v %v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var i image
	if err = json.Unmarshal(data, &i); err != nil {
		t.Fatal(err)
	}
	i.Version = 999
	data, _ = json.Marshal(i)
	if err = os.WriteFile(paths[0], data, 0600); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(context.Background(), c); err == nil {
		other.Close()
		t.Fatal("corrupt snapshot accepted")
	} else if !strings.Contains(err.Error(), "snapshot") {
		t.Fatal(err)
	}
}

func TestRemovedMonitorNeverResumesOldIntent(t *testing.T) {
	c := testConfig(t)
	c.Storage.Mode = "memory"
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	configure(t, s)
	now := time.Now().UTC()
	submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: now, Outcome: "failure"})
	if err := s.Reconcile(context.Background(), map[string]bool{}, now); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Get("stable-one")
	if !m.Removed || m.Policy.Enabled {
		t.Fatal(m)
	}
	for _, a := range m.Actions {
		if a.State != Cancelled {
			t.Fatal(a)
		}
	}
	configure(t, s)
	m, _ = s.Get("stable-one")
	if m.Removed || !m.Policy.Enabled {
		t.Fatal(m)
	}
	for _, a := range m.Actions {
		if a.State == Queued {
			t.Fatal("removed configuration action resumed")
		}
	}
}

func TestHealthyResultCancelsUnsentInterventionAndLateFailureDoesNotReopen(t *testing.T) {
	now := time.Now().UTC()
	m := Monitor{ID: "one", Revision: "r", Policy: Policy{Interval: time.Second, Healthy: 1, Unhealthy: 1, Enabled: true, Intervention: true}, Actions: map[string]Action{}, Cooldowns: map[string]time.Time{}}
	result := transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: now, Outcome: "failure"})
	m = *result.Monitor
	if len(m.Actions) != 1 {
		t.Fatal(m)
	}
	m = *transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 2, At: now.Add(time.Second), Outcome: "success"}).Monitor
	if m.Recovering {
		t.Fatal("healthy recovery blocked by an unsent action")
	}
	for _, a := range m.Actions {
		if a.State != Cancelled {
			t.Fatal(a)
		}
	}
	m.VerifyRemaining = 2
	m.VerificationAfter = 4
	m.Generation = 3
	m = *transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 4, At: now.Add(2 * time.Second), Outcome: "failure"}).Monitor
	if m.Incident || m.VerifyRemaining != 2 {
		t.Fatal("pre-intervention check invalidated verification", m)
	}
}

func TestSnapshotOfHealthyMonitorCanOpenItsFirstIncident(t *testing.T) {
	c := testConfig(t)
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	configure(t, s)
	if err = s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: time.Now().UTC(), Outcome: "failure"})
	m, _ := s.Get("stable-one")
	if len(m.Actions) != 2 || !m.Incident {
		t.Fatal(m)
	}
}

func TestCompleteBackupRestoresEvents(t *testing.T) {
	c := testConfig(t)
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	configure(t, s)
	submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: time.Now().UTC(), Outcome: "failure"})
	if err = s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	page, err := s.History().Page("stable-one", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restored := t.TempDir()
	err = filepath.WalkDir(c.Storage.Directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(c.Storage.Directory, path)
		if err != nil {
			return err
		}
		target := filepath.Join(restored, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Storage.Directory = restored
	s, err = Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	recovered, err := s.History().Page("stable-one", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Events) != len(page.Events) {
		t.Fatal("backup history differs")
	}
	for n := range page.Events {
		if page.Events[n] != recovered.Events[n] {
			t.Fatal("event identity changed during backup restore")
		}
	}
	m, _ := s.Get("stable-one")
	if !m.Incident || len(m.Actions) != 2 {
		t.Fatal(m)
	}
}
