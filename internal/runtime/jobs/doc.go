// Package jobs provides job types and execution logic for CPRA monitor operations.
//
// Jobs are created from monitor schemas via factory functions, executed by worker
// pools, and produce Results consumed by ECS systems for state updates and alerting.
//
// # Package Organization
//
// The package is organized into focused files:
//
//   - base.go: BaseJob and BaseNetworkJob with common functionality
//   - types.go: Job interface, Result struct, errors, and payloads
//   - factory.go: CreatePulseJob, CreateInterventionJob, CreateCodeJob
//   - pool.go: sync.Pool definitions for job struct reuse
//   - dial_limiter.go: Global rate/concurrency limiting for network operations
//   - TEMPLATE.go: Template for creating new job types
//
// Job implementations are split by type:
//
//   - pulse_http.go, pulse_tcp.go, pulse_icmp.go: Health check jobs
//   - intervention_docker.go: Docker intervention jobs (restart, stop, start, kill, pause, unpause, scale)
//   - code_log.go, code_slack.go, etc.: Alert notification jobs
//
// Infrastructure files:
//
//   - pool_http.go: fasthttp client pool for HTTP jobs
//   - pool_tcp.go: Optimized TCP dialer with SO_REUSEADDR
//   - pool_docker.go: Docker client pool
//   - log_writer.go: Async log writer for code alerts
//
// # Job Types
//
// Pulse Jobs perform health checks:
//   - PulseHTTPJob: HTTP health checks with fasthttp
//   - PulseTCPJob: TCP connection checks
//   - PulseICMPJob: ICMP ping checks with privilege handling
//
// Intervention Jobs perform automated recovery:
//   - InterventionDockerJob: Docker container restart (default)
//   - InterventionDockerStopJob: Graceful container stop (SIGTERM)
//   - InterventionDockerStartJob: Start stopped container
//   - InterventionDockerKillJob: Force kill container (SIGKILL or custom signal)
//   - InterventionDockerPauseJob: Pause container processes
//   - InterventionDockerUnpauseJob: Resume paused container processes
//   - InterventionDockerScaleJob: Scale Swarm service replicas
//
// Code Alert Jobs send notifications:
//   - CodeLogJob: JSON log file output
//   - CodeSlackJob: Slack notifications (placeholder)
//   - CodePagerDutyJob: PagerDuty alerts (placeholder)
//   - CodeEmailJob: Email notifications (placeholder)
//   - CodeWebhookJob: Webhook notifications (placeholder)
//
// # Safety Guardrails
//
// All network jobs implement critical safety patterns:
//
//  1. Global Dial Limiter: Prevents CPU spikes during network outages by limiting
//     both rate (requests/sec) and concurrency (in-flight dials). Without this,
//     100k monitors becoming unreachable simultaneously can spike CPU from 50% to 800%.
//
//  2. Context Cancellation: Jobs check ctx.Done() before each retry, enabling
//     graceful shutdown without stuck goroutines.
//
//  3. Retry with Backoff: Jobs retry with 50ms delays between attempts to handle
//     transient failures without overwhelming targets.
//
//  4. Object Pooling: sync.Pool reduces GC pressure by reusing job structs.
//     At 200k jobs/sec, this prevents massive allocation churn.
//
//  5. Pre-allocated Payloads: Immutable result payloads are shared to reduce
//     per-job allocations.
//
// # Creating New Jobs
//
// See TEMPLATE.go for a complete template with safety checklist. Key steps:
//
//  1. Copy TEMPLATE.go to a new file (e.g., pulse_dns.go)
//  2. Implement the job struct and Execute method
//  3. Add sync.Pool functions to pool.go
//  4. Add factory function to factory.go
//  5. Add predeclared errors to types.go
//
// # Example Usage
//
//	pulseSchema := schema.Pulse{
//		Config:  &schema.PulseHTTPConfig{Url: "https://example.com", Method: "GET"},
//		Timeout: 5 * time.Second,
//		Retries: 2,
//	}
//	job, err := jobs.CreatePulseJob(pulseSchema, entityID)
//	if err != nil {
//		return err
//	}
//	result := job.Execute(ctx)
//	if result.Err != nil {
//		log.Printf("Pulse failed: %v", result.Err)
//	}
package jobs
