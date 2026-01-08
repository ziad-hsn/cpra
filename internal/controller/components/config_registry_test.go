package components

import (
	"sync"
	"testing"

	"cpra/internal/platform/loader/schema"
)

func TestConfigRegistry_GetOrAdd(t *testing.T) {
	t.Parallel()

	t.Run("BasicAdd", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		cfg := ColorCodeConfig{
			Notify:      "log",
			MaxFailures: 3,
			Dispatch:    true,
			Config:      &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}

		id := reg.GetOrAdd(cfg)
		if id == 0 {
			t.Error("expected non-zero ConfigID")
		}
	})

	t.Run("Deduplication", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		cfg1 := ColorCodeConfig{
			Notify:      "log",
			MaxFailures: 3,
			Config:      &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}
		cfg2 := ColorCodeConfig{
			Notify:      "log",
			MaxFailures: 3,
			Config:      &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}

		id1 := reg.GetOrAdd(cfg1)
		id2 := reg.GetOrAdd(cfg2)

		if id1 != id2 {
			t.Errorf("expected same ID for identical configs, got %d and %d", id1, id2)
		}
	})

	t.Run("DifferentConfigs", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		cfg1 := ColorCodeConfig{
			Notify: "log",
			Config: &schema.CodeNotificationLog{File: "/var/log/test1.log"},
		}
		cfg2 := ColorCodeConfig{
			Notify: "log",
			Config: &schema.CodeNotificationLog{File: "/var/log/test2.log"},
		}

		id1 := reg.GetOrAdd(cfg1)
		id2 := reg.GetOrAdd(cfg2)

		if id1 == id2 {
			t.Error("expected different IDs for different configs")
		}
	})

	t.Run("InvalidConfig_NilConfig", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		cfg := ColorCodeConfig{
			Notify: "log",
			Config: nil,
		}

		id := reg.GetOrAdd(cfg)
		if id != 0 {
			t.Errorf("expected 0 for invalid config, got %d", id)
		}
	})

	t.Run("InvalidConfig_EmptyNotify", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		cfg := ColorCodeConfig{
			Notify: "",
			Config: &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}

		id := reg.GetOrAdd(cfg)
		if id != 0 {
			t.Errorf("expected 0 for empty notify, got %d", id)
		}
	})

	t.Run("PagerDutyConfig", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		cfg := ColorCodeConfig{
			Notify: "pagerduty",
			Config: &schema.CodeNotificationPagerDuty{URL: "https://events.pagerduty.com"},
		}

		id := reg.GetOrAdd(cfg)
		if id == 0 {
			t.Error("expected non-zero ID for PagerDuty config")
		}
	})

	t.Run("SlackConfig", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		cfg := ColorCodeConfig{
			Notify: "slack",
			Config: &schema.CodeNotificationSlack{WebHook: "https://hooks.slack.com/services/xxx"},
		}

		id := reg.GetOrAdd(cfg)
		if id == 0 {
			t.Error("expected non-zero ID for Slack config")
		}
	})

	t.Run("DifferentDispatchFlag", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		cfg1 := ColorCodeConfig{
			Notify:   "log",
			Dispatch: false,
			Config:   &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}
		cfg2 := ColorCodeConfig{
			Notify:   "log",
			Dispatch: true,
			Config:   &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}

		id1 := reg.GetOrAdd(cfg1)
		id2 := reg.GetOrAdd(cfg2)

		if id1 == id2 {
			t.Error("expected different IDs for different dispatch flags")
		}
	})

	t.Run("DifferentMaxFailures", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		cfg1 := ColorCodeConfig{
			Notify:      "log",
			MaxFailures: 3,
			Config:      &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}
		cfg2 := ColorCodeConfig{
			Notify:      "log",
			MaxFailures: 5,
			Config:      &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}

		id1 := reg.GetOrAdd(cfg1)
		id2 := reg.GetOrAdd(cfg2)

		if id1 == id2 {
			t.Error("expected different IDs for different MaxFailures")
		}
	})
}

