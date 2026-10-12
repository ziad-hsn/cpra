package persistence

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRaftDiagnosticsUseStructuredSinkWithoutPrivateText(t *testing.T) {
	t.Setenv("CPRA_ENV", "production")
	t.Setenv("CPRA_LOG_FORMAT", "json")
	t.Setenv("CPRA_LOG_SAMPLING", "false")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = previous; _ = r.Close(); _ = w.Close() })
	done := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(r); done <- data }()
	logger, err := newRaftLogger()
	if err != nil {
		t.Fatal(err)
	}
	logger.Error("snapshot could not complete", "last-index", uint64(17), "error",
		&os.PathError{Op: "open", Path: "PRIVATE_STATE_PATH", Err: os.ErrPermission},
		"configuration", map[string]string{"token": "PRIVATE_KEY"}, "address", "PRIVATE_ADDRESS")
	_ = w.Close()
	data := <-done
	var record map[string]interface{}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal("Raft diagnostics are not structured JSON", err)
	}
	if record["level"] != "error" || record["component"] != "raft" || record["last_index"] != float64(17) || record["error_code"] != "permission_denied" {
		t.Fatal("Raft severity or safe fields lost", record)
	}
	if strings.Contains(string(data), "PRIVATE_") || strings.Contains(string(data), "\x1b") {
		t.Fatal("Raft diagnostics exposed private text or terminal colour")
	}
}

func TestRaftDiagnosticsRejectInvalidLogConfiguration(t *testing.T) {
	t.Setenv("CPRA_LOG_LEVEL", "not-a-level")
	if _, err := newRaftLogger(); err == nil {
		t.Fatal("invalid logging configuration silently discarded diagnostics")
	}
}
