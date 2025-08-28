package streaming

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sync"
	"time"
	
	"gopkg.in/yaml.v3"
	"cpra/internal/loader/schema"
)

// StreamingYamlParser handles streaming YAML parsing with batching
type StreamingYamlParser struct {
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

// MonitorBatch represents a batch of monitors
type MonitorBatch struct {
	Monitors []schema.Monitor
	BatchID  int
	Offset   int64
}

// ParseConfig holds parsing configuration
type ParseConfig struct {
	BatchSize    int           // Number of monitors per batch
	BufferSize   int           // Read buffer size in bytes
	MaxMemory    int64         // Maximum memory usage
	ProgressChan chan<- Progress // Progress reporting channel
}

// Progress represents parsing progress
type Progress struct {
	EntitiesProcessed int64
	TotalBytes       int64
	ProcessedBytes   int64
	Percentage       float64
	Rate             float64 // entities per second
	EstimatedRemaining time.Duration
}

// NewStreamingYamlParser creates a new streaming parser
func NewStreamingYamlParser(filename string, config ParseConfig) (*StreamingYamlParser, error) {
	// Get file size for progress tracking
	fileInfo, err := os.Stat(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	
	return &StreamingYamlParser{
		filename:   filename,
		batchSize:  config.BatchSize,
		bufferSize: config.BufferSize,
		totalBytes: fileInfo.Size(),
		startTime:  time.Now(),
	}, nil
}

// ParseBatches streams YAML file and returns batches of monitors
func (p *StreamingYamlParser) ParseBatches(ctx context.Context, progressChan chan<- Progress) (<-chan MonitorBatch, <-chan error) {
	batchChan := make(chan MonitorBatch, 10) // Buffer for batches
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

// parseFile performs the actual file parsing
func (p *StreamingYamlParser) parseFile(ctx context.Context, batchChan chan<- MonitorBatch, progressChan chan<- Progress) error {
	file, err := os.Open(p.filename)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()
	
	// Create buffered reader for efficient I/O
	reader := bufio.NewReaderSize(file, p.bufferSize)
	decoder := yaml.NewDecoder(reader)
	
	// Parse the entire manifest first  
	fmt.Printf("DEBUG: Starting to parse YAML file...\n")
	var manifest schema.Manifest
	err = decoder.Decode(&manifest)
	if err != nil {
		return fmt.Errorf("failed to decode manifest: %w", err)
	}
	
	fmt.Printf("DEBUG: Loaded manifest with %d monitors\n", len(manifest.Monitors))
	
	// Now process monitors in batches with concurrent sending
	totalMonitors := len(manifest.Monitors)
	
	// Progress reporting ticker
	progressTicker := time.NewTicker(1 * time.Second)
	defer progressTicker.Stop()
	
	// Process monitors in large chunks with maximum concurrency
	const maxGoroutines = 100 // 100 concurrent goroutines
	const chunkSize = 10000   // 10k monitors per routine
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
		
		// Process large chunk concurrently, splitting into smaller batches
		sem <- struct{}{} // Acquire semaphore
		go func(chunkStart, chunkEnd int) {
			defer func() { <-sem }() // Release semaphore
			
			// Process this chunk in smaller batches
			for j := chunkStart; j < chunkEnd; j += p.batchSize {
				batchEnd := j + p.batchSize
				if batchEnd > chunkEnd {
					batchEnd = chunkEnd
				}
				
				// Create batch efficiently 
				batchMonitors := make([]schema.Monitor, batchEnd-j)
				copy(batchMonitors, manifest.Monitors[j:batchEnd])
				
				// Send batch (non-blocking)
				select {
				case batchChan <- MonitorBatch{
					Monitors: batchMonitors,
					BatchID:  j / p.batchSize, // Calculate batch ID
					Offset:   int64(j),
				}:
					// Update stats for this batch
					for range batchMonitors {
						p.incrementStats()
					}
				case <-ctx.Done():
					return
				}
			}
		}(i, end)
		
		// Small yield every 10 chunks to prevent overwhelming
		if (i/chunkSize)%10 == 0 && i > 0 {
			time.Sleep(10 * time.Microsecond)
		}
	}
	
	// Wait for all goroutines to complete
	for i := 0; i < maxGoroutines; i++ {
		sem <- struct{}{}
	}
	
	// Final progress report
	p.reportProgress(progressChan)
	return nil
}

// incrementStats safely increments parsing statistics
func (p *StreamingYamlParser) incrementStats() {
	p.mu.Lock()
	p.entitiesRead++
	p.mu.Unlock()
}

// reportProgress sends progress update
func (p *StreamingYamlParser) reportProgress(progressChan chan<- Progress) {
	if progressChan == nil {
		return
	}
	
	p.mu.RLock()
	entitiesRead := p.entitiesRead
	p.mu.RUnlock()
	
	elapsed := time.Since(p.startTime)
	rate := float64(entitiesRead) / elapsed.Seconds()
	
	// For progress, we'll use the entities read vs a reasonable estimate
	// This is simplified since we don't have accurate byte tracking during parsing
	var percentage float64
	var estimatedRemaining time.Duration
	if entitiesRead > 0 {
		percentage = 50.0 // Rough progress estimate
	}
	
	select {
	case progressChan <- Progress{
		EntitiesProcessed:  entitiesRead,
		TotalBytes:        p.totalBytes,
		ProcessedBytes:    int64(float64(p.totalBytes) * percentage / 100),
		Percentage:        percentage,
		Rate:             rate,
		EstimatedRemaining: estimatedRemaining,
	}:
	default:
		// Don't block if channel is full
	}
}

// GetStats returns current parsing statistics
func (p *StreamingYamlParser) GetStats() (entitiesRead int64, rate float64) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	elapsed := time.Since(p.startTime)
	return p.entitiesRead, float64(p.entitiesRead) / elapsed.Seconds()
}