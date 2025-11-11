package controller

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TraceSpan represents a single trace span
type TraceSpan struct {
	StartTime time.Time
	EndTime   time.Time
	Error     error
	Metadata  map[string]interface{}
	Tags      map[string]string
	EntityID  *uint64
	ID        string
	ParentID  string
	Operation string
	Component string
	Duration  time.Duration
	Success   bool
}

// TraceContext holds trace information
type TraceContext struct {
	Baggage map[string]string
	TraceID string
	SpanID  string
}

// Tracer manages trace collection and storage
type Tracer struct {
	spans     map[string]*TraceSpan
	traces    map[string][]*TraceSpan
	logger    *Logger
	component string
	mu        sync.RWMutex
	enabled   bool
}

// NewTracer creates a new tracer instance
func NewTracer(component string, enabled bool) *Tracer {
	// Create a simple logger without tracing to avoid circular dependency
	simpleLogger := &Logger{
		level:       LogLevelDebug,
		component:   fmt.Sprintf("TRACE:%s", component),
		enableColor: false,
		debugMode:   true,
		prodMode:    false,
		timezone:    time.Local,
		tracer:      nil, // No tracer to avoid recursion
	}

	return &Tracer{
		spans:     make(map[string]*TraceSpan),
		traces:    make(map[string][]*TraceSpan),
		enabled:   enabled,
		component: component,
		logger:    simpleLogger,
	}
}

// StartSpan begins a new trace span
func (t *Tracer) StartSpan(ctx context.Context, operation string) (context.Context, *TraceSpan) {
	if !t.enabled {
		return ctx, nil
	}

	var traceID, parentSpanID string

	// Check if there's an existing trace context
	if traceCtx, ok := ctx.Value("traceContext").(*TraceContext); ok {
		traceID = traceCtx.TraceID
		parentSpanID = traceCtx.SpanID
	} else {
		traceID = uuid.New().String()
	}

	spanID := uuid.New().String()

	span := &TraceSpan{
		ID:        spanID,
		ParentID:  parentSpanID,
		Operation: operation,
		StartTime: time.Now(),
		Metadata:  make(map[string]interface{}),
		Tags:      make(map[string]string),
		Component: t.component,
	}

	// Add caller information
	if pc, file, line, ok := runtime.Caller(1); ok {
		span.Tags["caller.file"] = file
		span.Tags["caller.line"] = fmt.Sprintf("%d", line)
		span.Tags["caller.function"] = runtime.FuncForPC(pc).Name()
	}

	t.mu.Lock()
	t.spans[spanID] = span
	t.traces[traceID] = append(t.traces[traceID], span)
	t.mu.Unlock()

	// Create new context with trace information
	newTraceCtx := &TraceContext{
		TraceID: traceID,
		SpanID:  spanID,
		Baggage: make(map[string]string),
	}

	newCtx := context.WithValue(ctx, "traceContext", newTraceCtx)

	t.logger.Debug("Started span %s for operation %s (trace: %s, parent: %s)",
		spanID, operation, traceID, parentSpanID)

	return newCtx, span
}

// FinishSpan completes a trace span
func (t *Tracer) FinishSpan(span *TraceSpan, err error) {
	if !t.enabled || span == nil {
		return
	}

	span.EndTime = time.Now()
	span.Duration = span.EndTime.Sub(span.StartTime)
	span.Success = err == nil
	span.Error = err

	t.mu.Lock()
	if existingSpan, exists := t.spans[span.ID]; exists {
		*existingSpan = *span
	}
	t.mu.Unlock()

	status := "SUCCESS"
	if err != nil {
		status = "ERROR"
	}

	t.logger.Debug("Finished span %s (%s) in %v [%s]",
		span.ID, span.Operation, span.Duration, status)

	if err != nil {
		t.logger.Debug("Span %s error: %v", span.ID, err)
	}
}

// AddSpanTag adds a tag to a span
func (t *Tracer) AddSpanTag(span *TraceSpan, key, value string) {
	if !t.enabled || span == nil {
		return
	}
	span.Tags[key] = value
}

