package streaming

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
	
	"cpra/internal/loader/schema"
)

// StreamingJsonParser handles streaming JSON parsing with batching
type StreamingJsonParser struct {
	filename     string
	batchSize    int
	bufferSize   int
	
	// Progress tracking
	totalBytes   int64
	processedBytes int64
	
	// Statistics
	startTime    time.Time
	entitiesRead int64
	
	mu sync.RWMutex // Protects statistics only
}

// NewStreamingJsonParser creates a new streaming JSON parser
func NewStreamingJsonParser(filename string, config ParseConfig) (*StreamingJsonParser, error) {
	// Get file size for progress tracking
	fileInfo, err := os.Stat(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	
	return &StreamingJsonParser{
		filename:   filename,
		batchSize:  config.BatchSize,
		bufferSize: config.BufferSize,
		totalBytes: fileInfo.Size(),
		startTime:  time.Now(),
	}, nil
}

// ParseBatches streams JSON file and returns batches of monitors
func (p *StreamingJsonParser) ParseBatches(ctx context.Context, progressChan chan<- Progress) (<-chan MonitorBatch, <-chan error) {
	batchChan := make(chan MonitorBatch, 1000) // Larger buffer for high throughput
	errorChan := make(chan error, 1)
	
	go func() {
		defer close(batchChan)
		defer close(errorChan)
		
		if err := p.parseFile(ctx, batchChan, progressChan); err != nil {
			errorChan <- err
		}
	}()
	
	return batchChan, errorChan
}

// parseFile performs the actual JSON file parsing
func (p *StreamingJsonParser) parseFile(ctx context.Context, batchChan chan<- MonitorBatch, progressChan chan<- Progress) error {
	file, err := os.Open(p.filename)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Create buffered reader for efficient I/O
	reader := bufio.NewReaderSize(file, p.bufferSize)

	// JSON parsing is MUCH faster than YAML - parse entire manifest
	fmt.Printf("DEBUG: Starting to parse JSON file...\n")
	var manifest schema.Manifest
	decoder := json.NewDecoder(reader)
	err = decoder.Decode(&manifest)
	if err != nil {
		return fmt.Errorf("failed to decode JSON manifest: %w", err)
	}

	fmt.Printf("DEBUG: Loaded JSON manifest with %d monitors\n", len(manifest.Monitors))

	// Process monitors with MAXIMUM concurrency for JSON speed
	totalMonitors := len(manifest.Monitors)

	// Progress reporting ticker
	progressTicker := time.NewTicker(100 * time.Millisecond) // Faster updates for JSON
	defer progressTicker.Stop()

	// Process with 100 goroutines handling 10k monitors each
	const maxGoroutines = 100
	const chunkSize = 10000
	sem := make(chan struct{}, maxGoroutines)

	for i := 0; i < totalMonitors; i += chunkSize {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-progressTicker.C:
			p.reportProgress(progressChan)
		default:
		}

		// Calculate end index for this chunk
		end := i + chunkSize
		if end > totalMonitors {
			end = totalMonitors
		}

		// Process large chunk concurrently
		sem <- struct{}{} // Acquire semaphore
		go func(chunkStart, chunkEnd int) {
			defer func() { <-sem }() // Release semaphore

			// Process this chunk in smaller batches for ECS
			for j := chunkStart; j < chunkEnd; j += p.batchSize {
				batchEnd := j + p.batchSize
				if batchEnd > chunkEnd {
					batchEnd = chunkEnd
				}

				// Direct slice reference for speed (JSON data is already in memory)
				batchMonitors := manifest.Monitors[j:batchEnd]

				// Send batch (non-blocking)
				select {
				case batchChan <- MonitorBatch{
					Monitors: batchMonitors,
					BatchID:  j / p.batchSize,
					Offset:   int64(j),
				}:
					// Update stats for this batch
					p.mu.Lock()
					p.entitiesRead += int64(len(batchMonitors))
					p.mu.Unlock()
				case <-ctx.Done():
					return
				}
			}
		}(i, end)

		// No delay needed for JSON - it's fast enough
	}

	// Wait for all goroutines to complete
	for i := 0; i < maxGoroutines; i++ {
		sem <- struct{}{}
	}

	// Final progress report
	p.reportProgress(progressChan)
	return nil
}

// reportProgress sends progress update
func (p *StreamingJsonParser) reportProgress(progressChan chan<- Progress) {
	if progressChan == nil {
		return
	}
	
	p.mu.RLock()
	entitiesRead := p.entitiesRead
	p.mu.RUnlock()
	
	elapsed := time.Since(p.startTime)
	rate := float64(entitiesRead) / elapsed.Seconds()
	
	// Better progress tracking for JSON
	var percentage float64
	if entitiesRead > 0 {
		percentage = float64(entitiesRead) / 1000000.0 * 100 // Assume 1M monitors
		if percentage > 100 {
			percentage = 100
		}
	}
	
	select {
	case progressChan <- Progress{
		EntitiesProcessed:  entitiesRead,
		TotalBytes:        p.totalBytes,
		ProcessedBytes:    int64(float64(p.totalBytes) * percentage / 100),
		Percentage:        percentage,
		Rate:             rate,
		EstimatedRemaining: time.Duration(0), // JSON is so fast, no meaningful estimate
	}:
	default:
		// Don't block if channel is full
	}
}

// GetStats returns current parsing statistics
func (p *StreamingJsonParser) GetStats() (entitiesRead int64, rate float64) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	elapsed := time.Since(p.startTime)
	return p.entitiesRead, float64(p.entitiesRead) / elapsed.Seconds()
}