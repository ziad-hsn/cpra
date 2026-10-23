/**
 * CPRA API types — matching cpra/internal/httpserver/types.go and snapshot/queue/controller shapes.
 * Durations are Go time.Duration nanoseconds; time fields are RFC3339 strings.
 */

import type { FailureDiagnostic, NotificationCondition } from './generated';

export type MonitorStatus = 'up' | 'down' | 'degraded' | 'verifying' | 'incident' | 'disabled' | 'unknown';

export interface MonitorSummary {
  health?: 'healthy' | 'unhealthy' | 'warning' | 'unknown';
  last_observed_health?: 'healthy' | 'unhealthy' | 'warning' | 'unknown';
  last_observed_at?: string;
  availability?: { state: 'available' | 'unavailable' | 'paused' | 'pending'; reason?: string; observed_at?: string };
  maintenance?: boolean;
  notifications_suppressed?: boolean;
  suppression_observed_at?: string;
  configuration_warnings?: string[];
  notification_conditions?: NotificationCondition[];
  notification_conditions_truncated?: boolean;
  notification_conditions_observed_at?: string;
  /** Decimal string: never coerce ledger progress to a JavaScript number. */
  notification_conditions_sequence?: string;
  phase?: 'idle' | 'incident' | 'verifying';
  enabled?: boolean;
  ignored?: boolean;
  snoozed_until?: string;
  incarnation_uid?: string;
  source?: 'file' | 'management';
  incident_id?: string;
  pending_notifications?: number;
  completed_notifications?: number;
  failed_notifications?: number;
  unknown_notifications?: number;
  actions_needing_review?: number;
  certificate_verification_disabled?: boolean;
  last_failure?: FailureDiagnostic;
	configuration_problem?: FailureDiagnostic;
	configuration_problem_at?: string;
	warning?: string;
  monitor_id?: string;
  latency_available?: boolean;
  unknown_actions?: number;
  id: number;
  name: string;
  pulse_type: string;
  status: MonitorStatus;
  incident: boolean;
  pending_code: string;
  consecutive_failures: number;
  last_check: string;
  last_success: string;
  next_check: string;
  active_codes: string[];
  /** Endpoint host/URL for display (may be empty if unknown). */
  target?: string;
  /** Pulse interval in milliseconds. */
  interval_ms?: number;
  /** Rolling success ratio 0..1 over observed checks. */
  uptime?: number;
  /** Last observed latency in milliseconds; meaningful only when latency_available is true. */
  latency_ms?: number;
}

export interface OverviewResponse {
  generated?: string;
  total: number;
  disabled: number;
  unavailable?: number;
  paused?: number;
  pending?: number;
  maintenance?: number;
  suppressed_unhealthy?: number;
  unsuppressed_unhealthy?: number;
  by_status: Record<string, number>;
  by_pulse_type: Record<string, number>;
  by_code: Record<string, number>;
  up_percent: number;
  index_capped: boolean;
}

export interface MonitorsFilters {
  status?: string;
  type?: string;
  code?: string;
  q?: string;
}

export interface MonitorsResponse {
  generated?: string;
  page: number;
  size: number;
  total: number;
  filters: Record<string, string>;
  monitors: MonitorSummary[];
}

export interface IncidentsResponse {
  generated?: string;
  count: number;
  incidents: MonitorSummary[];
}

export interface SystemMetrics {
  last_update_time: string;
  start_time: string;
  system_name: string;
  total_updates: number;
  total_entities_processed: number;
  total_batches_created: number;
  total_duration: number; // ns
  max_update_duration: number; // ns
  min_update_duration: number; // ns
}

export interface AggregateMetrics {
  start_time: string;
  min_update_duration: number;
  total_entities_processed: number;
  total_batches_created: number;
  total_duration: number;
  max_update_duration: number;
  system_count: number;
  avg_update_duration: number;
  avg_entities_per_update: number;
  avg_batches_per_update: number;
  entities_per_second: number;
  updates_per_second: number;
  total_updates: number;
}

export interface SystemsResponse {
  systems: Record<string, SystemMetrics>;
  aggregate: AggregateMetrics;
}

export interface DriverQueueStats {
	requested_capacity?: number;
	reserved_capacity?: number;
  driver: string;
  pending: number;
  active: number;
  concurrency_limit: number;
  admission_limit: number;
  oldest_pending_age: number; // ns
  oldest_active_age: number; // ns
  rejections: number;
  timeouts: number;
  condition: string;
}

