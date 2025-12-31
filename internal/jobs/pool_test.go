package jobs

import (
	"sync"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// TestPoolGetPut_PulseHTTPJob tests HTTP job pool get/put cycle
func TestPoolGetPut_PulseHTTPJob(t *testing.T) {
	t.Parallel()
	job1 := getPulseHTTPJob()
	if job1 == nil {
		t.Fatal("expected non-nil job from pool")
	}

	// Set some fields
	job1.URL = "http://example.com"
	job1.Method = "GET"
	job1.Host = "example.com:80"
	job1.Retries = 3
	job1.Timeout = 5 * time.Second
	job1.SetEnqueueTime(time.Now())

	// Put back to pool
	ReleasePulseJob(job1)

	// Get another job - should be cleaned
	job2 := getPulseHTTPJob()
	if job2.URL != "" {
		t.Errorf("URL should be reset, got %q", job2.URL)
	}
	if job2.Method != "" {
		t.Errorf("Method should be reset, got %q", job2.Method)
	}
	if job2.Host != "" {
		t.Errorf("Host should be reset, got %q", job2.Host)
	}
	if job2.Retries != 0 {
		t.Errorf("Retries should be reset, got %d", job2.Retries)
	}
	if job2.Timeout != 0 {
		t.Errorf("Timeout should be reset, got %v", job2.Timeout)
	}
	if !job2.GetEnqueueTime().IsZero() {
		t.Errorf("EnqueueTime should be reset, got %v", job2.GetEnqueueTime())
	}

	ReleasePulseJob(job2)
}

// TestPoolGetPut_PulseTCPJob tests TCP job pool get/put cycle
func TestPoolGetPut_PulseTCPJob(t *testing.T) {
	t.Parallel()
	job1 := getPulseTCPJob()
	if job1 == nil {
		t.Fatal("expected non-nil job from pool")
	}

	job1.Host = "db.example.com"
	job1.Port = 5432
	job1.Retries = 2
	job1.Timeout = 10 * time.Second

	ReleasePulseJob(job1)

	job2 := getPulseTCPJob()
	if job2.Host != "" {
		t.Errorf("Host should be reset, got %q", job2.Host)
	}
	if job2.Port != 0 {
		t.Errorf("Port should be reset, got %d", job2.Port)
	}
	if job2.Retries != 0 {
		t.Errorf("Retries should be reset, got %d", job2.Retries)
	}

	ReleasePulseJob(job2)
}

// TestPoolGetPut_PulseICMPJob tests ICMP job pool get/put cycle
func TestPoolGetPut_PulseICMPJob(t *testing.T) {
	t.Parallel()
	job1 := getPulseICMPJob()
	if job1 == nil {
		t.Fatal("expected non-nil job from pool")
	}

	job1.Host = "gateway.example.com"
	job1.Count = 3
	job1.IgnorePrivilege = true
	job1.Retries = 1

	ReleasePulseJob(job1)

	job2 := getPulseICMPJob()
	if job2.Host != "" {
		t.Errorf("Host should be reset, got %q", job2.Host)
	}
	if job2.Count != 0 {
		t.Errorf("Count should be reset, got %d", job2.Count)
	}
	if job2.IgnorePrivilege {
		t.Error("IgnorePrivilege should be reset to false")
	}

	ReleasePulseJob(job2)
}

// TestPoolGetPut_InterventionDockerJob tests Docker restart job pool
func TestPoolGetPut_InterventionDockerJob(t *testing.T) {
	t.Parallel()
	job1 := getInterventionDockerJob()
	if job1 == nil {
		t.Fatal("expected non-nil job from pool")
	}

	job1.Container = "my-container"
	job1.DockerHost = "unix:///var/run/docker.sock"
	job1.Retries = 3
	job1.Timeout = 30 * time.Second

	ReleaseInterventionJob(job1)

	job2 := getInterventionDockerJob()
	if job2.Container != "" {
		t.Errorf("Container should be reset, got %q", job2.Container)
	}
	if job2.DockerHost != "" {
		t.Errorf("DockerHost should be reset, got %q", job2.DockerHost)
	}
	if job2.Retries != 0 {
		t.Errorf("Retries should be reset, got %d", job2.Retries)
	}
	if job2.Timeout != 0 {
		t.Errorf("Timeout should be reset, got %v", job2.Timeout)
	}

	ReleaseInterventionJob(job2)
}

// TestPoolGetPut_InterventionDockerStopJob tests Docker stop job pool
func TestPoolGetPut_InterventionDockerStopJob(t *testing.T) {
	t.Parallel()
	job1 := getInterventionDockerStopJob()
	job1.Container = "stop-container"
	job1.DockerHost = "tcp://docker:2375"

	ReleaseInterventionJob(job1)

	job2 := getInterventionDockerStopJob()
	if job2.Container != "" {
		t.Errorf("Container should be reset, got %q", job2.Container)
	}
	ReleaseInterventionJob(job2)
}

// TestPoolGetPut_InterventionDockerStartJob tests Docker start job pool
func TestPoolGetPut_InterventionDockerStartJob(t *testing.T) {
	t.Parallel()
	job1 := getInterventionDockerStartJob()
	job1.Container = "start-container"

	ReleaseInterventionJob(job1)

	job2 := getInterventionDockerStartJob()
	if job2.Container != "" {
		t.Errorf("Container should be reset, got %q", job2.Container)
	}
	ReleaseInterventionJob(job2)
}

// TestPoolGetPut_InterventionDockerKillJob tests Docker kill job pool
func TestPoolGetPut_InterventionDockerKillJob(t *testing.T) {
	t.Parallel()
	job1 := getInterventionDockerKillJob()
	job1.Container = "kill-container"
	job1.Signal = "SIGTERM"

	ReleaseInterventionJob(job1)

	job2 := getInterventionDockerKillJob()
	if job2.Container != "" {
		t.Errorf("Container should be reset, got %q", job2.Container)
	}
	if job2.Signal != "" {
		t.Errorf("Signal should be reset, got %q", job2.Signal)
	}
	ReleaseInterventionJob(job2)
}

// TestPoolGetPut_InterventionDockerPauseJob tests Docker pause job pool
func TestPoolGetPut_InterventionDockerPauseJob(t *testing.T) {
	t.Parallel()
	job1 := getInterventionDockerPauseJob()
	job1.Container = "pause-container"

	ReleaseInterventionJob(job1)

	job2 := getInterventionDockerPauseJob()
	if job2.Container != "" {
		t.Errorf("Container should be reset, got %q", job2.Container)
	}
	ReleaseInterventionJob(job2)
}

// TestPoolGetPut_InterventionDockerUnpauseJob tests Docker unpause job pool
func TestPoolGetPut_InterventionDockerUnpauseJob(t *testing.T) {
	t.Parallel()
	job1 := getInterventionDockerUnpauseJob()
	job1.Container = "unpause-container"

	ReleaseInterventionJob(job1)

	job2 := getInterventionDockerUnpauseJob()
	if job2.Container != "" {
		t.Errorf("Container should be reset, got %q", job2.Container)
	}
	ReleaseInterventionJob(job2)
}

// TestPoolGetPut_InterventionDockerScaleJob tests Docker scale job pool
func TestPoolGetPut_InterventionDockerScaleJob(t *testing.T) {
	t.Parallel()
	job1 := getInterventionDockerScaleJob()
	job1.Service = "my-service"
	job1.Replicas = 5
	job1.Timeout = 60 * time.Second

	ReleaseInterventionJob(job1)

	job2 := getInterventionDockerScaleJob()
	if job2.Service != "" {
		t.Errorf("Service should be reset, got %q", job2.Service)
	}
	if job2.Replicas != 0 {
		t.Errorf("Replicas should be reset, got %d", job2.Replicas)
	}
	if job2.Timeout != 0 {
		t.Errorf("Timeout should be reset, got %v", job2.Timeout)
	}
	ReleaseInterventionJob(job2)
}

// TestPoolGetPut_CodeLogJob tests log notification job pool
func TestPoolGetPut_CodeLogJob(t *testing.T) {
	t.Parallel()
	job1 := getCodeLogJob()
	job1.Monitor = "api-server"
	job1.Color = "red"
	job1.Status = "CRITICAL"
	job1.File = "/var/log/alerts.log"

	ReleaseCodeJob(job1)

	job2 := getCodeLogJob()
	if job2.Monitor != "" {
		t.Errorf("Monitor should be reset, got %q", job2.Monitor)
	}
	if job2.Color != "" {
		t.Errorf("Color should be reset, got %q", job2.Color)
	}
	if job2.Status != "" {
		t.Errorf("Status should be reset, got %q", job2.Status)
	}
	if job2.File != "" {
		t.Errorf("File should be reset, got %q", job2.File)
	}

	ReleaseCodeJob(job2)
}

// TestPoolGetPut_CodePagerDutyJob tests PagerDuty job pool
func TestPoolGetPut_CodePagerDutyJob(t *testing.T) {
	t.Parallel()
	job1 := getCodePagerDutyJob()
	job1.Monitor = "critical-service"
	job1.Color = "red"
	job1.Message = "Service is down"

	ReleaseCodeJob(job1)

	job2 := getCodePagerDutyJob()
	if job2.Monitor != "" {
		t.Errorf("Monitor should be reset, got %q", job2.Monitor)
	}
	if job2.Message != "" {
		t.Errorf("Message should be reset, got %q", job2.Message)
	}

	ReleaseCodeJob(job2)
}

// TestPoolGetPut_CodeSlackJob tests Slack job pool
func TestPoolGetPut_CodeSlackJob(t *testing.T) {
	t.Parallel()
	job1 := getCodeSlackJob()
	job1.Monitor = "web-app"
	job1.Message = "Alert triggered"

	ReleaseCodeJob(job1)

	job2 := getCodeSlackJob()
	if job2.Monitor != "" {
		t.Errorf("Monitor should be reset, got %q", job2.Monitor)
	}

	ReleaseCodeJob(job2)
}

// TestPoolGetPut_CodeEmailJob tests email job pool
func TestPoolGetPut_CodeEmailJob(t *testing.T) {
	t.Parallel()
	job1 := getCodeEmailJob()
	job1.Monitor = "database"
	job1.Color = "yellow"

	ReleaseCodeJob(job1)

	job2 := getCodeEmailJob()
	if job2.Monitor != "" {
		t.Errorf("Monitor should be reset, got %q", job2.Monitor)
	}

	ReleaseCodeJob(job2)
}

// TestPoolGetPut_CodeWebhookJob tests webhook job pool
func TestPoolGetPut_CodeWebhookJob(t *testing.T) {
	t.Parallel()
	job1 := getCodeWebhookJob()
	job1.Monitor = "microservice"
	job1.Color = "green"

	ReleaseCodeJob(job1)

	job2 := getCodeWebhookJob()
	if job2.Monitor != "" {
		t.Errorf("Monitor should be reset, got %q", job2.Monitor)
	}

	ReleaseCodeJob(job2)
}

// TestResetPulseHTTPJob_Nil tests reset with nil job (should not panic)
func TestResetPulseHTTPJob_Nil(t *testing.T) {
	t.Parallel()
	// Should not panic
	resetPulseHTTPJob(nil)
}

// TestResetPulseTCPJob_Nil tests reset with nil job
func TestResetPulseTCPJob_Nil(t *testing.T) {
	t.Parallel()
	resetPulseTCPJob(nil)
}

// TestResetPulseICMPJob_Nil tests reset with nil job
func TestResetPulseICMPJob_Nil(t *testing.T) {
	t.Parallel()
	resetPulseICMPJob(nil)
}

// TestResetInterventionDockerJob_Nil tests reset with nil job
func TestResetInterventionDockerJob_Nil(t *testing.T) {
	t.Parallel()
	resetInterventionDockerJob(nil)
}

// TestResetCodeLogJob_Nil tests reset with nil job
func TestResetCodeLogJob_Nil(t *testing.T) {
	t.Parallel()
	resetCodeLogJob(nil)
}

// TestReleasePulseJob_WrongType tests release with wrong type (should be no-op)
func TestReleasePulseJob_WrongType(t *testing.T) {
	t.Parallel()
	// Passing nil should not panic
	ReleasePulseJob(nil)

	// Passing a different job type should be silently ignored
	dockerJob := getInterventionDockerJob()
	dockerJob.Container = "test"
	ReleasePulseJob(dockerJob) // Wrong release function
	// dockerJob fields should NOT be reset since it went to wrong release
	if dockerJob.Container != "test" {
		t.Error("Job should not be reset when passed to wrong release function")
	}
	ReleaseInterventionJob(dockerJob) // Proper cleanup
}

// TestPoolConcurrency_PulseHTTP tests concurrent pool access
func TestPoolConcurrency_PulseHTTP(t *testing.T) {
	t.Parallel()
	const numGoroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				job := getPulseHTTPJob()
				job.URL = "http://example.com"
				job.Method = "GET"
				job.Retries = id
				// Simulate some work
				ReleasePulseJob(job)
			}
		}(i)
	}

	wg.Wait()
}

