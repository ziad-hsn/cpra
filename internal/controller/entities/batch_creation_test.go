package entities

import (
	"cpra/internal/loader/schema"
	"github.com/mlange-42/ark/ecs"
	"testing"
	"time"
)

func TestBatchWithNotifications(t *testing.T) {
	w := ecs.NewWorld()
	m := NewEntityManager(&w)
	monitor := schema.Monitor{Name: "fixture", Enabled: true, Pulse: schema.Pulse{Type: "http", Interval: time.Second, Timeout: time.Second, Config: &schema.PulseHTTPConfig{Url: "http://127.0.0.1"}}, Codes: schema.Codes{"red": schema.CodeConfig{Notify: "log", Config: &schema.CodeNotificationLog{File: "fixture.log"}}}}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("batch path panics for normal monitor: %v", r)
		}
	}()
	if err := m.CreateEntitiesFromMonitors(&w, []schema.Monitor{monitor}); err != nil {
		t.Fatal(err)
	}
}
