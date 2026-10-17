package logger_test

import (
	"encoding/json"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/logger"
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

func TestMachineTimestampsAreUTC(t *testing.T) {
	at := time.Date(2026, 10, 17, 4, 5, 6, 0, time.FixedZone("fixture", 3*60*60))
	out := captureStderr(t, func() {
		l, err := logger.NewZapLogger(defaultProdConfig())
		if err != nil {
			t.Fatal(err)
		}
		l.Info("time", logger.Field{Key: "observed_at", Value: at})
	})
	var record map[string]any
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ts", "observed_at"} {
		value, ok := record[key].(string)
		if !ok || !strings.HasSuffix(value, "Z") {
			t.Fatal("timestamp is not explicit UTC", key, record[key])
		}
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			t.Fatal(err)
		}
	}
	if record["observed_at"] != "2026-10-17T01:05:06Z" {
		t.Fatal("timestamp changed instant", record)
	}
}

func TestDevelopmentOutputHasNoANSISequences(t *testing.T) {
	for _, format := range []string{"console", "json"} {
		t.Run(format, func(t *testing.T) {
			cfg := logger.DevelopmentConfig()
			cfg.Format = format
			out := captureStderr(t, func() {
				l, err := logger.NewZapLogger(cfg)
				if err != nil {
					t.Fatal(err)
				}
				l.Info("plain level")
			})
			if strings.Contains(out, "\x1b") || strings.Contains(out, `\u001b`) {
				t.Fatalf("colour escaped into captured %s output: %q", format, out)
			}
		})
	}
}

func TestCallerIdentifiesTheApplicationLine(t *testing.T) {
	for _, derived := range []bool{false, true} {
		var want string
		out := captureStderr(t, func() {
			l, err := logger.NewZapLogger(defaultProdConfig())
			if err != nil {
				t.Fatal(err)
			}
			var log logger.Logger = l
			if derived {
				log = log.With(logger.Field{Key: "component", Value: "test"})
			}
			_, _, line, _ := runtime.Caller(0)
			log.Info("caller-check")
			want = "logger/zap_logger_test.go:" + strconv.Itoa(line+1)
		})
		var record struct {
			Caller string `json:"caller"`
		}
		if err := json.Unmarshal([]byte(out), &record); err != nil || record.Caller != want {
			t.Fatalf("caller=%q, want=%q, error=%v", record.Caller, want, err)
		}
	}
}
