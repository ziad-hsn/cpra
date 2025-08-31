package optimized

import (
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mlange-42/ark-tools/app"
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"cpra/internal/queue/optimized"
)

// MockLogger implements the Logger interface for testing
type MockLogger struct{}

func (ml *MockLogger) Info(format string, args ...interface{})    {}
func (ml *MockLogger) Debug(format string, args ...interface{})   {}
func (ml *MockLogger) Warn(format string, args ...interface{})    {}
func (ml *MockLogger) Error(format string, args ...interface{})   {}
func (ml *MockLogger) LogSystemPerformance(name string, duration time.Duration, count int) {}
func (ml *MockLogger) LogComponentState(entityID uint32, component string, action string) {}

// TestBatchPulseSystem tests the batch pulse system
func TestBatchPulseSystem(t *testing.T) {
	// Create ECS world
	tool := app.New(1024).Seed(123)
	world := &tool.World
	
	// Create queue components
	queue := optimized.NewBoundedQueue(optimized.QueueConfig{
		MaxSize:      1000,
		MaxBatch:     100,
		BatchTimeout: 50 * time.Millisecond,
	})
	batchCollector := optimized.NewBatchCollector(queue, 100, 50*time.Millisecond)
	
	// Create system config
	config := SystemConfig{
		BatchSize:       100,
		BufferSize:      1000,
		ProcessInterval: 100 * time.Millisecond,
	}
	
	// Create system
	system := NewBatchPulseSystem(world, batchCollector, config)
	
	// Test system creation
	if system == nil {
		t.Fatal("Failed to create BatchPulseSystem")
	}
	
	// Test initialization
	if system.pulseFilter == nil {
		t.Error("Pulse filter not initialized")
	}
	
	// Test batch size
	if system.batchSize != config.BatchSize {
		t.Errorf("Expected batch size %d, got %d", config.BatchSize, system.batchSize)
	}
	
	// Cleanup
	batchCollector.Close()
	queue.Close()
}

// TestBatchInterventionSystem tests the batch intervention system
func TestBatchInterventionSystem(t *testing.T) {
	// Create ECS world
	tool := app.New(1024).Seed(123)
	world := &tool.World
	
	// Create queue components
	queue := optimized.NewBoundedQueue(optimized.QueueConfig{
		MaxSize:      1000,
		MaxBatch:     100,
		BatchTimeout: 50 * time.Millisecond,
	})
	batchCollector := optimized.NewBatchCollector(queue, 100, 50*time.Millisecond)
	
	// Create system config
	config := SystemConfig{
		BatchSize:       100,
		BufferSize:      1000,
		ProcessInterval: 100 * time.Millisecond,
	}
	
	// Create mock logger
	logger := &MockLogger{}
	
	// Create system
	system := NewBatchInterventionSystem(world, batchCollector, config, logger)
	
	// Test system creation
	if system == nil {
		t.Fatal("Failed to create BatchInterventionSystem")
	}
	
	// Test initialization
	// Note: We can't directly compare generic filters to nil, so we'll just check if the system was created
	
	// Test batch size
	if system.batchSize != config.BatchSize {
		t.Errorf("Expected batch size %d, got %d", config.BatchSize, system.batchSize)
	}
	
	// Cleanup
	batchCollector.Close()
	queue.Close()
}

// TestBatchCodeSystem tests the batch code system
func TestBatchCodeSystem(t *testing.T) {
	// Create ECS world
	tool := app.New(1024).Seed(123)
	world := &tool.World
	
	// Create queue components
	queue := optimized.NewBoundedQueue(optimized.QueueConfig{
		MaxSize:      1000,
		MaxBatch:     100,
		BatchTimeout: 50 * time.Millisecond,
	})
	batchCollector := optimized.NewBatchCollector(queue, 100, 50*time.Millisecond)
	
	// Create system config
	config := SystemConfig{
		BatchSize:       100,
		BufferSize:      1000,
		ProcessInterval: 100 * time.Millisecond,
	}
	
	// Create mock logger
	logger := &MockLogger{}
	
	// Create system
	system := NewBatchCodeSystem(world, batchCollector, config, logger)
	
	// Test system creation
	if system == nil {
		t.Fatal("Failed to create BatchCodeSystem")
	}
	
	// Test initialization
	if system.CodeNeededFilter == nil {
		t.Error("Code filter not initialized")
	}
	
	// Test batch size
	if system.batchSize != config.BatchSize {
		t.Errorf("Expected batch size %d, got %d", config.BatchSize, system.batchSize)
	}
	
	// Cleanup
	batchCollector.Close()
	queue.Close()
}

// TestMockResultChannels tests the mock result channels
func TestMockResultChannels(t *testing.T) {
	// Create ECS world for testing
	tool := app.New(1024).Seed(123)
	world := &tool.World
	
	// Create mock channels
	channels := NewMockResultChannels()
	defer channels.Close()
	
	// Test channel creation
	if channels.PulseResultChan == nil {
		t.Error("Pulse result channel not created")
	}
	if channels.InterventionResultChan == nil {
		t.Error("Intervention result channel not created")
	}
	if channels.CodeResultChan == nil {
		t.Error("Code result channel not created")
	}
	
	// Test sending results
	mockEntity := world.NewEntity()
	mockResult := &MockJobResult{entity: mockEntity, err: nil}
	
	channels.SendPulseResult(mockResult)
	channels.SendInterventionResult(mockResult)
	channels.SendCodeResult(mockResult)
	
	// Test receiving results
	select {
	case result := <-channels.PulseResultChan:
		if result.Entity() != mockEntity {
			t.Errorf("Expected entity %v, got %v", mockEntity, result.Entity())
		}
	default:
		t.Error("No pulse result received")
	}
	
	select {
	case result := <-channels.InterventionResultChan:
		if result.Entity() != mockEntity {
			t.Errorf("Expected entity %v, got %v", mockEntity, result.Entity())
		}
	default:
		t.Error("No intervention result received")
	}
	
	select {
	case result := <-channels.CodeResultChan:
		if result.Entity() != mockEntity {
			t.Errorf("Expected entity %v, got %v", mockEntity, result.Entity())
		}
	default:
		t.Error("No code result received")
	}
}

// MockJobResult implements jobs.Result for testing
type MockJobResult struct {
	entity ecs.Entity
	err    error
}

func (mjr *MockJobResult) Entity() ecs.Entity { return mjr.entity }
func (mjr *MockJobResult) Err() error         { return mjr.err } 