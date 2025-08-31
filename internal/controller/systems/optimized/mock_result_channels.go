package optimized

import (
	"cpra/internal/jobs"
	"sync"
)

// MockResultChannels provides mock result channels for testing
type MockResultChannels struct {
	PulseResultChan        chan jobs.Result
	InterventionResultChan chan jobs.Result
	CodeResultChan         chan jobs.Result
	
	mu sync.RWMutex
}

// NewMockResultChannels creates new mock result channels
func NewMockResultChannels() *MockResultChannels {
	return &MockResultChannels{
		PulseResultChan:        make(chan jobs.Result, 1000),
		InterventionResultChan: make(chan jobs.Result, 1000),
		CodeResultChan:         make(chan jobs.Result, 1000),
	}
}

// SendPulseResult sends a pulse result to the channel
func (mrc *MockResultChannels) SendPulseResult(result jobs.Result) {
	mrc.mu.RLock()
	defer mrc.mu.RUnlock()
	
	select {
	case mrc.PulseResultChan <- result:
	default:
		// Channel full, drop result
	}
}

// SendInterventionResult sends an intervention result to the channel
func (mrc *MockResultChannels) SendInterventionResult(result jobs.Result) {
	mrc.mu.RLock()
	defer mrc.mu.RUnlock()
	
	select {
	case mrc.InterventionResultChan <- result:
	default:
		// Channel full, drop result
	}
}

// SendCodeResult sends a code result to the channel
func (mrc *MockResultChannels) SendCodeResult(result jobs.Result) {
	mrc.mu.RLock()
	defer mrc.mu.RUnlock()
	
	select {
	case mrc.CodeResultChan <- result:
	default:
		// Channel full, drop result
	}
}

// Close closes all result channels
func (mrc *MockResultChannels) Close() {
	mrc.mu.Lock()
	defer mrc.mu.Unlock()
	
	close(mrc.PulseResultChan)
	close(mrc.InterventionResultChan)
	close(mrc.CodeResultChan)
}