func TestConfigRegistry_Lookup(t *testing.T) {
	t.Parallel()

	t.Run("ValidID", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		cfg := ColorCodeConfig{
			Notify:      "log",
			MaxFailures: 3,
			Config:      &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}

		id := reg.GetOrAdd(cfg)
		retrieved, ok := reg.Lookup(id)

		if !ok {
			t.Error("expected Lookup to succeed")
		}
		if retrieved.Notify != cfg.Notify {
			t.Errorf("Notify mismatch: got %q, want %q", retrieved.Notify, cfg.Notify)
		}
		if retrieved.MaxFailures != cfg.MaxFailures {
			t.Errorf("MaxFailures mismatch: got %d, want %d", retrieved.MaxFailures, cfg.MaxFailures)
		}
	})

	t.Run("ZeroID", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		_, ok := reg.Lookup(0)
		if ok {
			t.Error("expected Lookup(0) to return ok=false")
		}
	})

	t.Run("InvalidID", func(t *testing.T) {
		reg := NewConfigRegistry(16)

		_, ok := reg.Lookup(999)
		if ok {
			t.Error("expected Lookup of non-existent ID to return ok=false")
		}
	})
}

func TestConfigRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	reg := NewConfigRegistry(64)
	var wg sync.WaitGroup
	numGoroutines := 10
	numOps := 100

	// Concurrent writes
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				cfg := ColorCodeConfig{
					Notify:      "log",
					MaxFailures: goroutineID*1000 + i,
					Config:      &schema.CodeNotificationLog{File: "/var/log/test.log"},
				}
				reg.GetOrAdd(cfg)
			}
		}(g)
	}

	// Concurrent reads
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				reg.Lookup(ConfigID(i + 1))
			}
		}()
	}

	wg.Wait()
}

func TestCodeConfig_SetConfig(t *testing.T) {
	t.Parallel()

	t.Run("ValidSetAndResolve", func(t *testing.T) {
		reg := NewConfigRegistry(16)
		c := &CodeConfig{}

		cfg := ColorCodeConfig{
			Notify:      "log",
			MaxFailures: 3,
			Config:      &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}

		c.SetConfig(ColorRed, cfg, reg)

		resolved, ok := c.Resolve(ColorRed, reg)
		if !ok {
			t.Error("expected Resolve to succeed")
		}
		if resolved.Notify != cfg.Notify {
			t.Errorf("Notify mismatch: got %q, want %q", resolved.Notify, cfg.Notify)
		}
	})

	t.Run("NilRegistry", func(t *testing.T) {
		c := &CodeConfig{}
		cfg := ColorCodeConfig{
			Notify: "log",
			Config: &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}

		// Should not panic
		c.SetConfig(ColorRed, cfg, nil)

		_, ok := c.Resolve(ColorRed, nil)
		if ok {
			t.Error("expected Resolve with nil registry to return ok=false")
		}
	})

	t.Run("InvalidColor", func(t *testing.T) {
		reg := NewConfigRegistry(16)
		c := &CodeConfig{}

		cfg := ColorCodeConfig{
			Notify: "log",
			Config: &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}

		// Should not panic
		c.SetConfig(MaxColors, cfg, reg)
		c.SetConfig(ColorNone, cfg, reg)

		_, ok := c.Resolve(MaxColors, reg)
		if ok {
			t.Error("expected Resolve for invalid color to return ok=false")
		}
	})

	t.Run("UnsetColor", func(t *testing.T) {
		reg := NewConfigRegistry(16)
		c := &CodeConfig{}

		// Color not set
		_, ok := c.Resolve(ColorRed, reg)
		if ok {
			t.Error("expected Resolve for unset color to return ok=false")
		}
	})
}

func TestNewConfigRegistry(t *testing.T) {
	t.Parallel()

	t.Run("DefaultCapacity", func(t *testing.T) {
		reg := NewConfigRegistry(0)
		if reg == nil {
			t.Error("expected non-nil registry")
		}
	})

	t.Run("NegativeCapacity", func(t *testing.T) {
		reg := NewConfigRegistry(-10)
		if reg == nil {
			t.Error("expected non-nil registry")
		}
	})

	t.Run("CustomCapacity", func(t *testing.T) {
		reg := NewConfigRegistry(1000)
		if reg == nil {
			t.Error("expected non-nil registry")
		}
	})
}

func TestDefaultConfigRegistry(t *testing.T) {
	// Note: This test uses the global default registry
	// Can't run in parallel due to global state

	reg := DefaultConfigRegistry()
	if reg == nil {
		t.Error("expected non-nil default registry")
	}

	// Should return same instance
	reg2 := DefaultConfigRegistry()
	if reg != reg2 {
		t.Error("expected same registry instance")
	}
}

