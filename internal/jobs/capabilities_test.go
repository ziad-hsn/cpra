package jobs

import (
	"runtime"
	"testing"
)

func TestCompiledCapabilityInventory(t *testing.T) {
	list := Capabilities()
	if len(list) != 33 {
		t.Fatalf("inventory=%d", len(list))
	}
	seen := map[string]bool{}
	for _, c := range list {
		key := c.Kind + "/" + c.Driver
		if seen[key] {
			t.Fatal("duplicate", key)
		}
		seen[key] = true
		if err := ValidateDriver(c.Kind, c.Driver); (err == nil) != c.Available {
			t.Fatalf("inconsistent capability %s: %v", key, err)
		}
		if c.Driver == "systemd" && runtime.GOOS != "linux" && c.Available {
			t.Fatal("non-Linux systemd enabled")
		}
	}
	list[0].Available = false
	if !Capabilities()[0].Available {
		t.Fatal("caller mutated global capability inventory")
	}
	if ValidateDriver("check", "not-a-driver") == nil {
		t.Fatal("unknown driver accepted")
	}
}
