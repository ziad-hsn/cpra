package persistence

import (
	"sync"
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

// ExecutorStatusForTest observes claim removal without repairing a retained claim.
func ExecutorStatusForTest(store *Store) LocalExecutorStatus { return store.localExecutorStatus() }

// HoldReturnedForTest delays bookkeeping after an actual executor return. It
// neither changes the recorded time nor substitutes the provider result.
func HoldReturnedForTest(t testing.TB, store *Store, monitorID, actionID string) (time.Time, func()) {
	t.Helper()
	store.executorMu.Lock()
	handle := store.executors[localExecutionKey(monitorID, actionID)]
	store.executorMu.Unlock()
	if handle == nil || handle.returnedAt.Load() == nil {
		t.Fatal("executor has not actually returned")
	}
	select {
	case handle.serial <- struct{}{}:
	case <-time.After(time.Second):
		t.Fatal("completion serialization did not become available")
	}
	var once sync.Once
	release := func() { once.Do(func() { <-handle.serial }) }
	t.Cleanup(release)
	return *handle.returnedAt.Load(), release
}
