//go:build externaljobs

package management

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestWorkerMaintenanceCalendarAndConfigurationIdentity(t *testing.T) {
	c, _ := testCatalog(t)
	spec := api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test"}`)}},
		Maintenance: api.Pointer([]api.MaintenanceWindow{{Cron: api.Pointer("30 1 * * *"), Duration: api.Pointer("20m"), Timezone: api.Pointer("America/New_York")}})}
	r := createResource(t, c, resource("Monitor", "calendar", spec))
	in := persistence.WorkerExecutionIntent{MonitorID: r.Metadata.ID, MonitorUID: r.Metadata.UID,
		Guard: persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: persistence.CatalogKey{Kind: "Monitor", ID: r.Metadata.ID}, UID: r.Metadata.UID, Revision: r.Metadata.ResourceVersion}}}}
	for _, fixture := range []struct {
		at, queued      string
		active, covered bool
	}{
		{"2026-11-01T05:29:59Z", "2026-11-01T05:00:00Z", false, false},
		{"2026-11-01T05:30:00Z", "2026-11-01T05:00:00Z", true, true},
		{"2026-11-01T05:50:00Z", "2026-11-01T05:00:00Z", false, true},
		{"2026-11-01T06:30:00Z", "2026-11-01T06:00:00Z", true, true},
	} {
		at, _ := time.Parse(time.RFC3339, fixture.at)
		queued, _ := time.Parse(time.RFC3339, fixture.queued)
		d, err := c.EvaluateWorkerMaintenance(t.Context(), in, queued, at)
		if err != nil || !d.Available || d.Active != fixture.active || d.SinceQueued != fixture.covered || !d.Until.After(at) {
			t.Fatalf("calendar decision at %s: %+v, %v", fixture.at, d, err)
		}
	}
	in.Guard.Conditions[0].Revision = "superseded"
	d, err := c.EvaluateWorkerMaintenance(t.Context(), in, time.Time{}, time.Now().UTC())
	if err != nil || d.Available {
		t.Fatal("substituted current configuration for original execution", err)
	}
}
