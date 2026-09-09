/**
 * CPRA API types — matching cpra/internal/web/server/types.go and snapshot/queue/controller shapes.
 * Durations are Go time.Duration nanoseconds; time fields are RFC3339 strings.
 */

export type MonitorStatus = 'up' | 'down' | 'degraded' | 'verifying' | 'incident' | 'disabled' | 'unknown';

export interface MonitorSummary {
	warning?: string;
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
  /** Last observed latency in milliseconds (0 when unavailable). */
  latency_ms?: number;
}

export interface OverviewResponse {
  generated: string;
  total: number;
  disabled: number;
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
  generated: string;
  page: number;
  size: number;
  total: number;
  filters: Record<string, string>;
  monitors: MonitorSummary[];
}

export interface IncidentsResponse {
  generated: string;
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

export interface QueueStats {
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
