package jobs

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// codeAlertTemplate holds the template for code alert messages.
type codeAlertTemplate struct {
	Title     string
	Status    string
	Severity  string
	Summary   string
	Action    string
	NextSteps string
}

// codeAlertTemplateFor returns the appropriate template for the given color.
func codeAlertTemplateFor(color string) codeAlertTemplate {
	switch strings.ToLower(color) {
	case "red":
		return codeAlertTemplate{
			Title:     "CRITICAL ALERT",
			Status:    "FAILED",
			Severity:  "critical",
			Summary:   "Service outage detected after repeated health check failures and interventions",
			Action:    "Escalate immediately and engage on-call responders",
			NextSteps: "Perform manual recovery and review related service telemetry",
		}
	case "yellow":
		return codeAlertTemplate{
			Title:     "DEGRADED ALERT",
			Status:    "DEGRADED",
			Severity:  "warning",
			Summary:   "Service health checks are failing consecutively beyond safe thresholds",
			Action:    "Investigate partial outage or performance regression",
			NextSteps: "Validate dependencies, review recent changes, and monitor closely",
		}
	case "green":
		return codeAlertTemplate{
			Title:     "RECOVERY NOTICE",
			Status:    "RECOVERED",
			Severity:  "info",
			Summary:   "Service returned to a healthy state after previous failures",
			Action:    "No immediate action required",
			NextSteps: "Continue monitoring stability and capture incident follow-up notes",
		}
	case "cyan":
		return codeAlertTemplate{
			Title:     "INTERVENTION SUCCESS",
			Status:    "RESTORED",
			Severity:  "info",
			Summary:   "Automated intervention completed and service health checks are passing",
			Action:    "Confirm downstream systems are stable",
			NextSteps: "Document intervention details and verify customer impact is resolved",
		}
	case "gray":
		return codeAlertTemplate{
			Title:     "MAINTENANCE MODE",
			Status:    "MAINTENANCE",
			Severity:  "info",
			Summary:   "Monitor is intentionally suppressed during planned maintenance",
			Action:    "No action required during maintenance window",
			NextSteps: "Re-enable monitoring once maintenance activities conclude",
		}
	default:
		return codeAlertTemplate{
			Title:     "STATUS UPDATE",
			Status:    "UNKNOWN",
			Severity:  "unknown",
			Summary:   "Monitor generated an unspecified status update",
			Action:    "Review monitor configuration and recent events",
			NextSteps: "Validate service state and adjust alert routing if required",
		}
	}
}

// buildCodeNotificationMessage builds a human-readable alert message.
func buildCodeNotificationMessage(monitor string, tpl codeAlertTemplate) string {
	var b strings.Builder
	// Pre-size approximately to reduce reallocations
	b.Grow(len(tpl.Title) + len("\nMonitor: ") + len(monitor) +
		len("\nStatus: ") + len(tpl.Status) + len("\nSeverity: ") + len(tpl.Severity) +
		len("\nSummary: ") + len(tpl.Summary) + len("\nRecommended Action: ") + len(tpl.Action) +
		len("\nNext Steps: ") + len(tpl.NextSteps) + 8)
	b.WriteString(tpl.Title)
	b.WriteString("\nMonitor: ")
	b.WriteString(monitor)
	b.WriteString("\nStatus: ")
	b.WriteString(tpl.Status)
	b.WriteString("\nSeverity: ")
	b.WriteString(strings.ToUpper(tpl.Severity))
	b.WriteString("\nSummary: ")
	b.WriteString(tpl.Summary)
	b.WriteString("\nRecommended Action: ")
	b.WriteString(tpl.Action)
	b.WriteString("\nNext Steps: ")
	b.WriteString(tpl.NextSteps)
	return b.String()
}

// CodeLogJob writes alert notifications to a log file in JSON format.
// It uses an async LogManager to avoid blocking on file I/O.
type CodeLogJob struct {
	BaseJob
	Status    string
	Monitor   string
	Color     string
	Severity  string
	Summary   string
	Action    string
	NextSteps string
	File      string
}

// Execute writes the alert to the log file.
func (c *CodeLogJob) Execute(_ context.Context) Result {
	payload := map[string]interface{}{
		"type":     "code",
		"color":    c.Color,
		"severity": c.Severity,
		"status":   c.Status,
	}

	// Build message on-demand
	tpl := codeAlertTemplate{
		Title:     codeAlertTemplateFor(c.Color).Title,
		Status:    c.Status,
		Severity:  c.Severity,
		Summary:   c.Summary,
		Action:    c.Action,
		NextSteps: c.NextSteps,
	}
	message := buildCodeNotificationMessage(c.Monitor, tpl)

	now := time.Now().UTC()
	entry := struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Monitor   string `json:"monitor"`
		Color     string `json:"color"`
		Status    string `json:"status"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Action    string `json:"action"`
		NextSteps string `json:"next_steps,omitempty"`
		Message   string `json:"message,omitempty"`
	}{
		Timestamp: now.Format(time.RFC3339Nano),
		Type:      "code",
		Monitor:   c.Monitor,
		Color:     c.Color,
		Status:    c.Status,
		Severity:  c.Severity,
		Summary:   c.Summary,
		Action:    c.Action,
		NextSteps: c.NextSteps,
		Message:   message,
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return Result{Ent: c.Entity, Err: ErrLogMarshalFailed, Payload: payload}
	}

	line = append(line, '\n')

	// Use asynchronous log manager
	GetLogManager().WriteLog(c.File, line)

	return Result{Ent: c.Entity, Err: nil, Payload: payload}
}

// Copy returns a shallow copy of the job for safe pool reuse.
func (c *CodeLogJob) Copy() Job { job := *c; return &job }

func (c *CodeLogJob) Reset() {
	c.BaseJob.Reset()
	c.Status = ""
	c.Monitor = ""
	c.Color = ""
	c.Severity = ""
	c.Summary = ""
	c.Action = ""
	c.NextSteps = ""
	c.File = ""
}

// IsNil checks if the job is nil.
func (c *CodeLogJob) IsNil() bool {
	return c == nil
}
