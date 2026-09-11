package systems

import (
	"context"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/durable"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/web/snapshot"
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

func TestDurableFinalizeCommitsEveryBufferedResultAfterWorkersStop(t *testing.T) {
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode = "memory"
	cfg.Storage.BatchSize = 1
	store, err := durable.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	w := ecs.NewWorld()
	ent := releaseMonitor(t, &w, 1, 1)
	state := ecs.NewMap1[components.MonitorState](&w).Get(ent)
	pulse := make(chan []jobs.Result, 40)
	intervention := make(chan []jobs.Result)
	code := make(chan []jobs.Result)
	s := NewDurableSystem(&w, store, cfg, &snapshot.Holder{}, noopLogger{}, nil, nil, nil, []<-chan []jobs.Result{pulse, intervention, code}, time.Minute, false)
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Initialize(&w)
	s.StopAdmission()
	for generation := uint64(1); generation <= 40; generation++ {
		pulse <- []jobs.Result{{Type: "pulse", MonitorID: state.MonitorID, Revision: state.Revision, Generation: generation}}
	}
	close(pulse)
	close(intervention)
	close(code)
	s.Finalize(&w)
	m, ok := store.Get(state.MonitorID)
	if !ok || m.Generation != 40 || m.SuccessfulChecks != 40 {
		t.Fatalf("final buffered outcomes were lost: %+v", m)
	}
}
