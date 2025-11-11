package streaming

import (
	"context"
	"testing"
)

// Benchmark with ORIGINAL well-tuned config (baseline)
func BenchmarkStreamingYamlParserOriginal(b *testing.B) {
	config := ParseConfig{
		BatchSize:           10000,
		BufferSize:          4 * 1024 * 1024,
		MaxMemory:           1 * 1024 * 1024 * 1024,
		StrictUnknownFields: false,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parser, err := NewStreamingYamlParser("../test.yaml", config)
		if err != nil {
			b.Fatalf("Failed to create parser: %v", err)
		}

		ctx := context.Background()
		batchChan, errorChan := parser.ParseBatches(ctx, nil)

		monitorCount := 0
		for batch := range batchChan {
			monitorCount += len(batch.Monitors)
		}

		select {
		case err := <-errorChan:
			if err != nil {
				b.Fatalf("Parse error: %v", err)
			}
		default:
		}

		if monitorCount == 0 {
			b.Fatal("No monitors parsed")
		}
	}
}

// Benchmark with OPTIMIZED config for 10K monitors
func BenchmarkStreamingYamlParserOptimized(b *testing.B) {
	config := ParseConfig{
		BatchSize:           5000,  // Smaller batches for 10K monitors = 2 batches
		BufferSize:          2 * 1024 * 1024,  // 2MB buffer
		MaxMemory:           1 * 1024 * 1024 * 1024,
		StrictUnknownFields: false,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parser, err := NewStreamingYamlParser("../test.yaml", config)
		if err != nil {
			b.Fatalf("Failed to create parser: %v", err)
		}

		ctx := context.Background()
		batchChan, errorChan := parser.ParseBatches(ctx, nil)

		monitorCount := 0
		for batch := range batchChan {
			monitorCount += len(batch.Monitors)
		}

		select {
		case err := <-errorChan:
			if err != nil {
				b.Fatalf("Parse error: %v", err)
			}
		default:
		}

		if monitorCount == 0 {
			b.Fatal("No monitors parsed")
		}
	}
}