// TestPoolConcurrency_InterventionDocker tests concurrent pool access
func TestPoolConcurrency_InterventionDocker(t *testing.T) {
	t.Parallel()
	const numGoroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				job := getInterventionDockerJob()
				job.Container = "test-container"
				job.Retries = id
				ReleaseInterventionJob(job)
			}
		}(i)
	}

	wg.Wait()
}

// TestPoolConcurrency_CodeLog tests concurrent pool access
func TestPoolConcurrency_CodeLog(t *testing.T) {
	t.Parallel()
	const numGoroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				job := getCodeLogJob()
				job.Monitor = "test-monitor"
				job.Color = "red"
				ReleaseCodeJob(job)
			}
		}(i)
	}

	wg.Wait()
}

// TestResetJob_ClearsEntity tests that entity is properly cleared on reset
func TestResetJob_ClearsEntity(t *testing.T) {
	t.Parallel()
	job := getPulseHTTPJob()
	// Create a mock entity-like value (can't create real entity without world)
	job.Entity = ecs.Entity{}
	job.URL = "http://test.com"

	ReleasePulseJob(job)

	job2 := getPulseHTTPJob()
	if job2.Entity.ID() != 0 && job2.Entity.Gen() != 0 {
		t.Error("Entity should be cleared after reset")
	}
	if job2.URL != "" {
		t.Errorf("URL should be reset, got %q", job2.URL)
	}

	ReleasePulseJob(job2)
}

