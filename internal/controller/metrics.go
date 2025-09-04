package controller

import (
	"sync"
	"time"
)

// SystemMetrics holds performance metrics for a specific system
type SystemMetrics struct {
	SystemName          string
	TotalUpdates        int64
	TotalEntitiesProcessed int64
	TotalBatchesCreated    int64
	TotalDuration          time.Duration
	MaxUpdateDuration      time.Duration
	MinUpdateDuration      time.Duration
	LastUpdateTime         time.Time
	StartTime              time.Time
}

// MetricsAggregator collects and aggregates metrics from all systems
type MetricsAggregator struct {
	mu      sync.RWMutex
	systems map[string]*SystemMetrics
}

// NewMetricsAggregator creates a new metrics aggregator
func NewMetricsAggregator() *MetricsAggregator {
	return &MetricsAggregator{
		systems: make(map[string]*SystemMetrics),
	}
}

// RegisterSystem registers a new system for metrics collection
func (ma *MetricsAggregator) RegisterSystem(systemName string) {
	ma.mu.Lock()
	defer ma.mu.Unlock()
	
	ma.systems[systemName] = &SystemMetrics{
		SystemName:        systemName,
		StartTime:         time.Now(),
		MinUpdateDuration: time.Hour, // Initialize to high value
	}
}

// RecordSystemUpdate records a system update with performance metrics
func (ma *MetricsAggregator) RecordSystemUpdate(systemName string, duration time.Duration, entitiesProcessed int64, batchesCreated int64) {
	ma.mu.Lock()
	defer ma.mu.Unlock()
	
	metrics, exists := ma.systems[systemName]
	if !exists {
		// Auto-register system if not found
		metrics = &SystemMetrics{
			SystemName:        systemName,
			StartTime:         time.Now(),
			MinUpdateDuration: time.Hour,
		}
		ma.systems[systemName] = metrics
	}
	
	metrics.TotalUpdates++
	metrics.TotalEntitiesProcessed += entitiesProcessed
	metrics.TotalBatchesCreated += batchesCreated
	metrics.TotalDuration += duration
	metrics.LastUpdateTime = time.Now()
	
	// Update min/max durations
	if duration > metrics.MaxUpdateDuration {
		metrics.MaxUpdateDuration = duration
	}
	if duration < metrics.MinUpdateDuration {
		metrics.MinUpdateDuration = duration
	}
}

// GetSystemMetrics returns metrics for a specific system
func (ma *MetricsAggregator) GetSystemMetrics(systemName string) (*SystemMetrics, bool) {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	
	metrics, exists := ma.systems[systemName]
	if !exists {
		return nil, false
	}
	
	// Return a copy to avoid race conditions
	copy := *metrics
	return &copy, true
}

// GetAllMetrics returns metrics for all systems
func (ma *MetricsAggregator) GetAllMetrics() map[string]*SystemMetrics {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	
	result := make(map[string]*SystemMetrics)
	for name, metrics := range ma.systems {
		copy := *metrics
		result[name] = &copy
	}
	
	return result
}

// GetAggregateMetrics returns aggregate metrics across all systems
func (ma *MetricsAggregator) GetAggregateMetrics() AggregateMetrics {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	
	var aggregate AggregateMetrics
	aggregate.StartTime = time.Now()
	
	for _, metrics := range ma.systems {
		aggregate.TotalUpdates += metrics.TotalUpdates
		aggregate.TotalEntitiesProcessed += metrics.TotalEntitiesProcessed
		aggregate.TotalBatchesCreated += metrics.TotalBatchesCreated
		aggregate.TotalDuration += metrics.TotalDuration
		aggregate.SystemCount++
		
		if metrics.StartTime.Before(aggregate.StartTime) {
			aggregate.StartTime = metrics.StartTime
		}
		if metrics.MaxUpdateDuration > aggregate.MaxUpdateDuration {
			aggregate.MaxUpdateDuration = metrics.MaxUpdateDuration
		}
		if aggregate.MinUpdateDuration == 0 || (metrics.MinUpdateDuration < aggregate.MinUpdateDuration && metrics.MinUpdateDuration > 0) {
			aggregate.MinUpdateDuration = metrics.MinUpdateDuration
		}
	}
	
	// Calculate averages
	if aggregate.TotalUpdates > 0 {
		aggregate.AvgUpdateDuration = aggregate.TotalDuration / time.Duration(aggregate.TotalUpdates)
		aggregate.AvgEntitiesPerUpdate = float64(aggregate.TotalEntitiesProcessed) / float64(aggregate.TotalUpdates)
		aggregate.AvgBatchesPerUpdate = float64(aggregate.TotalBatchesCreated) / float64(aggregate.TotalUpdates)
	}
	
	// Calculate throughput
	totalRuntime := time.Since(aggregate.StartTime)
	if totalRuntime > 0 {
		aggregate.EntitiesPerSecond = float64(aggregate.TotalEntitiesProcessed) / totalRuntime.Seconds()
		aggregate.UpdatesPerSecond = float64(aggregate.TotalUpdates) / totalRuntime.Seconds()
	}
	
	return aggregate
}

// AggregateMetrics holds aggregate performance metrics across all systems
type AggregateMetrics struct {
	SystemCount             int
	TotalUpdates            int64
	TotalEntitiesProcessed  int64
	TotalBatchesCreated     int64
	TotalDuration           time.Duration
	MaxUpdateDuration       time.Duration
	MinUpdateDuration       time.Duration
	AvgUpdateDuration       time.Duration
	AvgEntitiesPerUpdate    float64
	AvgBatchesPerUpdate     float64
	EntitiesPerSecond       float64
	UpdatesPerSecond        float64
	StartTime               time.Time
}