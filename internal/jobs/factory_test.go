package jobs

import (
	"errors"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// TestCreatePulseJob_HTTP tests creating HTTP pulse jobs
func TestCreatePulseJob_HTTP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		config     schema.Pulse
		wantErr    error
		wantURL    string
		wantMethod string
		wantHost   string
		wantIsTLS  bool
	}{
		{
			name: "valid HTTP GET",
			config: schema.Pulse{
				Type:    "http",
				Timeout: 5 * time.Second,
				Config: &schema.PulseHTTPConfig{
					Url:     "http://example.com/health",
					Method:  "GET",
					Retries: 3,
				},
			},
			wantURL:    "http://example.com/health",
			wantMethod: "GET",
			wantHost:   "example.com:80",
			wantIsTLS:  false,
		},
		{
			name: "valid HTTPS POST",
			config: schema.Pulse{
				Type:    "http",
				Timeout: 10 * time.Second,
				Config: &schema.PulseHTTPConfig{
					Url:     "https://api.example.com/status",
					Method:  "POST",
					Retries: 2,
				},
			},
			wantURL:    "https://api.example.com/status",
			wantMethod: "POST",
			wantHost:   "api.example.com:443",
			wantIsTLS:  true,
		},
		{
			name: "HTTP with custom port",
			config: schema.Pulse{
				Type:    "http",
				Timeout: 5 * time.Second,
				Config: &schema.PulseHTTPConfig{
					Url:     "http://localhost:8080/api/health",
					Method:  "GET",
					Retries: 1,
				},
			},
			wantURL:    "http://localhost:8080/api/health",
			wantMethod: "GET",
			wantHost:   "localhost:8080",
			wantIsTLS:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entity := ecs.Entity{}
			job, err := CreatePulseJob(tt.config, entity)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			httpJob, ok := job.(*PulseHTTPJob)
			if !ok {
				t.Fatalf("expected *PulseHTTPJob, got %T", job)
			}

			if httpJob.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", httpJob.URL, tt.wantURL)
			}
			if httpJob.Method != tt.wantMethod {
				t.Errorf("Method = %q, want %q", httpJob.Method, tt.wantMethod)
			}
			if httpJob.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", httpJob.Host, tt.wantHost)
			}
			if httpJob.IsTLS != tt.wantIsTLS {
				t.Errorf("IsTLS = %v, want %v", httpJob.IsTLS, tt.wantIsTLS)
			}
			if httpJob.Entity != entity {
				t.Errorf("Entity = %v, want %v", httpJob.Entity, entity)
			}
			if httpJob.Timeout != tt.config.Timeout {
				t.Errorf("Timeout = %v, want %v", httpJob.Timeout, tt.config.Timeout)
			}
			if httpJob.JobType != InternedPulse {
				t.Errorf("JobType = %q, want %q", httpJob.JobType, InternedPulse)
			}
			if httpJob.Driver != InternedHTTP {
				t.Errorf("Driver = %q, want %q", httpJob.Driver, InternedHTTP)
			}

			// Cleanup: return job to pool
			ReleasePulseJob(job)
		})
	}
}

// TestCreatePulseJob_HTTP_MalformedURL tests HTTP job creation with truly malformed URLs
func TestCreatePulseJob_HTTP_MalformedURL(t *testing.T) {
	// Note: url.Parse is very lenient, so only truly malformed URLs error
	config := schema.Pulse{
		Type:    "http",
		Timeout: 5 * time.Second,
		Config: &schema.PulseHTTPConfig{
			Url:    "://invalid",
			Method: "GET",
		},
	}
	entity := ecs.Entity{}
	_, err := CreatePulseJob(config, entity)
	if err == nil {
		t.Fatal("expected error for malformed URL, got nil")
	}
}

// TestCreatePulseJob_TCP tests creating TCP pulse jobs
func TestCreatePulseJob_TCP(t *testing.T) {
	tests := []struct {
		name     string
		config   schema.Pulse
		wantHost string
		wantPort int
	}{
		{
			name: "valid TCP connection",
			config: schema.Pulse{
				Type:    "tcp",
				Timeout: 5 * time.Second,
				Config: &schema.PulseTCPConfig{
					Host:    "db.example.com",
					Port:    5432,
					Retries: 3,
				},
			},
			wantHost: "db.example.com",
			wantPort: 5432,
		},
		{
			name: "localhost TCP",
			config: schema.Pulse{
				Type:    "tcp",
				Timeout: 2 * time.Second,
				Config: &schema.PulseTCPConfig{
					Host:    "localhost",
					Port:    6379,
					Retries: 1,
				},
			},
			wantHost: "localhost",
			wantPort: 6379,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entity := ecs.Entity{}
			job, err := CreatePulseJob(tt.config, entity)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			tcpJob, ok := job.(*PulseTCPJob)
			if !ok {
				t.Fatalf("expected *PulseTCPJob, got %T", job)
			}

			if tcpJob.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", tcpJob.Host, tt.wantHost)
			}
			if tcpJob.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", tcpJob.Port, tt.wantPort)
			}
			if tcpJob.Entity != entity {
				t.Errorf("Entity = %v, want %v", tcpJob.Entity, entity)
			}
			if tcpJob.JobType != InternedPulse {
				t.Errorf("JobType = %q, want %q", tcpJob.JobType, InternedPulse)
			}
			if tcpJob.Driver != InternedTCP {
				t.Errorf("Driver = %q, want %q", tcpJob.Driver, InternedTCP)
			}

			ReleasePulseJob(job)
		})
	}
}

