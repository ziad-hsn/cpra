package components

import (
	"testing"

	"github.com/ziad-hsn/cpra/internal/jobs"
)

func TestJobStorageCopyPreservesEndpointOrdinals(t *testing.T) {
	first := &jobs.CodeLogJob{File: "first"}
	last := &jobs.CodeLogJob{File: "last"}
	storage := &JobStorage{CodeJobs: map[string][]jobs.Job{
		"red": {nil, first, nil, last, nil}, "green": nil, "yellow": {},
	}}
	cloned := storage.Copy()
	if cloned == storage || len(cloned.CodeJobs["red"]) != 5 {
		t.Fatal("copy compacted endpoint positions")
	}
	for _, i := range []int{0, 2, 4} {
		if cloned.CodeJobs["red"][i] != nil {
			t.Fatalf("empty endpoint %d became a job", i)
		}
	}
	for _, i := range []int{1, 3} {
		if cloned.CodeJobs["red"][i] == storage.CodeJobs["red"][i] {
			t.Fatal("copy retained a borrowed job")
		}
	}
	if value, ok := cloned.CodeJobs["green"]; !ok || value != nil || cloned.CodeJobs["yellow"] == nil {
		t.Fatal("copy changed absent versus empty slot lists")
	}
	first.File = "changed"
	if cloned.CodeJobs["red"][1].(*jobs.CodeLogJob).File != "first" || cloned.CodeJobs["red"][3].(*jobs.CodeLogJob).File != "last" {
		t.Fatal("copy changed endpoint order or retained mutable input")
	}
}