// TestResetJob_ClearsTimestamps tests that timestamps are properly cleared
func TestResetJob_ClearsTimestamps(t *testing.T) {
	t.Parallel()
	job := getPulseHTTPJob()
	now := time.Now()
	job.SetEnqueueTime(now)
	job.SetStartTime(now)

	ReleasePulseJob(job)

	job2 := getPulseHTTPJob()
	if !job2.GetEnqueueTime().IsZero() {
		t.Error("EnqueueTime should be zero after reset")
	}
	if !job2.GetStartTime().IsZero() {
		t.Error("StartTime should be zero after reset")
	}

	ReleasePulseJob(job2)
}

// TestAllInterventionDockerJobTypes tests all Docker intervention job types
func TestAllInterventionDockerJobTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		getJob  func() Job
		setData func(Job)
	}{
		{
			name:   "DockerStop",
			getJob: func() Job { return getInterventionDockerStopJob() },
			setData: func(j Job) {
				job := j.(*InterventionDockerStopJob)
				job.Container = "test"
			},
		},
		{
			name:   "DockerStart",
			getJob: func() Job { return getInterventionDockerStartJob() },
			setData: func(j Job) {
				job := j.(*InterventionDockerStartJob)
				job.Container = "test"
			},
		},
		{
			name:   "DockerKill",
			getJob: func() Job { return getInterventionDockerKillJob() },
			setData: func(j Job) {
				job := j.(*InterventionDockerKillJob)
				job.Container = "test"
				job.Signal = "SIGTERM"
			},
		},
		{
			name:   "DockerPause",
			getJob: func() Job { return getInterventionDockerPauseJob() },
			setData: func(j Job) {
				job := j.(*InterventionDockerPauseJob)
				job.Container = "test"
			},
		},
		{
			name:   "DockerUnpause",
			getJob: func() Job { return getInterventionDockerUnpauseJob() },
			setData: func(j Job) {
				job := j.(*InterventionDockerUnpauseJob)
				job.Container = "test"
			},
		},
		{
			name:   "DockerScale",
			getJob: func() Job { return getInterventionDockerScaleJob() },
			setData: func(j Job) {
				job := j.(*InterventionDockerScaleJob)
				job.Service = "test"
				job.Replicas = 5
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := tt.getJob()
			if job == nil {
				t.Fatal("expected non-nil job")
			}
			tt.setData(job)
			ReleaseInterventionJob(job)
		})
	}
}

