package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

func TestLeadershipClassificationDoesNotMaskPermanentFailures(t *testing.T) {
	for _, err := range []error{raft.ErrNotLeader, raft.ErrLeadershipLost, raft.ErrLeadershipTransferInProgress} {
		if !IsLeadershipUnavailable(errors.Join(ErrCommitUnconfirmed, err)) {
			t.Fatal("lost classification", err)
		}
	}
	for _, err := range []error{nil, raft.ErrRaftShutdown, context.DeadlineExceeded, ErrCollectionUnavailable, errors.New("disk write failed")} {
		if IsLeadershipUnavailable(err) {
			t.Fatal("unsafe retry classification", err)
		}
	}
}

func TestRaftLeadershipFlapDoesNotPoisonStore(t *testing.T) {
	s, err := Open(context.Background(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	configure(t, s)
	initial := s.Status().CommittedIndex
	reload := s.raft.ReloadableConfig()
	reload.HeartbeatTimeout, reload.ElectionTimeout = 2*time.Second, 2*time.Second
	if err := s.raft.ReloadConfig(reload); err != nil {
		t.Fatal(err)
	}
	_, peer := raft.NewInmemTransport("test-peer")
	t.Cleanup(func() { _ = peer.Close() })
	addr := s.transport.LocalAddr()
	peer.Connect(addr, s.transport)
	var response raft.RequestVoteResponse
	// The configured voter receives a higher term and steps down. Its durable
	// log remains the same, and it must elect itself again without reopening.
	request := &raft.RequestVoteRequest{RPCHeader: raft.RPCHeader{ProtocolVersion: 3, ID: []byte(s.nodeID), Addr: []byte("test-peer")},
		Term: 1 << 20, LeadershipTransfer: true}
	if err := peer.RequestVote(raft.ServerID(s.nodeID), addr, request, &response); err != nil {
		t.Fatal(err)
	}
	if s.raft.State() == raft.Leader || s.Status().Ready {
		t.Fatal("node did not leave leader readiness")
	}
	if _, err := s.CollectionValidationWork(context.Background(), time.Now()); !IsLeadershipUnavailable(err) {
		t.Fatal("validation work lost transient cause", err)
	}
	if _, err := s.CollectionExecutionWork(context.Background(), time.Now()); !IsLeadershipUnavailable(err) {
		t.Fatal("execution work lost transient cause", err)
	}
	if _, err := s.ObserveOperatorAuthority(context.Background(), "operator", time.Now()); !IsLeadershipUnavailable(err) {
		t.Fatal("authority lost transient cause", err)
	}
	if err := s.Flush(context.Background()); !IsLeadershipUnavailable(err) || !errors.Is(err, ErrCommitUnconfirmed) {
		t.Fatal("follower write did not retain uncertainty", err)
	}
	s.mu.RLock()
	poisoned := s.err
	s.mu.RUnlock()
	if poisoned != nil {
		t.Fatal("leadership flap poisoned storage", poisoned)
	}
	deadline := time.Now().Add(8 * time.Second)
	for !s.Status().Ready && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.Status().Ready {
		t.Fatal("same store never recovered readiness")
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal("recovered FIFO barrier", err)
	}
	if s.Status().CommittedIndex <= initial {
		t.Fatal("new leader did not commit")
	}
	if m, ok := s.Get("stable-one"); !ok || m.Revision != "r1" {
		t.Fatal("flap lost committed monitor")
	}
}
