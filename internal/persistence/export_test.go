package persistence

import (
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

// ForceFollowerForTest sends a real higher-term vote request to the configured
// voter. The returned function restores its normal election timing.
func ForceFollowerForTest(t testing.TB, store *Store) func() {
	t.Helper()
	original := store.raft.ReloadableConfig()
	held := original
	held.HeartbeatTimeout, held.ElectionTimeout = 5*time.Second, 5*time.Second
	if err := store.raft.ReloadConfig(held); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		t.Helper()
		if err := store.raft.ReloadConfig(original); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(restore)
	_, peer := raft.NewInmemTransport("controller-recovery-peer")
	t.Cleanup(func() { _ = peer.Close() })
	address := store.transport.LocalAddr()
	peer.Connect(address, store.transport)
	request := &raft.RequestVoteRequest{
		RPCHeader: raft.RPCHeader{ProtocolVersion: 3, ID: []byte(store.nodeID), Addr: []byte(peer.LocalAddr())},
		Term:      1 << 20, LeadershipTransfer: true,
	}
	var response raft.RequestVoteResponse
	if err := peer.RequestVote(raft.ServerID(store.nodeID), address, request, &response); err != nil {
		t.Fatal(err)
	}
	if store.raft.State() == raft.Leader || store.Status().Ready {
		t.Fatal("higher-term vote did not remove leader readiness")
	}
	return restore
}

// RaftLeaderForTest observes election state independently of recorded store faults.
func RaftLeaderForTest(store *Store) bool { return store.raft.State() == raft.Leader }