// ============================================================================
// POOL POLLUTION DETECTION TESTS
// ============================================================================

// TestPool_DetectsPollution_PulseHTTP tests that pool properly resets HTTP jobs
func TestPool_DetectsPollution_PulseHTTP(t *testing.T) {
	t.Parallel()
	const poisonURL = "http://poison.malicious.com"
	const poisonMethod = "POISON"

	// Poison the pool
	job := getPulseHTTPJob()
	job.URL = poisonURL
	job.Method = poisonMethod
	job.Host = "poison.host:666"
	job.IsTLS = true
	job.Timeout = 999 * time.Second
	job.Retries = 999
	job.SetEnqueueTime(time.Now())
	ReleasePulseJob(job)

	// Get multiple jobs and verify none are polluted
	for i := 0; i < 10; i++ {
		job := getPulseHTTPJob()
		if job.URL == poisonURL {
			t.Fatal("Pool pollution detected: URL was not reset")
		}
		if job.Method == poisonMethod {
			t.Fatal("Pool pollution detected: Method was not reset")
		}
		if job.Host != "" {
			t.Errorf("Pool pollution detected: Host=%q should be empty", job.Host)
		}
		if job.IsTLS {
			t.Error("Pool pollution detected: IsTLS should be false")
		}
		if job.Timeout != 0 {
			t.Errorf("Pool pollution detected: Timeout=%v should be 0", job.Timeout)
		}
		if job.Retries != 0 {
			t.Errorf("Pool pollution detected: Retries=%d should be 0", job.Retries)
		}
		if !job.GetEnqueueTime().IsZero() {
			t.Error("Pool pollution detected: EnqueueTime should be zero")
		}
		ReleasePulseJob(job)
	}
}

