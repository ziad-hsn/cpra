package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

func TestCollectionReceiptAndPreparationPreserveLeadershipCause(t *testing.T) {
	store, err := Open(context.Background(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	head, auth := activationFixture(t, store)
	at := head.ActivityAt.Add(time.Second)
	head = validationApplyAllowed(t, collectionCommand(t, store, activationCommand(head, auth, at), at))
	view := openExecutionPreparationView(t, store, head, auth, at)
	reloaded := store.raft.ReloadableConfig()
	reloaded.HeartbeatTimeout, reloaded.ElectionTimeout = 2*time.Second, 2*time.Second
	if err := store.raft.ReloadConfig(reloaded); err != nil {
		t.Fatal(err)
	}
	_, peer := raft.NewInmemTransport("collection-read-peer")
	defer peer.Close()
	address := store.transport.LocalAddr()
	peer.Connect(address, store.transport)
	request := &raft.RequestVoteRequest{RPCHeader: raft.RPCHeader{ProtocolVersion: 3, ID: []byte(store.nodeID), Addr: []byte("collection-read-peer")}, Term: 1 << 20, LeadershipTransfer: true}
	var response raft.RequestVoteResponse
	if err := peer.RequestVote(raft.ServerID(store.nodeID), address, request, &response); err != nil {
		t.Fatal(err)
	}
	if store.raft.State() == raft.Leader {
		t.Fatal("did not enter follower state")
	}
	checks := []struct {
		name string
		read func() error
	}{
		{"receipt", func() error { _, err := store.CollectionReceipt(context.Background(), head.ID, at); return err }},
		{"existing preparation", func() error { return view.Check(context.Background(), at) }},
		{"new preparation", func() error {
			v, err := store.CollectionExecutionPreparationView(context.Background(), head.ID, auth, head.Activation.CapabilitiesDigest, at)
			if v != nil {
				_ = v.Close()
			}
			return err
		}},
	}
	for _, check := range checks {
		if err := check.read(); !errors.Is(err, ErrCollectionUnavailable) || !IsLeadershipUnavailable(err) {
			t.Fatalf("%s erased leadership cause: %v", check.name, err)
		}
	}
	store.MarkUnavailable(errors.New("permanent storage fixture"))
	for _, check := range checks {
		if err := check.read(); !errors.Is(err, ErrCollectionUnavailable) || IsLeadershipUnavailable(err) {
			t.Fatalf("%s masked permanent storage fault: %v", check.name, err)
		}
	}
}
