package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotificationLogLocationIsPure(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "absent-private-state-marker")
	for _, location := range []string{state, filepath.Join(state, "logs"), dir, "relative"} {
		err := ValidateNotificationLogLocation(location, state)
		if err == nil || strings.Contains(err.Error(), "private-state-marker") {
			t.Fatal("unsafe or missing path rejection", err)
		}
	}
	if err := ValidateNotificationLogLocation(filepath.Join(dir, "logs"), state); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal("static validation touched disk", err)
	}
}