// TestPool_DetectsPollution_PulseTCP tests that pool properly resets TCP jobs
func TestPool_DetectsPollution_PulseTCP(t *testing.T) {
	t.Parallel()
	const poisonHost = "poison.evil.com"
	const poisonPort = 66666

	job := getPulseTCPJob()
	job.Host = poisonHost
	job.Port = poisonPort
	job.Timeout = 888 * time.Second
	job.Retries = 888
	ReleasePulseJob(job)

	for i := 0; i < 10; i++ {
		job := getPulseTCPJob()
		if job.Host == poisonHost {
			t.Fatal("Pool pollution detected: Host was not reset")
		}
		if job.Port == poisonPort {
			t.Fatal("Pool pollution detected: Port was not reset")
		}
		if job.Port != 0 {
			t.Errorf("Pool pollution detected: Port=%d should be 0", job.Port)
		}
		if job.Timeout != 0 {
			t.Errorf("Pool pollution detected: Timeout=%v should be 0", job.Timeout)
		}
		ReleasePulseJob(job)
	}
}

// TestPool_DetectsPollution_PulseICMP tests that pool properly resets ICMP jobs
func TestPool_DetectsPollution_PulseICMP(t *testing.T) {
	t.Parallel()
	const poisonHost = "poison.icmp.com"

	job := getPulseICMPJob()
	job.Host = poisonHost
	job.Count = 777
	job.IgnorePrivilege = true
	job.Retries = 777
	ReleasePulseJob(job)

	for i := 0; i < 10; i++ {
		job := getPulseICMPJob()
		if job.Host == poisonHost {
			t.Fatal("Pool pollution detected: Host was not reset")
		}
		if job.Count != 0 {
			t.Errorf("Pool pollution detected: Count=%d should be 0", job.Count)
		}
		if job.IgnorePrivilege {
			t.Error("Pool pollution detected: IgnorePrivilege should be false")
		}
		ReleasePulseJob(job)
	}
}

