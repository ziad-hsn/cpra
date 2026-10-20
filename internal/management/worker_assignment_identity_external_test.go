//go:build externaljobs

package management

import (
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestWorkerAssignmentHistoricalIdentities(t *testing.T) {
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	in := persistence.WorkerExecutionIntent{
		ID: "execution", Revision: "intent-v1", MonitorID: "monitor", MonitorUID: "monitor-uid", MonitorRevision: "monitor-revision", ControlRevision: "control", Category: "check", Generation: 4,
		Source: persistence.CatalogKey{Kind: "Monitor", ID: "monitor"}, SourceUID: "monitor-uid", SourceRevision: "source-revision",
		JobType: persistence.JobTypeReference{JobTypeID: "job", JobTypeUID: "job-uid", Version: "v1", Revision: "job-revision", Category: "check"},
		Guard:   persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: persistence.CatalogKey{Kind: "Monitor", ID: "monitor"}, UID: "monitor-uid", Revision: "source-revision"}}}, Scheduled: at, Deadline: at.Add(time.Minute),
	}
	// These fixed digests cover the format-20 and format-30 wire projections.
	// New notification context must not change authentication of older payloads.
	for _, test := range []struct {
		schedule *persistence.WorkerScheduleTicket
		want     string
	}{
		{nil, "e259c9d0f41e46bdcf54b4fabaa6bde0d5b996aa2e42d6c1763566883522ad96"},
		{&persistence.WorkerScheduleTicket{OwnerEpoch: "owner", Epoch: at.Unix() / 5, Sequence: 4}, "708cca95168c52b492898ac015ca3ed06774c31b137ad4a09e0e9b390d5f82e6"},
	} {
		in.Schedule = test.schedule
		got, err := workerAssignmentIdentity(in)
		if err != nil || got != test.want {
			t.Fatalf("historical assignment identity changed: %s, %v", got, err)
		}
	}
}