// AddSpanMetadata adds metadata to a span
func (t *Tracer) AddSpanMetadata(span *TraceSpan, key string, value interface{}) {
	if !t.enabled || span == nil {
		return
	}
	span.Metadata[key] = value
}

// SetSpanEntity sets the entity ID for a span
func (t *Tracer) SetSpanEntity(span *TraceSpan, entityID uint64) {
	if !t.enabled || span == nil {
		return
	}
	span.EntityID = &entityID
	span.Tags["entity.id"] = fmt.Sprintf("%d", entityID)
}

// GetTrace returns all spans for a trace ID
func (t *Tracer) GetTrace(traceID string) []*TraceSpan {
	if !t.enabled {
		return nil
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	spans := make([]*TraceSpan, len(t.traces[traceID]))
	copy(spans, t.traces[traceID])
	return spans
}

// GetSpan returns a specific span by ID
func (t *Tracer) GetSpan(spanID string) *TraceSpan {
	if !t.enabled {
		return nil
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	if span, exists := t.spans[spanID]; exists {
		// Return a copy
		spanCopy := *span
		return &spanCopy
	}
	return nil
}

// GetStats returns tracing statistics
func (t *Tracer) GetStats() map[string]interface{} {
	if !t.enabled {
		return map[string]interface{}{"enabled": false}
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	totalSpans := len(t.spans)
	totalTraces := len(t.traces)

	var avgDuration time.Duration
	var successCount, errorCount int

	if totalSpans > 0 {
		var totalDuration time.Duration
		for _, span := range t.spans {
			if !span.EndTime.IsZero() {
				totalDuration += span.Duration
				if span.Success {
					successCount++
				} else {
					errorCount++
				}
			}
		}
		if totalSpans > 0 {
			avgDuration = totalDuration / time.Duration(totalSpans)
		}
	}

	return map[string]interface{}{
		"enabled":       true,
		"total_spans":   totalSpans,
		"total_traces":  totalTraces,
		"success_count": successCount,
		"error_count":   errorCount,
		"avg_duration":  avgDuration.String(),
		"component":     t.component,
	}
}

// Cleanup removes old spans to prevent memory leaks
func (t *Tracer) Cleanup(maxAge time.Duration) {
	if !t.enabled {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	var removedSpans, removedTraces int

	// Remove old spans
	for spanID, span := range t.spans {
		if span.EndTime.Before(cutoff) || (span.EndTime.IsZero() && span.StartTime.Before(cutoff)) {
			delete(t.spans, spanID)
			removedSpans++
		}
	}

	// Remove empty traces
	for traceID, spans := range t.traces {
		activeSpans := 0
		for _, span := range spans {
			if _, exists := t.spans[span.ID]; exists {
				activeSpans++
			}
		}
		if activeSpans == 0 {
			delete(t.traces, traceID)
			removedTraces++
		}
	}

	if removedSpans > 0 || removedTraces > 0 {
		t.logger.Debug("Cleaned up %d spans and %d traces", removedSpans, removedTraces)
	}
}

// Global tracer instances
var (
	SystemTracer     *Tracer
	SchedulerTracer  *Tracer
	DispatchTracer   *Tracer
	ResultTracer     *Tracer
	WorkerPoolTracer *Tracer
	EntityTracer     *Tracer
)

// InitializeTracers sets up all component tracers
func InitializeTracers(enabled bool) {
	SystemTracer = NewTracer("SYSTEM", enabled)
	SchedulerTracer = NewTracer("SCHEDULER", enabled)
	DispatchTracer = NewTracer("DISPATCH", enabled)
	ResultTracer = NewTracer("RESULT", enabled)
	WorkerPoolTracer = NewTracer("WORKER", enabled)
	EntityTracer = NewTracer("ENTITY", enabled)
}

// StartPeriodicCleanup starts a goroutine that periodically cleans up old traces
func StartPeriodicCleanup(interval, maxAge time.Duration) {
	tracers := []*Tracer{
		SystemTracer, SchedulerTracer, DispatchTracer,
		ResultTracer, WorkerPoolTracer, EntityTracer,
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			for _, tracer := range tracers {
				if tracer != nil {
					tracer.Cleanup(maxAge)
				}
			}
		}
	}()
}
