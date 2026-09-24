package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
)

func controllerFollower(t *testing.T, s *Store) {
	t.Helper()
	reload := s.raft.ReloadableConfig()
	reload.HeartbeatTimeout, reload.ElectionTimeout = 2*time.Second, 2*time.Second
	if err := s.raft.ReloadConfig(reload); err != nil {
		t.Fatal(err)
	}
	_, peer := raft.NewInmemTransport("controller-health-peer")
	t.Cleanup(func() { _ = peer.Close() })
	address := s.transport.LocalAddr()
	peer.Connect(address, s.transport)
	request := &raft.RequestVoteRequest{RPCHeader: raft.RPCHeader{ProtocolVersion: 3, ID: []byte(s.nodeID), Addr: []byte("controller-health-peer")}, Term: 1 << 20, LeadershipTransfer: true}
	var response raft.RequestVoteResponse
	if err := peer.RequestVote(raft.ServerID(s.nodeID), address, request, &response); err != nil {
		t.Fatal(err)
	}
	if s.raft.State() == raft.Leader {
		t.Fatal("node did not become a follower")
	}
}

func TestControllerHealthAndAdmissionPreserveLeadershipCause(t *testing.T) {
	s, err := Open(context.Background(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	m, guard := managedControlMonitor(t, s, "controller-health")
	view, err := s.ControlSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := s.CatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		read func() error
	}{
		{"health", s.ControllerHealth},
		{"check admission", func() error { return s.CheckCheckAdmission(m.ID, guard, m.ControlRevision, time.Now().UTC()) }},
		{"catalog admission", func() error { return s.CheckCatalogGuard(m.ID, guard) }},
		{"control snapshot", func() error { _, err := s.ControlSnapshot(); return err }},
		{"control changes", func() error { _, err := s.ControlsChangedSince(view.Cursor, 10); return err }},
	}
	for _, check := range controllerContextReads(s, m, guard, view.Cursor) {
		checks = append(checks, struct {
			name string
			read func() error
		}{"context " + check.name, func() error { return check.read(context.Background()) }})
	}
	for _, check := range checks {
		if err := check.read(); err != nil {
			t.Fatalf("healthy %s: %v", check.name, err)
		}
	}
	controllerFollower(t, s)
	for _, check := range catalogContextReads(s, CatalogKey{Kind: "Monitor", ID: m.ID}, catalog.Cursor) {
		if err := check.read(context.Background()); err != nil {
			t.Fatalf("native follower inspection %s rejected: %v", check.name, err)
		}
	}
	for _, check := range checks {
		if err := check.read(); !IsLeadershipUnavailable(err) {
			t.Fatalf("follower %s lost cause: %v", check.name, err)
		}
	}
	called := 0
	dispatch := jobs.NewDispatch(&completionRecoveryJob{execute: func() jobs.Result { called++; return jobs.Result{} }}, ecs.Entity{}, "pulse", "", 1, 0)
	dispatch.Before = func(context.Context) error {
		return s.CheckCheckAdmission(m.ID, guard, m.ControlRevision, time.Now().UTC())
	}
	if result := dispatch.Execute(); called != 0 || !result.ExecutionStart.IsZero() || !IsLeadershipUnavailable(result.Err) {
		t.Fatal("queued check crossed follower admission", called, result)
	}
	if handle, err := s.BeginLocalAction(context.Background(), Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: "not-admitted", At: time.Now().UTC()}); handle != nil || !IsLeadershipUnavailable(err) {
		t.Fatal("follower reserved an external invocation", handle, err)
	}
	// These isolated stores share only the real follower's Raft observation. No
	// running FSM or maintenance goroutine can consume the synthetic fault image.
	for _, fault := range []struct {
		name string
		set  func(*Store)
	}{
		{"store", func(s *Store) { s.err = errors.Join(errors.New("disk fault"), raft.ErrNotLeader) }},
		{"fsm", func(s *Store) { s.fsm.err = errors.Join(errors.New("history fault"), raft.ErrNotLeader) }},
		{"bootstrap", func(s *Store) { s.fsm.image.Bootstrap = &BootstrapState{Phase: "seeding"} }},
		{"restore", func(s *Store) { s.fsm.image.Restore = &RestoreState{Phase: "pending"} }},
		{"authentication reset", func(s *Store) { s.fsm.image.Authentication = &AuthenticationState{ResetRequired: true} }},
		{"administrative", func(s *Store) { s.administrative = true }},
		{"opening", func(s *Store) { s.opening = true }},
		{"closed", func(s *Store) { close(s.stop) }},
	} {
		t.Run(fault.name, func(t *testing.T) {
			faulted := &Store{stop: make(chan struct{}), fsm: &machine{}, raft: s.raft}
			fault.set(faulted)
			for _, check := range []func() error{
				faulted.ControllerHealth,
				func() error { return faulted.CheckCheckAdmission(m.ID, guard, m.ControlRevision, time.Now().UTC()) },
				func() error { return faulted.CheckCatalogGuard(m.ID, guard) },
				func() error { _, err := faulted.ControlSnapshot(); return err },
			} {
				if err := check(); err == nil || IsLeadershipUnavailable(err) {
					t.Fatalf("permanent failure became retryable: %v", err)
				}
			}
			for _, check := range controllerContextReads(faulted, m, guard, view.Cursor) {
				if err := check.read(context.Background()); err == nil || IsLeadershipUnavailable(err) {
					t.Fatalf("%s permanent failure became retryable: %v", check.name, err)
				}
			}
		})
	}
	waitBudgetCondition(t, "controller leadership recovery", func() bool { return s.ControllerHealth() == nil })
	for _, check := range checks {
		if err := check.read(); err != nil {
			t.Fatalf("recovered %s: %v", check.name, err)
		}
	}
	if err := s.raft.Shutdown().Error(); err != nil {
		t.Fatal(err)
	}
	if err := s.ControllerHealth(); err == nil || IsLeadershipUnavailable(err) || !errors.Is(err, raft.ErrRaftShutdown) {
		t.Fatal("Raft shutdown became a recoverable follower", err)
	}
}

func TestControllerHealthSupportsManifestWithoutCatalogCapabilities(t *testing.T) {
	s := openCatalogMemory(t)
	configure(t, s)
	m, _ := s.Get("stable-one")
	if err := s.ControllerHealth(); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckCheckAdmission(m.ID, nil, m.ControlRevision, time.Now().UTC()); err != nil {
		t.Fatal("legacy manifest check rejected", err)
	}
	if err := s.CheckCatalogGuard(m.ID, nil); err != nil {
		t.Fatal("legacy manifest guard rejected", err)
	}
}
