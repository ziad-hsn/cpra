package loader

import (
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
)

// ProgressReporter displays loading progress to the terminal.
// It shows a progress bar, percentage, counts, elapsed time, and ETA.
type ProgressReporter struct {
	total       int64         // Total bytes or monitors (0 if unknown)
	current     *int64        // Pointer to current progress counter (bytes)
	monitors    *int64        // Number of monitors parsed (for display)
	startTime   time.Time     // When loading started
	lastUpdate  time.Time     // Last time we updated the display
	updateEvery time.Duration // Minimum time between display updates
	writer      io.Writer     // Where to write progress (usually os.Stderr)
	label       string        // What we're loading (e.g., "monitors")
	barWidth    int           // Width of the progress bar in characters
	lastPct     int           // Last percentage displayed (for rate limiting)
}

// NewProgressReporter creates a new progress reporter.
// If total is 0, shows indeterminate progress (no bar, just counts).
func NewProgressReporter(writer io.Writer, label string, total int64) *ProgressReporter {
	return &ProgressReporter{
		total:       total,
		current:     new(int64),
		monitors:    new(int64),
		startTime:   time.Now(),
		updateEvery: 100 * time.Millisecond, // 10 updates/sec max
		writer:      writer,
		label:       label,
		barWidth:    30,
	}
}

// SetCurrent sets the current progress value.
func (p *ProgressReporter) SetCurrent(current int64) {
	atomic.StoreInt64(p.current, current)
}

// GetCurrent returns the current progress value.
func (p *ProgressReporter) GetCurrent() int64 {
	return atomic.LoadInt64(p.current)
}

// Update updates the progress display.
// Only redraws if enough time has passed since the last update.
// Returns true if the display was updated.
func (p *ProgressReporter) Update(current int64) bool {
	return p.UpdateWithMonitors(current, 0)
}

// UpdateWithMonitors updates progress using bytes for percentage, monitors for display.
func (p *ProgressReporter) UpdateWithMonitors(bytesRead, monitorsCount int64) bool {
	atomic.StoreInt64(p.current, bytesRead)
	atomic.StoreInt64(p.monitors, monitorsCount)

	// Throttle updates to avoid terminal spam
	now := time.Now()
	if now.Sub(p.lastUpdate) < p.updateEvery {
		return false
	}
	p.lastUpdate = now

	elapsed := now.Sub(p.startTime)

	// Calculate rate (monitors per second for display)
	var monitorRate float64
	if elapsed.Seconds() > 0 {
		monitorRate = float64(monitorsCount) / elapsed.Seconds()
	}

	// Format the progress line
	var line string
	if p.total > 0 {
		// Determinate progress with bar (based on bytes)
		pct := float64(bytesRead) / float64(p.total) * 100
		if pct > 100 {
			pct = 100
		}

		// Calculate ETA based on byte progress
		var eta time.Duration
		if bytesRead > 0 && bytesRead < p.total {
			byteRate := float64(bytesRead) / elapsed.Seconds()
			if byteRate > 0 {
				remaining := float64(p.total - bytesRead)
				eta = time.Duration(remaining/byteRate) * time.Second
			}
		}

		// Build progress bar
		bar := p.buildBar(pct)

		// Show monitor count (more meaningful than bytes)
		monitorStr := formatCount(monitorsCount)
		rateStr := formatCount(int64(monitorRate))

		if eta > 0 {
			line = fmt.Sprintf("\rLoading %s: %s %.1f%% | %s loaded (%s/sec) | %v elapsed | ETA: %v    ",
				p.label, bar, pct, monitorStr, rateStr,
				elapsed.Round(time.Second), eta.Round(time.Second))
		} else {
			line = fmt.Sprintf("\rLoading %s: %s %.1f%% | %s loaded (%s/sec) | %v elapsed    ",
				p.label, bar, pct, monitorStr, rateStr,
				elapsed.Round(time.Second))
		}
	} else {
		// Indeterminate progress (no bar)
		monitorStr := formatCount(monitorsCount)
		rateStr := formatCount(int64(monitorRate))
		line = fmt.Sprintf("\rLoading %s: %s loaded | %s/sec | %v elapsed    ",
			p.label, monitorStr, rateStr, elapsed.Round(time.Second))
	}

	fmt.Fprint(p.writer, line)
	return true
}

// Complete finishes the progress display with a summary line.
func (p *ProgressReporter) Complete() {
	monitors := atomic.LoadInt64(p.monitors)
	elapsed := time.Since(p.startTime)

	var rate float64
	if elapsed.Seconds() > 0 {
		rate = float64(monitors) / elapsed.Seconds()
	}

	// Clear line and print final summary
	fmt.Fprintf(p.writer, "\rLoaded %s %s in %v (%s/sec)%s\n",
		formatCount(monitors), p.label,
		elapsed.Round(time.Millisecond),
		formatCount(int64(rate)),
		strings.Repeat(" ", 30)) // Clear any trailing characters
}

// buildBar creates the visual progress bar.
func (p *ProgressReporter) buildBar(pct float64) string {
	filled := int(float64(p.barWidth) * pct / 100)
	if filled > p.barWidth {
		filled = p.barWidth
	}
	empty := p.barWidth - filled

	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", empty) + "]"
}

// formatCount formats a number with K/M suffix for readability.
func formatCount(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// DefaultProgressCallback returns a progress callback that uses ProgressReporter.
// Call the returned cleanup function when loading is complete to print the final summary.
func DefaultProgressCallback(writer io.Writer, totalBytes int64) (ProgressCallback, func()) {
	reporter := NewProgressReporter(writer, "monitors", totalBytes)

	callback := func(progress LoadProgress) {
		// Use bytes read for accurate progress percentage
		// Store monitors count for display
		reporter.UpdateWithMonitors(progress.BytesRead, progress.MonitorsParsed)
	}

	cleanup := func() {
		reporter.Complete()
	}

	return callback, cleanup
}
