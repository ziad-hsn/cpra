package jobs

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
)

// Top-level factory functions for sync.Pool - eliminates closure allocations at init
func newPulseHTTPJob() any          { return &PulseHTTPJob{} }
func newPulseTCPJob() any           { return &PulseTCPJob{} }
func newPulseICMPJob() any          { return &PulseICMPJob{} }
func newInterventionDockerJob() any { return &InterventionDockerJob{} }
func newCodeLogJob() any            { return &CodeLogJob{} }
func newCodePagerDutyJob() any      { return &CodePagerDutyJob{} }
func newCodeSlackJob() any          { return &CodeSlackJob{} }
func newCodeEmailJob() any          { return &CodeEmailJob{} }
func newCodeWebhookJob() any        { return &CodeWebhookJob{} }

var (
	pulseHTTPJobPool = sync.Pool{New: newPulseHTTPJob}
	pulseTCPJobPool  = sync.Pool{New: newPulseTCPJob}
	pulseICMPJobPool = sync.Pool{New: newPulseICMPJob}

	interventionDockerJobPool = sync.Pool{New: newInterventionDockerJob}

	codeLogJobPool       = sync.Pool{New: newCodeLogJob}
	codePagerDutyJobPool = sync.Pool{New: newCodePagerDutyJob}
	codeSlackJobPool     = sync.Pool{New: newCodeSlackJob}
	codeEmailJobPool     = sync.Pool{New: newCodeEmailJob}
	codeWebhookJobPool   = sync.Pool{New: newCodeWebhookJob}
)

func getPulseHTTPJob() *PulseHTTPJob { return pulseHTTPJobPool.Get().(*PulseHTTPJob) }
func getPulseTCPJob() *PulseTCPJob   { return pulseTCPJobPool.Get().(*PulseTCPJob) }
func getPulseICMPJob() *PulseICMPJob { return pulseICMPJobPool.Get().(*PulseICMPJob) }

func getInterventionDockerJob() *InterventionDockerJob {
	return interventionDockerJobPool.Get().(*InterventionDockerJob)
}

func getCodeLogJob() *CodeLogJob             { return codeLogJobPool.Get().(*CodeLogJob) }
func getCodePagerDutyJob() *CodePagerDutyJob { return codePagerDutyJobPool.Get().(*CodePagerDutyJob) }
func getCodeSlackJob() *CodeSlackJob         { return codeSlackJobPool.Get().(*CodeSlackJob) }
func getCodeEmailJob() *CodeEmailJob         { return codeEmailJobPool.Get().(*CodeEmailJob) }
func getCodeWebhookJob() *CodeWebhookJob     { return codeWebhookJobPool.Get().(*CodeWebhookJob) }

// ReleasePulseJob returns a pulse job back to its pool.
func ReleasePulseJob(job Job) {
	switch j := job.(type) {
	case *PulseHTTPJob:
		resetPulseHTTPJob(j)
		pulseHTTPJobPool.Put(j)
	case *PulseTCPJob:
		resetPulseTCPJob(j)
		pulseTCPJobPool.Put(j)
	case *PulseICMPJob:
		resetPulseICMPJob(j)
		pulseICMPJobPool.Put(j)
	}
}

// ReleaseInterventionJob returns an intervention job back to its pool.
func ReleaseInterventionJob(job Job) {
	switch j := job.(type) {
	case *InterventionDockerJob:
		resetInterventionDockerJob(j)
		interventionDockerJobPool.Put(j)
	}
}

// ReleaseCodeJob returns a code job back to its pool.
func ReleaseCodeJob(job Job) {
	switch j := job.(type) {
	case *CodeLogJob:
		resetCodeLogJob(j)
		codeLogJobPool.Put(j)
	case *CodePagerDutyJob:
		resetCodePagerDutyJob(j)
		codePagerDutyJobPool.Put(j)
	case *CodeSlackJob:
		resetCodeSlackJob(j)
		codeSlackJobPool.Put(j)
	case *CodeEmailJob:
		resetCodeEmailJob(j)
		codeEmailJobPool.Put(j)
	case *CodeWebhookJob:
		resetCodeWebhookJob(j)
		codeWebhookJobPool.Put(j)
	}
}

func resetPulseHTTPJob(job *PulseHTTPJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	// DO NOT reset Client - it's shared and reused
	job.URL = ""
	job.Method = ""
	job.Timeout = 0
	job.Retries = 0
	job.Entity = ecs.Entity{}
	job.ID = uuid.Nil
	// JobType and Driver are set on creation, don't clear
}

func resetPulseTCPJob(job *PulseTCPJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Host = ""
	job.Port = 0
	job.Timeout = 0
	job.Retries = 0
	job.Entity = ecs.Entity{}
	job.ID = uuid.Nil
	// JobType and Driver are set on creation, don't clear
}

func resetPulseICMPJob(job *PulseICMPJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Host = ""
	job.Timeout = 0
	job.Count = 0
	job.Retries = 0
	job.Entity = ecs.Entity{}
	job.ID = uuid.Nil
	job.IgnorePrivilege = false
	// JobType and Driver are set on creation, don't clear
}

func resetInterventionDockerJob(job *InterventionDockerJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Container = ""
	job.Timeout = 0
	job.Retries = 0
	job.Entity = ecs.Entity{}
	job.ID = uuid.Nil
	// JobType and Driver are set on creation, don't clear
}

func resetCodeLogJob(job *CodeLogJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Status = ""
	job.Monitor = ""
	job.Color = ""
	job.Severity = ""
	job.Summary = ""
	job.Action = ""
	job.NextSteps = ""
	job.File = ""
	job.Entity = ecs.Entity{}
	job.ID = uuid.Nil
}

func resetCodePagerDutyJob(job *CodePagerDutyJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Monitor = ""
	job.Message = ""
	job.Color = ""
	job.Entity = ecs.Entity{}
	job.ID = uuid.Nil
}

func resetCodeSlackJob(job *CodeSlackJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Monitor = ""
	job.Message = ""
	job.Color = ""
	job.Entity = ecs.Entity{}
	job.ID = uuid.Nil
}

func resetCodeEmailJob(job *CodeEmailJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Monitor = ""
	job.Message = ""
	job.Color = ""
	job.Entity = ecs.Entity{}
	job.ID = uuid.Nil
}

func resetCodeWebhookJob(job *CodeWebhookJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Monitor = ""
	job.Message = ""
	job.Color = ""
	job.Entity = ecs.Entity{}
	job.ID = uuid.Nil
}
