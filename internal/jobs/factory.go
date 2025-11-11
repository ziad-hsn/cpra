package jobs

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/interning"
	"cpra/internal/loader/schema"
)

// CreatePulseJob creates a new pulse job based on the provided schema.
// It returns the appropriate job type (HTTP, TCP, or ICMP) based on the config.
//
// Jobs are obtained from sync.Pool for memory efficiency and must be
// returned after use via their respective put functions.
func CreatePulseJob(pulseSchema schema.Pulse, jobID ecs.Entity) (Job, error) {
	timeout := pulseSchema.Timeout
	switch cfg := pulseSchema.Config.(type) {
	case *schema.PulseHTTPConfig:
		// Extract host for fasthttp client pool
		host, isTLS, err := ExtractHostFromURL(cfg.Url)
		if err != nil {
			return nil, fmt.Errorf("invalid URL: %w", err)
		}
		job := getPulseHTTPJob()
		job.Entity = jobID
		job.URL = cfg.Url
		job.Method = interning.Intern(cfg.Method)
		job.Timeout = timeout
		job.Retries = cfg.Retries
		job.Host = host
		job.IsTLS = isTLS
		job.JobType = InternedPulse
		job.Driver = InternedHTTP
		return job, nil

	case *schema.PulseTCPConfig:
		job := getPulseTCPJob()
		job.Entity = jobID
		job.Host = cfg.Host
		job.Port = cfg.Port
		job.Timeout = timeout
		job.Retries = cfg.Retries
		job.JobType = InternedPulse
		job.Driver = InternedTCP
		return job, nil

	case *schema.PulseICMPConfig:
		job := getPulseICMPJob()
		job.Entity = jobID
		job.Host = cfg.Host
		job.Timeout = timeout
		job.Count = cfg.Count
		job.Retries = cfg.Retries
		job.IgnorePrivilege = cfg.Privilege
		job.JobType = InternedPulse
		job.Driver = InternedICMP
		return job, nil

	default:
		return nil, ErrUnknownPulseConfig
	}
}

// CreateInterventionJob creates a new intervention job based on the provided schema.
// Supports Docker interventions: restart, stop, start, kill, pause, unpause, scale.
//
// Jobs are obtained from sync.Pool for memory efficiency.
func CreateInterventionJob(interventionSchema schema.Intervention, jobID ecs.Entity) (Job, error) {
	retries := interventionSchema.Retries
	switch interventionSchema.Action {
	case "docker":
		target, ok := interventionSchema.Target.(*schema.InterventionTargetDocker)
		if !ok || target == nil {
			return nil, ErrDockerMissingTarget
		}

		// Route by target.Type (default to "restart" for backwards compatibility)
		switch target.Type {
		case "restart", "":
			job := getInterventionDockerJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Retries = retries
			job.Timeout = target.Timeout
			job.JobType = InternedIntervention
			job.Driver = InternedDocker
			return job, nil

		case "stop":
			job := getInterventionDockerStopJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Retries = retries
			job.Timeout = target.Timeout
			return job, nil

		case "start":
			job := getInterventionDockerStartJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Retries = retries
			job.Timeout = target.Timeout
			return job, nil

		case "kill":
			job := getInterventionDockerKillJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Signal = target.Signal
			job.Retries = retries
			return job, nil

		case "pause":
			job := getInterventionDockerPauseJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Retries = retries
			return job, nil

		case "unpause":
			job := getInterventionDockerUnpauseJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Retries = retries
			return job, nil

		case "scale":
			job := getInterventionDockerScaleJob()
			job.Entity = jobID
			job.Service = target.Service
			job.DockerHost = target.DockerHost
			job.Replicas = target.Replicas
			job.Retries = retries
			job.Timeout = target.Timeout
			return job, nil

		default:
			return nil, ErrUnknownDockerAction
		}

	default:
		return nil, ErrUnknownInterventionAction
	}
}

// CreateCodeJob creates a new code alert job based on the provided configuration.
// Supported notification types: log, pagerduty, slack, email, webhook.
//
// Jobs are obtained from sync.Pool for memory efficiency.
func CreateCodeJob(monitor string, config schema.CodeConfig, jobID ecs.Entity, color string) (Job, error) {
	template := codeAlertTemplateFor(color)
	colorValue := color
	monitorValue := monitor

	switch config.Notify {
	case "log":
		logJob := getCodeLogJob()
		logJob.Entity = jobID
		logJob.Monitor = monitorValue
		logJob.Color = colorValue
		logJob.Status = template.Status
		logJob.Severity = template.Severity
		logJob.Summary = template.Summary
		logJob.Action = template.Action
		logJob.NextSteps = template.NextSteps
		logJob.File = ""
		if logCfg, ok := config.Config.(*schema.CodeNotificationLog); ok && logCfg != nil {
			logJob.File = logCfg.File
		}
		return logJob, nil

	case "pagerduty":
		job := getCodePagerDutyJob()
		job.Entity = jobID
		job.Monitor = monitorValue
		job.Color = colorValue
		return job, nil

	case "slack":
		job := getCodeSlackJob()
		job.Entity = jobID
		job.Monitor = monitorValue
		job.Color = colorValue
		return job, nil

	case "email":
		job := getCodeEmailJob()
		job.Entity = jobID
		job.Monitor = monitorValue
		job.Color = colorValue
		return job, nil

	case "webhook":
		job := getCodeWebhookJob()
		job.Entity = jobID
		job.Monitor = monitorValue
		job.Color = colorValue
		return job, nil

	default:
		return nil, ErrUnknownCodeNotification
	}
}
