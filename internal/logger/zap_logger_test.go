package logger_test

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"cpra/internal/logger"
)

// captureStderr runs fn with os.Stderr redirected to a pipe (zap's default
// production/development configs write there) and returns what was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

func defaultProdConfig() logger.LoggerConfig {
	cfg := logger.DefaultConfig()
	cfg.Format = "json"
	cfg.EnableSampling = false
	return cfg
}

func TestErrorProducesNoStacktraceByDefault(t *testing.T) {
	out := captureStderr(t, func() {
		l, err := logger.NewZapLogger(defaultProdConfig())
		if err != nil {
			t.Fatalf("NewZapLogger: %v", err)
		}
		l.Error("boom", logger.Field{Key: "k", Value: "v"})
	})
	if !strings.Contains(out, "boom") {
		t.Fatalf("expected error message in output, got: %q", out)
	}
	if strings.Contains(out, "stacktrace") {
		t.Fatalf("did not expect a stack trace for Error level, got: %q", out)
	}
}

func TestErrorProducesStacktraceWhenLevelIsError(t *testing.T) {
	cfg := defaultProdConfig()
	cfg.StacktraceLevel = "error"
	out := captureStderr(t, func() {
		l, err := logger.NewZapLogger(cfg)
		if err != nil {
			t.Fatalf("NewZapLogger: %v", err)
		}
		l.Error("boom2")
	})
	if !strings.Contains(out, "stacktrace") {
		t.Fatalf("expected a stack trace when stacktrace_level=error, got: %q", out)
	}
}

func TestErrorLogIsWellFormedJSON(t *testing.T) {
	out := captureStderr(t, func() {
		l, err := logger.NewZapLogger(defaultProdConfig())
		if err != nil {
			t.Fatalf("NewZapLogger: %v", err)
		}
		l.Error("json-check", logger.Field{Key: "monitor", Value: "x"})
	})
	line := strings.TrimSpace(out)
	if line == "" {
		t.Fatal("no log output captured")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("log line is not valid JSON: %v\n%s", err, line)
	}
	if m["msg"] != "json-check" {
		t.Fatalf("unexpected msg field: %v", m["msg"])
	}
	if m["monitor"] != "x" {
		t.Fatalf("structured field not encoded: %v", m["monitor"])
	}
}
