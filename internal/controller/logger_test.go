package controller

import (
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStructuredControllerLoggerReportsCallSitesWithoutColor(t *testing.T) {
	t.Setenv("CPRA_ENV", "development")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = previous; _ = r.Close(); _ = w.Close() })
	done := make(chan string, 1)
	go func() {
		output, _ := io.ReadAll(r)
		done <- string(output)
	}()
	InitializeLoggers(false)
	SystemLogger.debugMode = true
	SystemLogger.level = LogLevelDebug
	_, _, line, _ := runtime.Caller(0)
	SystemLogger.Info("direct caller")
	direct := "controller/logger_test.go:" + strconv.Itoa(line+1)
	adapter := &LoggerAdapter{logger: SystemLogger.withCallerSkip(1)}
	_, _, line, _ = runtime.Caller(0)
	adapter.Info("system caller")
	system := "controller/logger_test.go:" + strconv.Itoa(line+1)
	_, _, line, _ = runtime.Caller(0)
	SystemLogger.LogSystemPerformance("direct", time.Millisecond, 1)
	directPerformance := "controller/logger_test.go:" + strconv.Itoa(line+1)
	_, _, line, _ = runtime.Caller(0)
	adapter.LogSystemPerformance("system", time.Millisecond, 1)
	systemPerformance := "controller/logger_test.go:" + strconv.Itoa(line+1)
	_, _, line, _ = runtime.Caller(0)
	SystemLogger.LogComponentState(1, "direct", "updated")
	directState := "controller/logger_test.go:" + strconv.Itoa(line+1)
	_, _, line, _ = runtime.Caller(0)
	adapter.LogComponentState(1, "system", "updated")
	systemState := "controller/logger_test.go:" + strconv.Itoa(line+1)
	CloseLoggers()
	_ = w.Close()
	output := <-done
	if strings.Contains(output, "\x1b") || !strings.Contains(output, direct) || !strings.Contains(output, system) {
		t.Fatalf("incorrect controller call sites or colour in redirected output: %q", output)
	}
	for _, want := range []string{directPerformance, systemPerformance, directState, systemState} {
		if !strings.Contains(output, want) {
			t.Fatalf("helper call site %q missing from output: %q", want, output)
		}
	}
}

func TestStructuredInitializationDoesNotCreateFallbackFiles(t *testing.T) {
	t.Setenv("CPRA_ENV", "production")
	t.Chdir(t.TempDir())
	InitializeLoggers(false)
	t.Cleanup(CloseLoggers)
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("structured logging unexpectedly created a working-directory file: %v, %v", entries, err)
	}
}
