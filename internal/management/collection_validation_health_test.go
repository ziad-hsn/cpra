package management

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestValidationHealthDoesNotRetryPermanentCatalogFailure(t *testing.T) {
	fixture, _ := candidateFixture(t, collectionMonitor("service", "http://original.example/health"), nil)
	fixture.catalog.failed.Store(true)
	err := fixture.catalog.collectionValidationHealth(context.Background(), time.Now())
	if !errors.Is(err, ErrUnavailable) || persistence.IsLeadershipUnavailable(err) {
		t.Fatal("catalog failure classified as temporary", err)
	}
}

func TestValidationHealthDoesNotRetryRecordedStorageFailure(t *testing.T) {
	fixture, _ := candidateFixture(t, collectionMonitor("service", "http://original.example/health"), nil)
	// Once explicitly recorded as unavailable, even a joined historical cause
	// cannot permit the validation path to restart the catalog.
	fixture.store.MarkUnavailable(errors.Join(errors.New("permanent failure"), raft.ErrNotLeader))
	err := fixture.catalog.collectionValidationHealth(context.Background(), time.Now())
	if !errors.Is(err, ErrUnavailable) || persistence.IsLeadershipUnavailable(err) {
		t.Fatal("recorded storage failure classified as temporary", err)
	}
}