// TestCreatePulseJob_ICMP tests creating ICMP pulse jobs
func TestCreatePulseJob_ICMP(t *testing.T) {
	config := schema.Pulse{
		Type:    "icmp",
		Timeout: 5 * time.Second,
		Config: &schema.PulseICMPConfig{
			Host:      "gateway.example.com",
			Count:     3,
			Privilege: false,
			Retries:   2,
		},
	}
	entity := ecs.Entity{}

	job, err := CreatePulseJob(config, entity)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	icmpJob, ok := job.(*PulseICMPJob)
	if !ok {
		t.Fatalf("expected *PulseICMPJob, got %T", job)
	}

	if icmpJob.Host != "gateway.example.com" {
		t.Errorf("Host = %q, want %q", icmpJob.Host, "gateway.example.com")
	}
	if icmpJob.Count != 3 {
		t.Errorf("Count = %d, want %d", icmpJob.Count, 3)
	}
	if icmpJob.JobType != InternedPulse {
		t.Errorf("JobType = %q, want %q", icmpJob.JobType, InternedPulse)
	}
	if icmpJob.Driver != InternedICMP {
		t.Errorf("Driver = %q, want %q", icmpJob.Driver, InternedICMP)
	}

	ReleasePulseJob(job)
}

// TestCreatePulseJob_InvalidConfig tests that unknown config types return error
func TestCreatePulseJob_InvalidConfig(t *testing.T) {
	config := schema.Pulse{
		Type:    "unknown",
		Timeout: 5 * time.Second,
		Config:  nil,
	}
	entity := ecs.Entity{}

	_, err := CreatePulseJob(config, entity)
	if err == nil {
		t.Fatal("expected error for nil/unknown config, got nil")
	}
	if !errors.Is(err, ErrUnknownPulseConfig) {
		t.Errorf("expected ErrUnknownPulseConfig, got %v", err)
	}
}

// TestCreateInterventionJob_DockerRestart tests Docker restart intervention
func TestCreateInterventionJob_DockerRestart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		targetType    string
		wantContainer string
	}{
		{name: "explicit restart type", targetType: "restart", wantContainer: "my-container"},
		{name: "empty type defaults to restart", targetType: "", wantContainer: "another-container"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := schema.Intervention{
				Action:  "docker",
				Retries: 3,
				Target: &schema.InterventionTargetDocker{
					Type:       tt.targetType,
					Container:  tt.wantContainer,
					DockerHost: "unix:///var/run/docker.sock",
					Timeout:    30 * time.Second,
				},
			}
			entity := ecs.Entity{}

			job, err := CreateInterventionJob(config, entity)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			dockerJob, ok := job.(*InterventionDockerJob)
			if !ok {
				t.Fatalf("expected *InterventionDockerJob, got %T", job)
			}

			if dockerJob.Container != tt.wantContainer {
				t.Errorf("Container = %q, want %q", dockerJob.Container, tt.wantContainer)
			}
			if dockerJob.Entity != entity {
				t.Errorf("Entity = %v, want %v", dockerJob.Entity, entity)
			}
			if dockerJob.JobType != InternedIntervention {
				t.Errorf("JobType = %q, want %q", dockerJob.JobType, InternedIntervention)
			}
			if dockerJob.Driver != InternedDocker {
				t.Errorf("Driver = %q, want %q", dockerJob.Driver, InternedDocker)
			}

			ReleaseInterventionJob(job)
		})
	}
}

// TestCreateInterventionJob_DockerStop tests Docker stop intervention
func TestCreateInterventionJob_DockerStop(t *testing.T) {
	t.Parallel()
	config := schema.Intervention{
		Action:  "docker",
		Retries: 2,
		Target: &schema.InterventionTargetDocker{
			Type:       "stop",
			Container:  "stop-me",
			DockerHost: "tcp://docker:2375",
			Timeout:    10 * time.Second,
		},
	}
	entity := ecs.Entity{}

	job, err := CreateInterventionJob(config, entity)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stopJob, ok := job.(*InterventionDockerStopJob)
	if !ok {
		t.Fatalf("expected *InterventionDockerStopJob, got %T", job)
	}

	if stopJob.Container != "stop-me" {
		t.Errorf("Container = %q, want %q", stopJob.Container, "stop-me")
	}

	ReleaseInterventionJob(job)
}