export interface QueueStats {
  drivers?: DriverQueueStats[];
	/** Nanoseconds for a currently pending admission; consult availability. */
	oldest_pending_age?: number;
	oldest_pending_available?: boolean;
	oldest_pending_reason?: string;
  arrival_cv?: number;
  arrival_samples?: number;
  last_enqueue: string;
  last_dequeue: string;
  avg_queue_time: number; // ns
  dequeued: number;
  dropped: number;
  max_queue_time: number; // ns
  queue_depth: number;
  max_job_latency: number; // ns
  avg_job_latency: number; // ns
  enqueue_rate: number;
  dequeue_rate: number;
  enqueued: number;
  capacity: number;
  sample_window: number; // ns
}

export interface WorkerPoolStats {
	requested_capacity?: number;
	reserved_capacity?: number;
	draining_workers?: number;
	capacity_condition?: string;
  active_executions?: number;
  oldest_execution_age?: number;
  pending_submissions?: number;
  oldest_submission_age?: number;
  slo_condition?: string;
  sizing_model?: string;
  service_time?: number; // ns
  service_cv?: number;
  service_samples?: number;
  last_scale_time: string;
  min_workers: number;
  max_workers: number;
  current_capacity: number;
  running_workers: number;
  waiting_tasks: number;
  target_workers: number;
  tasks_submitted: number;
  tasks_completed: number;
  scaling_events: number;
  pending_results: number;
}

export interface QueuesResponse {
  pulse: QueueStats & { name: string };
  intervention: QueueStats & { name: string };
  code: QueueStats & { name: string };
}

export interface PoolsResponse {
  pulse: WorkerPoolStats & { name: string };
  intervention: WorkerPoolStats & { name: string };
  code: WorkerPoolStats & { name: string };
}

export interface ConfigResponse {
  queue_capacity: number;
  batch_size: number;
  alert_cooldown: number; // ns (time.Duration)
  recovery_bypass: boolean;
  use_adaptive_queue: boolean;
  queue_type: string;
}

export interface HealthResponse {
  status: string;
}

export interface DurableAction {
  id: string;
  revision: string;
  kind: string;
  color?: string;
  endpoint: number;
  attempt: number;
  state: 'queued' | 'started' | 'succeeded' | 'failed' | 'unknown' | 'cancelled';
  outcome?: string;
}
export interface DurableStateResponse {
  storage: { mode: 'raft' | 'memory'; ready: boolean; node_id?: string; committed_index: number; commit_latency_ms: number; snapshot_duration_ms: number; error?: string };
  actions: DurableAction[];
  monitor_id?: string;
  revision?: string;
}
export interface MonitorEvent {
  resolution?: import('./generated').ResolutionNotice;
  terminationReason?: string;
  failure?: import('./generated').FailureDiagnostic;
  cause?: import('./generated').FailureDiagnostic;
  agentReceipt?: import('./generated').AgentReceipt;
  recoveryFailure?: import('./generated').RecoveryFailure;
  id: string;
  monitor_id: string;
  revision: string;
  at: string;
  type: string;
  action_id?: string;
  condition_id?: string;
  opening_action_id?: string;
  kind?: string;
  color?: string;
  endpoint?: number;
  outcome?: string;
  actor?: string;
  reason?: string;
  note?: string;
  incident_id?: string;
  control_revision?: string;
  evidence_refs?: string[];
}
export interface HistoryResponse { events: MonitorEvent[]; next_cursor?: string; retention_days: number }
export interface Percentiles { p50_ms: number | null; p95_ms: number | null; p99_ms: number | null }
export interface SLOResponse {
  generated: string;
  window_seconds: number;
  queue_target_ms: number;
  result_target_ms: number;
  coverage_complete: boolean;
  gap_start?: string;
  gap_end?: string;
  reports: { driver: string; samples: number; expected: number; overdue?: number; pending?: number; timeouts: number; missed: number; paused_monitors?: number; paused_monitor_seconds?: number; scheduling_queue: Percentiles; execution: Percentiles; scheduled_result: Percentiles; queue_met: number; result_met: number; queue_attainment: number | null; result_attainment: number | null; condition: string }[];
}
