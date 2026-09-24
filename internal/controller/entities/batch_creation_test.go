package entities

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"testing"
	"time"
)

func TestBatchWithNotifications(t *testing.T) {
	w := ecs.NewWorld()
	m := NewEntityManager(&w)
	monitor := manifest.Monitor{Name: "fixture", Enabled: true, Pulse: manifest.Pulse{Type: "http", Interval: time.Second, Timeout: time.Second, Config: &manifest.PulseHTTPConfig{Url: "http://127.0.0.1"}}, Codes: manifest.Codes{"red": manifest.CodeConfig{Notify: "log", Config: &manifest.CodeNotificationLog{File: "fixture.log"}}}}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("batch path panics for normal monitor: %v", r)
		}
	}()
	if err := m.CreateEntitiesFromMonitors(&w, []manifest.Monitor{monitor}); err != nil {
		t.Fatal(err)
	}
}