// TestCreateInterventionJob_DockerStart tests Docker start intervention
func TestCreateInterventionJob_DockerStart(t *testing.T) {
	config := schema.Intervention{
		Action:  "docker",
		Retries: 1,
		Target: &schema.InterventionTargetDocker{
			Type:      "start",
			Container: "start-me",
		},
	}
	entity := ecs.Entity{}

	job, err := CreateInterventionJob(config, entity)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	startJob, ok := job.(*InterventionDockerStartJob)
	if !ok {
		t.Fatalf("expected *InterventionDockerStartJob, got %T", job)
	}

	if startJob.Container != "start-me" {
		t.Errorf("Container = %q, want %q", startJob.Container, "start-me")
	}

	ReleaseInterventionJob(job)
}

// TestCreateInterventionJob_DockerKill tests Docker kill intervention
func TestCreateInterventionJob_DockerKill(t *testing.T) {
	t.Parallel()
	config := schema.Intervention{
		Action:  "docker",
		Retries: 0,
		Target: &schema.InterventionTargetDocker{
			Type:      "kill",
			Container: "kill-me",
			Signal:    "SIGTERM",
		},
	}
	entity := ecs.Entity{}

	job, err := CreateInterventionJob(config, entity)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	killJob, ok := job.(*InterventionDockerKillJob)
	if !ok {
		t.Fatalf("expected *InterventionDockerKillJob, got %T", job)
	}

	if killJob.Container != "kill-me" {
		t.Errorf("Container = %q, want %q", killJob.Container, "kill-me")
	}
	if killJob.Signal != "SIGTERM" {
		t.Errorf("Signal = %q, want %q", killJob.Signal, "SIGTERM")
	}

	ReleaseInterventionJob(job)
}

