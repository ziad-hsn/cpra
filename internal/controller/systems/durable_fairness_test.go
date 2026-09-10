package systems

import (
	"cpra/internal/jobs"
	"cpra/internal/runtimeconfig"
	"github.com/mlange-42/ark/ecs"
	"testing"
)

func TestDurableResultDrainDoesNotStarveActionPipelines(t *testing.T) {
	pulse := make(chan []jobs.Result, 3)
	intervention := make(chan []jobs.Result, 1)
	code := make(chan []jobs.Result, 1)
	for n := 0; n < 3; n++ {
		pulse <- []jobs.Result{{Type: "pulse"}}
	}
	intervention <- []jobs.Result{{Type: "intervention"}}
	code <- []jobs.Result{{Type: "code"}}
	cfg := runtimeconfig.Default()
	cfg.Storage.BatchSize = 1
	s := DurableSystem{config: cfg, entities: map[string]ecs.Entity{}, results: []<-chan []jobs.Result{pulse, intervention, code}}
	for n := 0; n < 3; n++ {
		s.drain()
	}
	if len(intervention) != 0 || len(code) != 0 || len(pulse) != 2 {
		t.Fatal("continuous check results starved external-action results")
	}
}