// TestPool_DetectsPollution_InterventionDocker tests Docker job pollution
func TestPool_DetectsPollution_InterventionDocker(t *testing.T) {
	t.Parallel()
	const poisonContainer = "poison-container-do-not-use"
	const poisonHost = "tcp://poison:2375"

	job := getInterventionDockerJob()
	job.Container = poisonContainer
	job.DockerHost = poisonHost
	job.Timeout = 666 * time.Second
	job.Retries = 666
	ReleaseInterventionJob(job)

	for i := 0; i < 10; i++ {
		job := getInterventionDockerJob()
		if job.Container == poisonContainer {
			t.Fatal("Pool pollution detected: Container was not reset")
		}
		if job.DockerHost == poisonHost {
			t.Fatal("Pool pollution detected: DockerHost was not reset")
		}
		if job.Container != "" {
			t.Errorf("Pool pollution detected: Container=%q should be empty", job.Container)
		}
		if job.DockerHost != "" {
			t.Errorf("Pool pollution detected: DockerHost=%q should be empty", job.DockerHost)
		}
		ReleaseInterventionJob(job)
	}
}

// TestPool_DetectsPollution_CodeLog tests CodeLog job pollution
func TestPool_DetectsPollution_CodeLog(t *testing.T) {
	t.Parallel()
	const poisonMonitor = "POISON_MONITOR"
	const poisonFile = "/dev/null/poison"

	job := getCodeLogJob()
	job.Monitor = poisonMonitor
	job.Color = "poison"
	job.Status = "POISON"
	job.Severity = "POISON"
	job.Summary = "POISON SUMMARY"
	job.Action = "POISON ACTION"
	job.NextSteps = "POISON STEPS"
	job.File = poisonFile
	ReleaseCodeJob(job)

	for i := 0; i < 10; i++ {
		job := getCodeLogJob()
		if job.Monitor == poisonMonitor {
			t.Fatal("Pool pollution detected: Monitor was not reset")
		}
		if job.File == poisonFile {
			t.Fatal("Pool pollution detected: File was not reset")
		}
		// Check all string fields
		stringFields := map[string]string{
			"Monitor":   job.Monitor,
			"Color":     job.Color,
			"Status":    job.Status,
			"Severity":  job.Severity,
			"Summary":   job.Summary,
			"Action":    job.Action,
			"NextSteps": job.NextSteps,
			"File":      job.File,
		}
		for field, value := range stringFields {
			if value != "" {
				t.Errorf("Pool pollution detected: %s=%q should be empty", field, value)
			}
		}
		ReleaseCodeJob(job)
	}
}