// TestCreateInterventionJob_DockerPause tests Docker pause intervention
func TestCreateInterventionJob_DockerPause(t *testing.T) {
	t.Parallel()
	config := schema.Intervention{
		Action: "docker",
		Target: &schema.InterventionTargetDocker{Type: "pause", Container: "pause-me"},
	}
	job, err := CreateInterventionJob(config, ecs.Entity{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := job.(*InterventionDockerPauseJob); !ok {
		t.Fatalf("expected *InterventionDockerPauseJob, got %T", job)
	}
	ReleaseInterventionJob(job)
}

// TestCreateInterventionJob_DockerUnpause tests Docker unpause intervention
func TestCreateInterventionJob_DockerUnpause(t *testing.T) {
	config := schema.Intervention{
		Action: "docker",
		Target: &schema.InterventionTargetDocker{Type: "unpause", Container: "unpause-me"},
	}
	job, err := CreateInterventionJob(config, ecs.Entity{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := job.(*InterventionDockerUnpauseJob); !ok {
		t.Fatalf("expected *InterventionDockerUnpauseJob, got %T", job)
	}
	ReleaseInterventionJob(job)
}

// TestCreateInterventionJob_DockerScale tests Docker scale intervention
func TestCreateInterventionJob_DockerScale(t *testing.T) {
	t.Parallel()
	config := schema.Intervention{
		Action:  "docker",
		Retries: 3,
		Target: &schema.InterventionTargetDocker{
			Type:     "scale",
			Service:  "my-service",
			Replicas: 5,
			Timeout:  60 * time.Second,
		},
	}
	entity := ecs.Entity{}

	job, err := CreateInterventionJob(config, entity)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	scaleJob, ok := job.(*InterventionDockerScaleJob)
	if !ok {
		t.Fatalf("expected *InterventionDockerScaleJob, got %T", job)
	}

	if scaleJob.Service != "my-service" {
		t.Errorf("Service = %q, want %q", scaleJob.Service, "my-service")
	}
	if scaleJob.Replicas != 5 {
		t.Errorf("Replicas = %d, want %d", scaleJob.Replicas, 5)
	}

	ReleaseInterventionJob(job)
}

// TestCreateInterventionJob_UnknownAction tests unknown Docker action type
func TestCreateInterventionJob_UnknownAction(t *testing.T) {
	t.Parallel()
	config := schema.Intervention{
		Action: "docker",
		Target: &schema.InterventionTargetDocker{Type: "unknown-action", Container: "test"},
	}

	_, err := CreateInterventionJob(config, ecs.Entity{})
	if err == nil {
		t.Fatal("expected error for unknown docker action, got nil")
	}
	if !errors.Is(err, ErrUnknownDockerAction) {
		t.Errorf("expected ErrUnknownDockerAction, got %v", err)
	}
}

// TestCreateInterventionJob_UnknownInterventionAction tests unknown intervention action
func TestCreateInterventionJob_UnknownInterventionAction(t *testing.T) {
	config := schema.Intervention{Action: "unknown", Retries: 1}

	_, err := CreateInterventionJob(config, ecs.Entity{})
	if err == nil {
		t.Fatal("expected error for unknown intervention action, got nil")
	}
	if !errors.Is(err, ErrUnknownInterventionAction) {
		t.Errorf("expected ErrUnknownInterventionAction, got %v", err)
	}
}

// TestCreateInterventionJob_MissingTarget tests docker intervention with nil target
func TestCreateInterventionJob_MissingTarget(t *testing.T) {
	t.Parallel()
	config := schema.Intervention{Action: "docker", Retries: 1, Target: nil}

	_, err := CreateInterventionJob(config, ecs.Entity{})
	if err == nil {
		t.Fatal("expected error for missing target, got nil")
	}
	if !errors.Is(err, ErrDockerMissingTarget) {
		t.Errorf("expected ErrDockerMissingTarget, got %v", err)
	}
}

// TestCreateCodeJob_Log tests creating log notification jobs
func TestCreateCodeJob_Log(t *testing.T) {
	t.Parallel()
	config := schema.CodeConfig{
		Notify:   "log",
		Dispatch: true,
		Config:   &schema.CodeNotificationLog{File: "/var/log/alerts.log"},
	}
	entity := ecs.Entity{}

	job, err := CreateCodeJob("api-server", config, entity, "red")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logJob, ok := job.(*CodeLogJob)
	if !ok {
		t.Fatalf("expected *CodeLogJob, got %T", job)
	}

	if logJob.Monitor != "api-server" {
		t.Errorf("Monitor = %q, want %q", logJob.Monitor, "api-server")
	}
	if logJob.Color != "red" {
		t.Errorf("Color = %q, want %q", logJob.Color, "red")
	}
	if logJob.File != "/var/log/alerts.log" {
		t.Errorf("File = %q, want %q", logJob.File, "/var/log/alerts.log")
	}

	ReleaseCodeJob(job)
}

// TestCreateCodeJob_PagerDuty tests creating PagerDuty notification jobs
func TestCreateCodeJob_PagerDuty(t *testing.T) {
	config := schema.CodeConfig{Notify: "pagerduty", Dispatch: true}
	job, err := CreateCodeJob("critical-service", config, ecs.Entity{}, "red")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := job.(*CodePagerDutyJob); !ok {
		t.Fatalf("expected *CodePagerDutyJob, got %T", job)
	}
	ReleaseCodeJob(job)
}

// TestCreateCodeJob_Slack tests creating Slack notification jobs
func TestCreateCodeJob_Slack(t *testing.T) {
	t.Parallel()
	config := schema.CodeConfig{Notify: "slack", Dispatch: true}
	job, err := CreateCodeJob("web-app", config, ecs.Entity{}, "yellow")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := job.(*CodeSlackJob); !ok {
		t.Fatalf("expected *CodeSlackJob, got %T", job)
	}
	ReleaseCodeJob(job)
}

// TestCreateCodeJob_Email tests creating email notification jobs
func TestCreateCodeJob_Email(t *testing.T) {
	t.Parallel()
	config := schema.CodeConfig{Notify: "email", Dispatch: true}
	job, err := CreateCodeJob("database", config, ecs.Entity{}, "red")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := job.(*CodeEmailJob); !ok {
		t.Fatalf("expected *CodeEmailJob, got %T", job)
	}
	ReleaseCodeJob(job)
}

// TestCreateCodeJob_Webhook tests creating webhook notification jobs
func TestCreateCodeJob_Webhook(t *testing.T) {
	config := schema.CodeConfig{Notify: "webhook", Dispatch: true}
	job, err := CreateCodeJob("microservice", config, ecs.Entity{}, "green")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := job.(*CodeWebhookJob); !ok {
		t.Fatalf("expected *CodeWebhookJob, got %T", job)
	}
	ReleaseCodeJob(job)
}

// TestCreateCodeJob_UnknownNotification tests unknown notification type
func TestCreateCodeJob_UnknownNotification(t *testing.T) {
	t.Parallel()
	config := schema.CodeConfig{Notify: "sms"}

	_, err := CreateCodeJob("test", config, ecs.Entity{}, "red")
	if err == nil {
		t.Fatal("expected error for unknown notification type, got nil")
	}
	if !errors.Is(err, ErrUnknownCodeNotification) {
		t.Errorf("expected ErrUnknownCodeNotification, got %v", err)
	}
}