func TestMakeCanonicalKey(t *testing.T) {
	t.Parallel()

	t.Run("LogConfig", func(t *testing.T) {
		cfg := ColorCodeConfig{
			Notify: "log",
			Config: &schema.CodeNotificationLog{File: "/var/log/test.log"},
		}
		key, ok := makeCanonicalKey(cfg)
		if !ok {
			t.Error("expected ok=true for valid log config")
		}
		if key.Type != "log" {
			t.Errorf("expected type 'log', got %q", key.Type)
		}
		if key.Value != "/var/log/test.log" {
			t.Errorf("expected value '/var/log/test.log', got %q", key.Value)
		}
	})

	t.Run("PagerDutyConfig", func(t *testing.T) {
		cfg := ColorCodeConfig{
			Notify: "pagerduty",
			Config: &schema.CodeNotificationPagerDuty{URL: "https://pd.example.com"},
		}
		key, ok := makeCanonicalKey(cfg)
		if !ok {
			t.Error("expected ok=true for valid pagerduty config")
		}
		if key.Type != "pagerduty" {
			t.Errorf("expected type 'pagerduty', got %q", key.Type)
		}
	})

	t.Run("SlackConfig", func(t *testing.T) {
		cfg := ColorCodeConfig{
			Notify: "slack",
			Config: &schema.CodeNotificationSlack{WebHook: "https://slack.example.com"},
		}
		key, ok := makeCanonicalKey(cfg)
		if !ok {
			t.Error("expected ok=true for valid slack config")
		}
		if key.Type != "slack" {
			t.Errorf("expected type 'slack', got %q", key.Type)
		}
	})

	t.Run("UnknownConfigType", func(t *testing.T) {
		cfg := ColorCodeConfig{
			Notify: "unknown",
			Config: &unknownConfig{},
		}
		_, ok := makeCanonicalKey(cfg)
		if ok {
			t.Error("expected ok=false for unknown config type")
		}
	})
}

// unknownConfig is a test helper for unknown config types
type unknownConfig struct{}

func (u *unknownConfig) Copy() schema.CodeNotification { return &unknownConfig{} }
func (u *unknownConfig) IsCodeNotification()           {}

func TestEqualKey(t *testing.T) {
	t.Parallel()

	key1 := canonicalKey{
		Type:        "log",
		Value:       "/var/log/test.log",
		Notify:      "log",
		MaxFailures: 3,
		Dispatch:    true,
	}

	t.Run("Equal", func(t *testing.T) {
		key2 := key1
		if !equalKey(key1, key2) {
			t.Error("expected keys to be equal")
		}
	})

	t.Run("DifferentType", func(t *testing.T) {
		key2 := key1
		key2.Type = "slack"
		if equalKey(key1, key2) {
			t.Error("expected keys with different Type to be unequal")
		}
	})

	t.Run("DifferentValue", func(t *testing.T) {
		key2 := key1
		key2.Value = "/var/log/other.log"
		if equalKey(key1, key2) {
			t.Error("expected keys with different Value to be unequal")
		}
	})

	t.Run("DifferentNotify", func(t *testing.T) {
		key2 := key1
		key2.Notify = "pagerduty"
		if equalKey(key1, key2) {
			t.Error("expected keys with different Notify to be unequal")
		}
	})

	t.Run("DifferentMaxFailures", func(t *testing.T) {
		key2 := key1
		key2.MaxFailures = 5
		if equalKey(key1, key2) {
			t.Error("expected keys with different MaxFailures to be unequal")
		}
	})

	t.Run("DifferentDispatch", func(t *testing.T) {
		key2 := key1
		key2.Dispatch = false
		if equalKey(key1, key2) {
			t.Error("expected keys with different Dispatch to be unequal")
		}
	})
}

func TestHashKey(t *testing.T) {
	t.Parallel()

	t.Run("SameKeysSameHash", func(t *testing.T) {
		key1 := canonicalKey{
			Type:        "log",
			Value:       "/var/log/test.log",
			Notify:      "log",
			MaxFailures: 3,
			Dispatch:    true,
		}
		key2 := key1

		if hashKey(key1) != hashKey(key2) {
			t.Error("expected identical keys to have same hash")
		}
	})

	t.Run("DifferentKeysDifferentHash", func(t *testing.T) {
		key1 := canonicalKey{
			Type:   "log",
			Value:  "/var/log/test1.log",
			Notify: "log",
		}
		key2 := canonicalKey{
			Type:   "log",
			Value:  "/var/log/test2.log",
			Notify: "log",
		}

		// Note: Different keys could have same hash (collision), but unlikely
		// This test just verifies the hash function works
		h1 := hashKey(key1)
		h2 := hashKey(key2)
		if h1 == h2 {
			t.Log("Warning: hash collision detected (unlikely but possible)")
		}
	})
}
