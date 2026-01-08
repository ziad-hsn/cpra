package jobs

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/interning"
	"cpra/internal/runtime/jobs/drivers"
	"cpra/internal/platform/loader/schema"
)

// DockerJobCreator is a factory function for Docker intervention jobs.
type DockerJobCreator func(target *schema.InterventionTargetDocker, jobID ecs.Entity, retries int) (Job, error)

// dockerJobCreators maps action names to their creator functions.
var dockerJobCreators = map[string]DockerJobCreator{
	"restart": func(t *schema.InterventionTargetDocker, id ecs.Entity, r int) (Job, error) {
		job := getInterventionDockerJob()
		job.Entity = id
		job.Container = t.Container
		job.DockerHost = t.DockerHost
		job.Retries = r
		job.Timeout = t.Timeout
		job.JobType = InternedIntervention
		job.Driver = InternedDocker
		return job, nil
	},
	"stop": func(t *schema.InterventionTargetDocker, id ecs.Entity, r int) (Job, error) {
		job := getInterventionDockerStopJob()
		job.Entity = id
		job.Container = t.Container
		job.DockerHost = t.DockerHost
		job.Retries = r
		job.Timeout = t.Timeout
		return job, nil
	},
	"start": func(t *schema.InterventionTargetDocker, id ecs.Entity, r int) (Job, error) {
		job := getInterventionDockerStartJob()
		job.Entity = id
		job.Container = t.Container
		job.DockerHost = t.DockerHost
		job.Retries = r
		job.Timeout = t.Timeout
		return job, nil
	},
	"kill": func(t *schema.InterventionTargetDocker, id ecs.Entity, r int) (Job, error) {
		job := getInterventionDockerKillJob()
		job.Entity = id
		job.Container = t.Container
		job.DockerHost = t.DockerHost
		job.Signal = t.Signal
		job.Retries = r
		return job, nil
	},
	"pause": func(t *schema.InterventionTargetDocker, id ecs.Entity, r int) (Job, error) {
		job := getInterventionDockerPauseJob()
		job.Entity = id
		job.Container = t.Container
		job.DockerHost = t.DockerHost
		job.Retries = r
		return job, nil
	},
	"unpause": func(t *schema.InterventionTargetDocker, id ecs.Entity, r int) (Job, error) {
		job := getInterventionDockerUnpauseJob()
		job.Entity = id
		job.Container = t.Container
		job.DockerHost = t.DockerHost
		job.Retries = r
		return job, nil
	},
	"scale": func(t *schema.InterventionTargetDocker, id ecs.Entity, r int) (Job, error) {
		job := getInterventionDockerScaleJob()
		job.Entity = id
		job.Service = t.Service
		job.DockerHost = t.DockerHost
		job.Replicas = t.Replicas
		job.Retries = r
		job.Timeout = t.Timeout
		return job, nil
	},
}

// CodeJobCreator is a factory function for Code alert jobs.
type CodeJobCreator func(monitor string, config schema.CodeConfig, jobID ecs.Entity, color string) (Job, error)

// codeJobCreators maps notification types to their creator functions.
var codeJobCreators = map[string]CodeJobCreator{
	"log": func(monitor string, config schema.CodeConfig, jobID ecs.Entity, color string) (Job, error) {
		template := codeAlertTemplateFor(color)
		logJob := getCodeLogJob()
		logJob.Entity = jobID
		logJob.Monitor = monitor
		logJob.Color = color
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
	},
	"pagerduty": createCodeNotificationJobCreator("pagerduty"),
	"slack":     createCodeNotificationJobCreator("slack"),
	"email":     createCodeNotificationJobCreator("email"),
	"webhook":   createCodeNotificationJobCreator("webhook"),
}

// createCodeNotificationJobCreator returns a factory function for a unified CodeNotificationJob
// with the specified driver. This eliminates code duplication across notification types.
func createCodeNotificationJobCreator(driverName string) CodeJobCreator {
	return func(monitor string, config schema.CodeConfig, jobID ecs.Entity, color string) (Job, error) {
		template := codeAlertTemplateFor(color)
		job := getCodeNotificationJob()
		job.Entity = jobID
		job.Monitor = monitor
		job.Color = color
		job.DriverName = driverName
		job.Driver = drivers.DefaultRegistry.Get(driverName)
		job.Status = template.Status
		job.Severity = template.Severity
		job.Summary = template.Summary
		job.Action = template.Action
		job.NextSteps = template.NextSteps
		job.Message = buildCodeNotificationMessage(monitor, template)
		return job, nil
	}
}

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
		// Route by target.Type using registry
		action := target.Type
		if action == "" {
			action = "restart" // default for backwards compatibility
		}

		if creator, ok := dockerJobCreators[action]; ok {
			return creator(target, jobID, retries)
		}
		return nil, ErrUnknownDockerAction

	default:
		return nil, ErrUnknownInterventionAction
	}
}

// CreateCodeJob creates a new code alert job based on the provided configuration.
// Supported notification types: log, pagerduty, slack, email, webhook.
//
// Jobs are obtained from sync.Pool for memory efficiency.
func CreateCodeJob(monitor string, config schema.CodeConfig, jobID ecs.Entity, color string) (Job, error) {
	if creator, ok := codeJobCreators[config.Notify]; ok {
		return creator(monitor, config, jobID, color)
	}
	return nil, ErrUnknownCodeNotification
}
