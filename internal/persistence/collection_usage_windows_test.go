package persistence

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestCollectionGenerationUsageWindowsInitialAllocation(t *testing.T) {
	owner := generationOwnerFixture(t, filepath.Join(t.TempDir(), "collections"), uuid.NewString())
	ledger, err := openCollectionLedger(owner.directory, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	owner.track(ledger)
	if err := owner.publish(ledger); err != nil {
		t.Fatal(err)
	}
	owner.sampleUsage(context.Background())
	owner.publishStatus("", 0)
	status := owner.accounting()
	info, err := os.Stat(filepath.Join(owner.directory, ledger.generation, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 64<<20 || status.UsageCoverage != "complete" || !status.AllocationAvailable || status.KnownAllocatedBytes <= 0 || status.KnownAllocatedBytes > 65<<20 {
		t.Fatalf("initial 1 GiB quota created unexpected NTFS allocation: file=%d status=%+v", info.Size(), status)
	}
	t.Logf("NTFS completed generation: apparent=%d allocated=%d quota=%d", status.KnownApparentBytes, status.KnownAllocatedBytes, 1<<30)
}
