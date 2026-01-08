package jobs

import (
	"context"
	"runtime"
	"strings"
	"time"

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
	BaseJob
	Host            string
	Count           int
	IgnorePrivilege bool
}

// Execute performs the ICMP ping check with retries.
func (p *PulseICMPJob) Execute(ctx context.Context) Result {
	// Create fresh payload - cannot use pool because payload escapes in Result
	payload := map[string]interface{}{
		"type":   "pulse",
		"driver": "icmp",
	}

	// Use global dial limiter to prevent CPU spikes during mass failures.
	if !GetDialLimiter().Acquire(ctx) {
		return Result{Ent: p.Entity, Err: ErrDialLimiterTimeout, Payload: payload}
	}
	defer GetDialLimiter().Release()

	count := p.Count
	if count <= 0 {
		count = 1
	}
	payload["count"] = count

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = time.Duration(count)*time.Second + 500*time.Millisecond
	}

	var privilegeIgnored bool

	err := RetryWithBackoff(ctx, p.Retries+1, 50*time.Millisecond, func() error {
		// Create a fresh pinger each attempt - pro-bing Pinger is not safe for reuse
		pr, pingerErr := ping.NewPinger(p.Host)
		if pingerErr != nil {
			return pingerErr
		}

		// Default privilege: Linux unprivileged, others privileged
		if runtime.GOOS == "linux" {
			pr.SetPrivileged(false)
		} else {
			pr.SetPrivileged(true)
		}

		pr.Count = count
		pr.Timeout = timeout

		runErr := pr.Run()
		if runErr == nil {
			if stats := pr.Statistics(); stats != nil && stats.PacketsRecv > 0 {
				return nil // Success
			}
			return ErrICMPCheckFailed // No packets received
		}

		// Privilege fallback for Linux
		if !pr.Privileged() && isPrivilegeError(runErr) {
			pr.SetPrivileged(true)
			privilegedErr := pr.Run()
			if privilegedErr == nil {
				if stats := pr.Statistics(); stats != nil && stats.PacketsRecv > 0 {
					return nil // Success with elevated privilege
				}
				return ErrICMPCheckFailed // No packets received
			}
			if p.IgnorePrivilege && isPrivilegeError(privilegedErr) {
				privilegeIgnored = true
				return nil // Ignore privilege error
			}
			return privilegedErr
		}

		if p.IgnorePrivilege && isPrivilegeError(runErr) {
			privilegeIgnored = true
			return nil // Ignore privilege error
		}
		return runErr
	})

	if privilegeIgnored {
		payload["privilege_ignored"] = true
	}

	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return Result{Ent: p.Entity, Err: err, Payload: payload}
		}
		return Result{Ent: p.Entity, Err: ErrICMPCheckFailed, Payload: payload}
	}
	return Result{Ent: p.Entity, Err: nil, Payload: payload}
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

// Reset clears the job for pool reuse.
func (p *PulseICMPJob) Reset() {
	p.BaseJob.Reset()
	p.Host = ""
	p.Count = 0
	p.IgnorePrivilege = false
}

// IsNil checks if the job is nil.
func (p *PulseICMPJob) IsNil() bool {
	return p == nil
}
