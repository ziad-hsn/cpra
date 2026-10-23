package systems

import (
	"errors"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/jobs"
)

func TestAutomaticIgnoreTransitionsReachStructuredLogs(t *testing.T) {
	world := ecs.NewWorld()
	ent := releaseMonitor(t, &world, 1, 1)
	ecs.NewMap1[components.AutoIgnore](&world).Add(ent, &components.AutoIgnore{Incidents: 2, Window: time.Hour})
	system := newIncidentSystem(t, &world, nil, nil)
	log := &lifecycleTestLogger{}
	system.logger = log
	failure := jobs.Result{Err: errors.New("fixture service failure")}
	applyIncidentPulse(t, system, ent, failure)
	applyIncidentPulse(t, system, ent, jobs.Result{})
	applyIncidentPulse(t, system, ent, failure)
	applyIncidentPulse(t, system, ent, jobs.Result{})
	applyIncidentPulse(t, system, ent, jobs.Result{})
	var transitions []lifecycleLogRecord
	for _, record := range log.records {
		if record.fields["event_type"] == "monitor_ignored" || record.fields["event_type"] == "monitor_unignored" {
			transitions = append(transitions, record)
		}
	}
	if len(transitions) != 2 {
		t.Fatal("missing or repeated transitions", transitions)
	}
	for i, cause := range []string{"incident_frequency", "verified_recovery"} {
		if transitions[i].fields["ignore_cause"] != cause || transitions[i].fields["actor"] != "system" || transitions[i].fields["control_revision"] == "" || transitions[i].fields["observed_at"] == "" {
			t.Fatal("incomplete structured transition", transitions)
		}
	}
}