// TestPool_ExhaustiveReset_PulseHTTP verifies ALL fields are reset
func TestPool_ExhaustiveReset_PulseHTTP(t *testing.T) {
	t.Parallel()

	job := getPulseHTTPJob()
	now := time.Now()

	// Set every single field
	job.EnqueueTime = now
	job.StartTime = now
	job.URL = "http://test.com/path?query=value"
	job.Method = "POST"
	job.Host = "test.com:8080"
	job.IsTLS = true
	job.Timeout = 30 * time.Second
	job.Retries = 5
	job.Entity = ecs.Entity{}

	ReleasePulseJob(job)
	job2 := getPulseHTTPJob()

	// Verify EVERY field is reset
	if !job2.EnqueueTime.IsZero() {
		t.Errorf("EnqueueTime not reset: %v", job2.EnqueueTime)
	}
	if !job2.StartTime.IsZero() {
		t.Errorf("StartTime not reset: %v", job2.StartTime)
	}
	if job2.URL != "" {
		t.Errorf("URL not reset: %q", job2.URL)
	}
	if job2.Method != "" {
		t.Errorf("Method not reset: %q", job2.Method)
	}
	if job2.Host != "" {
		t.Errorf("Host not reset: %q", job2.Host)
	}
	if job2.IsTLS {
		t.Error("IsTLS not reset")
	}
	if job2.Timeout != 0 {
		t.Errorf("Timeout not reset: %v", job2.Timeout)
	}
	if job2.Retries != 0 {
		t.Errorf("Retries not reset: %d", job2.Retries)
	}

	ReleasePulseJob(job2)
}

// TestPool_ExhaustiveReset_CodeLogJob verifies ALL CodeLog fields are reset
func TestPool_ExhaustiveReset_CodeLogJob(t *testing.T) {
	t.Parallel()

	job := getCodeLogJob()
	now := time.Now()

	// Set every single field
	job.EnqueueTime = now
	job.StartTime = now
	job.Status = "CRITICAL"
	job.Monitor = "test-monitor"
	job.Color = "red"
	job.Severity = "high"
	job.Summary = "Test summary"
	job.Action = "Test action"
	job.NextSteps = "Test next steps"
	job.File = "/var/log/test.log"
	job.Entity = ecs.Entity{}

	ReleaseCodeJob(job)
	job2 := getCodeLogJob()

	// Verify EVERY field is reset
	if !job2.EnqueueTime.IsZero() {
		t.Errorf("EnqueueTime not reset: %v", job2.EnqueueTime)
	}
	if !job2.StartTime.IsZero() {
		t.Errorf("StartTime not reset: %v", job2.StartTime)
	}
	if job2.Status != "" {
		t.Errorf("Status not reset: %q", job2.Status)
	}
	if job2.Monitor != "" {
		t.Errorf("Monitor not reset: %q", job2.Monitor)
	}
	if job2.Color != "" {
		t.Errorf("Color not reset: %q", job2.Color)
	}
	if job2.Severity != "" {
		t.Errorf("Severity not reset: %q", job2.Severity)
	}
	if job2.Summary != "" {
		t.Errorf("Summary not reset: %q", job2.Summary)
	}
	if job2.Action != "" {
		t.Errorf("Action not reset: %q", job2.Action)
	}
	if job2.NextSteps != "" {
		t.Errorf("NextSteps not reset: %q", job2.NextSteps)
	}
	if job2.File != "" {
		t.Errorf("File not reset: %q", job2.File)
	}

	ReleaseCodeJob(job2)
}

// ============================================================================
// BENCHMARKS
// ============================================================================

func BenchmarkPool_GetPut_PulseHTTP(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		job := getPulseHTTPJob()
		job.URL = "http://benchmark.test"
		ReleasePulseJob(job)
	}
}

func BenchmarkPool_GetPut_InterventionDocker(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		job := getInterventionDockerJob()
		job.Container = "benchmark-container"
		ReleaseInterventionJob(job)
	}
}

func BenchmarkPool_GetPut_CodeLog(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		job := getCodeLogJob()
		job.Monitor = "benchmark-monitor"
		ReleaseCodeJob(job)
	}
}

func BenchmarkPool_Concurrent_PulseHTTP(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			job := getPulseHTTPJob()
			job.URL = "http://concurrent.test"
			ReleasePulseJob(job)
		}
	})
}
