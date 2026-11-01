package systems

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestCheckAvailabilityDoesNotDependOnRecoveryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		available bool
	}{
		{"missing_ca", &jobs.ConfigurationError{Code: "ca_file_missing"}, false},
		{"permission", os.ErrPermission, false},
		{"descriptors", syscall.EMFILE, false},
		{"target_status", &jobs.HTTPStatusError{Status: 503}, true},
		{"unclassified", errors.New("private remote error"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, rules := range []bool{false, true} {
				world := ecs.NewWorld()
				monitor := manifest.Monitor{ID: "policy-independent", Name: "Policy independent", Enabled: true, Pulse: manifest.Pulse{Type: "http", Interval: time.Second, Timeout: time.Second, UnhealthyThreshold: 1, HealthyThreshold: 1, Config: &manifest.PulseHTTPConfig{Url: "https://example.test/"}}}
				if rules {
					monitor.Intervention = manifest.Intervention{TaxonomyVersion: "1", Rules: []manifest.InterventionRule{{ID: "no-recovery", None: true, Match: manifest.FailureMatch{Codes: []string{"unexpected_http_status"}}}}}
				}
				prepared, err := entities.PrepareMonitor(monitor, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				ent, err := entities.NewEntityManager(&world).Install(prepared, &world)
				if err != nil {
					t.Fatal(err)
				}
				s := newIncidentSystem(t, &world, nil, nil)
				prior := applyIncidentPulse(t, s, ent, jobs.Result{})
				m := applyIncidentPulse(t, s, ent, jobs.Result{Err: tc.err, ExecutionStart: time.Now().UTC(), ExecutionEnd: time.Now().UTC()})
				row, _ := s.index.Get(ent.ID())
				if tc.available {
					if row.Health != "unhealthy" || !m.Incident || m.TotalChecks != prior.TotalChecks+1 {
						t.Fatal("target failure lost", rules, row.Health, m.Incident)
					}
				} else {
					if row.Health != "unknown" || row.Availability.State != "unavailable" || m.Incident || !m.LastCheck.Equal(prior.LastCheck) || m.TotalChecks != prior.TotalChecks {
						t.Fatal("unavailable check manufactured target failure", rules, row.Health, m.Incident)
					}
				}
			}
		})
	}
}
