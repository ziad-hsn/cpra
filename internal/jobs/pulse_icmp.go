package jobs

import (
	"context"
	"runtime"
	"strings"
	"time"

	"github.com/mlange-42/ark/ecs"
	ping "github.com/prometheus-community/pro-bing"
)

// PulseICMPJob performs ICMP ping health checks.
// It uses the pro-bing library for cross-platform ICMP support.
//
// Safety features:
//   - Uses global dial limiter to prevent CPU spikes during outages
//   - Creates fresh pinger each attempt (pro-bing is not reuse-safe)
//   - Handles privilege escalation fallback on Linux
//   - Fresh payload per execution (cannot pool - escapes to Result)
//
// Note: ICMP payloads are NOT pooled because the payload map escapes in
// the Result struct and is read asynchronously by RouteResults.
type PulseICMPJob struct {
	EnqueueTime     time.Time
	StartTime       time.Time
	Host            string
	JobType         string
	Driver          string
	Timeout         time.Duration
	Count           int
	Retries         int
	Entity          ecs.Entity
	IgnorePrivilege bool
}

// Execute performs the ICMP ping check with retries.
func (p *PulseICMPJob) Execute(ctx context.Context) Result {
	// Create fresh payload - cannot use pool because payload escapes in Result
	// and its lifetime extends beyond this function (read by RouteResults later)
	payload := map[string]interface{}{
		"type":   "pulse",
		"driver": "icmp",
	}

	// Use global dial limiter to prevent CPU spikes during mass failures.
	if !GetDialLimiter().Acquire(ctx) {
		return Result{Ent: p.Entity, Err: ErrDialLimiterTimeout, Payload: payload}
	}
	defer GetDialLimiter().Release()

	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}

	count := p.Count
	if count <= 0 {
		count = 1
	}

	// Add count to the payload
	payload["count"] = count

	for attempt := 0; attempt < attempts; attempt++ {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return Result{Ent: p.Entity, Err: ctx.Err(), Payload: payload}
		default:
		}

		// Create a fresh pinger each attempt - pro-bing Pinger is not safe for reuse
		pr, err := ping.NewPinger(p.Host)
		if err != nil {
			return Result{Ent: p.Entity, Err: err, Payload: payload}
		}

		// Default privilege: Linux unprivileged, others privileged
		switch runtime.GOOS {
		case "linux":
			pr.SetPrivileged(false)
		default:
			pr.SetPrivileged(true)
		}

		pr.Count = count
		if p.Timeout > 0 {
			pr.Timeout = p.Timeout
		} else {
			pr.Timeout = time.Duration(count)*time.Second + 500*time.Millisecond
		}

		if err := pr.Run(); err == nil {
			stats := pr.Statistics()
			if stats != nil && stats.PacketsRecv > 0 {
				return Result{Ent: p.Entity, Err: nil, Payload: payload}
			}
			// No packets received, continue retry
		} else {
			// Privilege fallback for Linux
			if !pr.Privileged() && isPrivilegeError(err) {
				pr.SetPrivileged(true)
				if err2 := pr.Run(); err2 == nil {
					stats := pr.Statistics()
					if stats != nil && stats.PacketsRecv > 0 {
						return Result{Ent: p.Entity, Err: nil, Payload: payload}
					}
					// No packets received, continue retry
				} else {
					if p.IgnorePrivilege && isPrivilegeError(err2) {
						payload["privilege_ignored"] = true
						return Result{Ent: p.Entity, Err: nil, Payload: payload}
					}
				}
			} else {
				if p.IgnorePrivilege && isPrivilegeError(err) {
					payload["privilege_ignored"] = true
					return Result{Ent: p.Entity, Err: nil, Payload: payload}
				}
			}
		}

		// Brief pause before retry (don't block on last attempt)
		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	return Result{
		Ent:     p.Entity,
		Err:     ErrICMPCheckFailed,
		Payload: payload,
	}
}

// isPrivilegeError checks common privilege-related error strings from pinger.
func isPrivilegeError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "operation not permitted") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "privilege")
}

// Copy returns a shallow copy of the job for safe pool reuse.
func (p *PulseICMPJob) Copy() Job { job := *p; return &job }

// GetEnqueueTime returns when the job was enqueued.
func (p *PulseICMPJob) GetEnqueueTime() time.Time { return p.EnqueueTime }

// SetEnqueueTime sets when the job was enqueued.
func (p *PulseICMPJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }

// GetStartTime returns when the job started executing.
func (p *PulseICMPJob) GetStartTime() time.Time { return p.StartTime }

// SetStartTime sets when the job started executing.
func (p *PulseICMPJob) SetStartTime(t time.Time) { p.StartTime = t }

// IsNil returns true if the job pointer is nil.
func (p *PulseICMPJob) IsNil() bool { return p == nil }
