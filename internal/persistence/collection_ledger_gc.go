package persistence

import (
	"context"
	"errors"
	"os"
	"sync"
)

var (
	errCollectionGCUnavailable = errors.New("collection generation cleanup unavailable")
	errCollectionGCBlocked     = errors.New("collection generation cleanup requires attention")
	errCollectionGCBudget      = errors.New("collection generation cleanup step budget exhausted")
)

// collectionGCProtection is supplied by the future generation lifecycle owner
// after a completed retention-selection pass. Previous is the selected completed
// generation to retain; Pinned includes every live ledger/snapshot generation.
// These names are trusted selection inputs, not proof supplied by an HTTP caller.
type collectionGCProtection struct {
	StoreID, NodeID   string
	Current, Previous string
	Pinned            []string
}

// collectionGCGuard holds a dedicated ownership/generation guard while use runs.
// It guarantees the main Raft ownership lock remains held and protects generation
// selection and reader pins for the complete callback. It must NOT hold the
// controller or FSM mutex: use performs synchronous filesystem maintenance.
// This slice adds no Store integration, scanner, or reader-pin registration.
type collectionGCGuard func(context.Context, func(collectionGCProtection) error) error

type collectionGCStep struct {
	Generation    string
	Phase         string
	UnlinkedBytes int64 // Apparent bytes unlinked, not reclaimed physical space.
}

// collectionGCIdentity is native local evidence, never portable Raft identity.
// Mount IDs can change across boots; unverifiable intents remain untouched.
type collectionGCIdentity struct {
	DeviceMajor uint32 `json:"deviceMajor"`
	DeviceMinor uint32 `json:"deviceMinor"`
	Mount       uint64 `json:"mount"`
	Inode       uint64 `json:"inode"`
	BirthSec    int64  `json:"birthSec"`
	BirthNSec   uint32 `json:"birthNSec"`
}

type collectionRetirement struct {
	mu       sync.Mutex
	root     *os.Root
	dir      *os.File
	identity collectionGCIdentity
	guard    collectionGCGuard
	closed   bool
}

func (r *collectionRetirement) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return errors.Join(r.root.Close(), r.dir.Close())
}
