package persistence

import (
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestControllerWakeupsFollowCommittedStateAndCoalesce(t *testing.T) {
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode = "memory"
	store, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wakeups := store.ControllerWakeups()
	select {
	case <-wakeups:
	default:
	}
	for _, id := range []string{"wake-one", "wake-two"} {
		m := Monitor{ID: id, Name: id, Revision: "r1", Policy: Policy{Interval: time.Minute, Enabled: true, Healthy: 1, Unhealthy: 1}}
		results, err := store.Submit(t.Context(), []Command{{Kind: "configure", MonitorID: id, Revision: m.Revision, Config: &m, At: time.Now().UTC()}})
		if err != nil || len(results) != 1 || results[0].Err != nil {
			t.Fatal("configuration did not commit", results, err)
		}
	}
	select {
	case <-wakeups:
	default:
		t.Fatal("committed state produced no owner hint")
	}
	select {
	case <-wakeups:
		t.Fatal("hints must coalesce instead of accumulating per monitor")
	default:
	}
	if _, present := store.Get("wake-two"); !present {
		t.Fatal("wakeup replaced authoritative mutation")
	}
}
