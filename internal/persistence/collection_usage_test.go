//go:build linux || darwin || windows

package persistence

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestCollectionGenerationUsageSamplesOneGenerationPerStep(t *testing.T) {
	owner := generationOwnerFixture(t, filepath.Join(t.TempDir(), "collections"), uuid.NewString())
	previous := publishGenerationFixture(t, owner)
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	current := publishGenerationFixture(t, owner)
	if status := owner.accounting(); status.UsageCoverage != "pending" || status.UsageGenerations != 0 {
		t.Fatalf("unsampled usage claimed availability: %+v", status)
	}
	for count := 1; count <= 2; count++ {
		owner.mu.Lock()
		owner.sampleUsage(context.Background())
		owner.publishStatus("", 0)
		owner.mu.Unlock()
		status := owner.accounting()
		if status.UsageGenerations != count || status.UsageExpected != 2 {
			t.Fatalf("sample exceeded one-generation bound: %+v", status)
		}
		if count == 1 && status.UsageCoverage != "partial" || count == 2 && status.UsageCoverage != "complete" {
			t.Fatalf("incorrect coverage: %+v", status)
		}
	}
	var want int64
	for _, generation := range []string{previous.generation, current.generation} {
		for _, leaf := range []string{"ledger.db", "generation.json"} {
			info, err := os.Stat(filepath.Join(owner.directory, generation, leaf))
			if err != nil {
				t.Fatal(err)
			}
			want += info.Size()
		}
	}
	status := owner.accounting()
	if status.KnownApparentBytes != want || !status.AllocationAvailable || status.KnownAllocatedBytes <= 0 || status.UsageOldestAt.IsZero() {
		t.Fatalf("file metadata does not agree with observation: %+v, apparent %d", status, want)
	}
	// State reads return the last sample and perform no filesystem work.
	if err := os.Remove(filepath.Join(owner.directory, previous.generation, "generation.json")); err != nil {
		t.Fatal(err)
	}
	if cached := owner.accounting(); cached != status {
		t.Fatal("status read refreshed filesystem data")
	}
	owner.mu.Lock()
	owner.sampleUsage(context.Background())
	owner.publishStatus("", 0)
	owner.mu.Unlock()
	if partial := owner.accounting(); partial.UsageCoverage != "partial" || partial.UsageGenerations != 1 || partial.KnownApparentBytes >= want {
		t.Fatalf("unreadable generation reported complete: %+v", partial)
	}
}

func TestCollectionGenerationUsageDoesNotAllocateEmptyCatalog(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "collections")
	owner := generationOwnerFixture(t, directory, uuid.NewString())
	if err := owner.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := owner.accounting(); status.UsageCoverage != "complete" || status.KnownApparentBytes != 0 || status.UsageExpected != 0 {
		t.Fatalf("empty accounting: %+v", status)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("empty usage created storage", err)
	}
}

func TestCollectionGenerationUsageExcludesUncataloguedFilesAndRetiredSamples(t *testing.T) {
	owner := generationOwnerFixture(t, filepath.Join(t.TempDir(), "collections"), uuid.NewString())
	previous := publishGenerationFixture(t, owner)
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	_ = publishGenerationFixture(t, owner)
	for range 2 {
		owner.sampleUsage(context.Background())
	}
	owner.publishStatus("", 0)
	before := owner.accounting()
	if err := os.WriteFile(filepath.Join(owner.directory, "unknown-file"), make([]byte, 1<<20), 0600); err != nil {
		t.Fatal(err)
	}
	owner.sampleUsage(context.Background())
	owner.publishStatus("", 0)
	if status := owner.accounting(); status.KnownApparentBytes != before.KnownApparentBytes || status.UsageCoverage != "complete" {
		t.Fatal("unknown data entered the completed-generation subtotal", status)
	}
	owner.catalog.Completed = []string{owner.catalog.Current}
	owner.publishStatus("", 0)
	if status := owner.accounting(); status.UsageExpected != 1 || status.UsageGenerations != 1 || status.KnownApparentBytes >= before.KnownApparentBytes || len(owner.usage) != 1 {
		t.Fatal("retired sample remained in accounting", status)
	}
	if _, err := os.Stat(filepath.Join(owner.directory, "unknown-file")); err != nil {
		t.Fatal("sampling changed unknown contents", err)
	}
}
