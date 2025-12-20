package loader

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cpra/internal/controller/entities"

	"github.com/mlange-42/ark/ecs"
)

// TestStreamingParserBasic tests the streaming YAML parser with a small file.
func TestStreamingParserBasic(t *testing.T) {
	// Create a test YAML file
	yaml := `monitors:
  - name: test-monitor-1
    enabled: true
    pulse_check:
      type: http
      interval: 5s
      timeout: 3s
      config:
        url: http://example.com/health
  - name: test-monitor-2
    enabled: true
    pulse_check:
      type: tcp
      interval: 10s
      timeout: 5s
      config:
        host: localhost
        port: 22
  - name: test-monitor-3
    enabled: false
    pulse_check:
      type: icmp
      interval: 30s
      timeout: 2s
      config:
        host: 8.8.8.8
`

	// Write to temp file
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test_monitors.yaml")
	if err := os.WriteFile(tmpFile, []byte(yaml), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Create minimal pipeline with streaming mode
	world := ecs.NewWorld()
	em := entities.NewEntityManager(&world)

	config := DefaultPipelineConfig()
	config.StreamingMode = true // Explicit streaming
	config.Workers = 4

	pipeline := NewPipeline(&world, em, config)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stats, err := pipeline.Load(ctx, tmpFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify stats
	if stats.TotalMonitors != 3 {
		t.Errorf("Expected 3 monitors, got %d", stats.TotalMonitors)
	}
	if stats.EntitiesCreated != 3 {
		t.Errorf("Expected 3 entities created, got %d", stats.EntitiesCreated)
	}
}

// TestStreamingParserLarge tests the streaming parser with a larger synthetic file.
func TestStreamingParserLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large file test in short mode")
	}

	// Generate 10,000 monitors
	var buf bytes.Buffer
	buf.WriteString("monitors:\n")
	for i := 0; i < 10000; i++ {
		buf.WriteString(fmt.Sprintf("  - name: monitor-%04d\n", i))
		buf.WriteString("    enabled: true\n")
		buf.WriteString("    pulse_check:\n")
		buf.WriteString("      type: http\n")
		buf.WriteString("      interval: 5s\n")
		buf.WriteString("      timeout: 3s\n")
		buf.WriteString("      config:\n")
		buf.WriteString("        url: http://example.com/health\n")
	}

	// Write to temp file
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test_monitors_large.yaml")
	if err := os.WriteFile(tmpFile, buf.Bytes(), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Create pipeline with streaming mode
	world := ecs.NewWorld()
	em := entities.NewEntityManager(&world)

	config := DefaultPipelineConfig()
	config.StreamingMode = true
	config.Workers = 100

	pipeline := NewPipeline(&world, em, config)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stats, err := pipeline.Load(ctx, tmpFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify stats
	if stats.TotalMonitors != 10000 {
		t.Errorf("Expected 10000 monitors, got %d", stats.TotalMonitors)
	}
	if stats.EntitiesCreated != 10000 {
		t.Errorf("Expected 10000 entities created, got %d", stats.EntitiesCreated)
	}

	t.Logf("Loaded %d monitors in %v (%.0f/sec)", stats.EntitiesCreated, stats.LoadingTime, stats.CreationRate)
}

// TestTraditionalVsStreaming compares traditional and streaming modes.
func TestTraditionalVsStreaming(t *testing.T) {
	yaml := `monitors:
  - name: test-1
    enabled: true
    pulse_check:
      type: http
      interval: 5s
      timeout: 3s
      config:
        url: http://example.com/health
  - name: test-2
    enabled: true
    pulse_check:
      type: tcp
      interval: 10s
      timeout: 5s
      config:
        host: localhost
        port: 22
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.yaml")
	if err := os.WriteFile(tmpFile, []byte(yaml), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Test traditional mode
	t.Run("Traditional", func(t *testing.T) {
		world := ecs.NewWorld()
		em := entities.NewEntityManager(&world)

		config := DefaultPipelineConfig()
		config.StreamingMode = false
		config.Workers = 4

		pipeline := NewPipeline(&world, em, config)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		stats, err := pipeline.Load(ctx, tmpFile)
		if err != nil {
			t.Fatalf("Traditional load failed: %v", err)
		}
		if stats.EntitiesCreated != 2 {
			t.Errorf("Expected 2 entities, got %d", stats.EntitiesCreated)
		}
	})

	// Test streaming mode
	t.Run("Streaming", func(t *testing.T) {
		world := ecs.NewWorld()
		em := entities.NewEntityManager(&world)

		config := DefaultPipelineConfig()
		config.StreamingMode = true
		config.Workers = 4

		pipeline := NewPipeline(&world, em, config)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		stats, err := pipeline.Load(ctx, tmpFile)
		if err != nil {
			t.Fatalf("Streaming load failed: %v", err)
		}
		if stats.EntitiesCreated != 2 {
			t.Errorf("Expected 2 entities, got %d", stats.EntitiesCreated)
		}
	})
}

// TestProgressCallback tests that progress callback is called.
func TestProgressCallback(t *testing.T) {
	yaml := `monitors:
  - name: test-1
    enabled: true
    pulse_check:
      type: http
      interval: 5s
      timeout: 3s
      config:
        url: http://example.com/health
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.yaml")
	if err := os.WriteFile(tmpFile, []byte(yaml), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	world := ecs.NewWorld()
	em := entities.NewEntityManager(&world)

	var callbackCount int64
	config := DefaultPipelineConfig()
	config.StreamingMode = true
	config.ProgressInterval = 1 * time.Millisecond // Frequent updates for testing
	config.ProgressCallback = func(progress LoadProgress) {
		atomic.AddInt64(&callbackCount, 1)
	}

	pipeline := NewPipeline(&world, em, config)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pipeline.Load(ctx, tmpFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Should have at least one callback (final progress)
	if atomic.LoadInt64(&callbackCount) < 1 {
		t.Error("Expected progress callback to be called at least once")
	}
}

// TestProgressReporter tests the progress reporter formatting.
func TestProgressReporter(t *testing.T) {
	var buf bytes.Buffer
	reporter := NewProgressReporter(&buf, "monitors", 1000)

	// Simulate progress updates
	reporter.Update(100)
	reporter.Update(500)
	reporter.Update(1000)
	reporter.Complete()

	output := buf.String()
	if len(output) == 0 {
		t.Error("Expected progress output")
	}

	// Should contain "monitors" label
	if !bytes.Contains(buf.Bytes(), []byte("monitors")) {
		t.Error("Expected output to contain 'monitors'")
	}
}

