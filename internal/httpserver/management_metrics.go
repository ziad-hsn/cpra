package httpserver

import "time"

func boolMetric(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func (s *Server) writeSystemMetrics(b *metricWriter) {
	values, available := s.metrics.MetricsSnapshot(256)
	writeGauge(b, "cpra_system_measurements_available", "Whether the complete bounded system snapshot is available.", nil, boolMetric(available))
	if !available {
		return
	}
	var updates, entities int64
	start := time.Now()
	for _, value := range values {
		updates += value.TotalUpdates
		entities += value.TotalEntitiesProcessed
		if value.StartTime.Before(start) {
			start = value.StartTime
		}
		labels := []kv{{"system", value.SystemName}}
		writeCounter(b, "cpra_system_entities_processed_by_system_total", "Entities processed by system.", labels, float64(value.TotalEntitiesProcessed))
		writeCounter(b, "cpra_system_updates_by_system_total", "Updates by system.", labels, float64(value.TotalUpdates))
		if value.TotalUpdates > 0 {
			writeGauge(b, "cpra_system_update_duration_seconds_avg", "Average system update duration (s).", labels, value.TotalDuration.Seconds()/float64(value.TotalUpdates))
			writeGauge(b, "cpra_system_update_duration_seconds_max", "Max system update duration (s).", labels, value.MaxUpdateDuration.Seconds())
			writeGauge(b, "cpra_system_update_duration_seconds_min", "Min system update duration (s).", labels, value.MinUpdateDuration.Seconds())
		}
	}
	writeCounter(b, "cpra_system_updates_total", "Total system updates across all systems.", nil, float64(updates))
	writeCounter(b, "cpra_system_entities_processed_total", "Total entities processed across all systems.", nil, float64(entities))
	if elapsed := time.Since(start).Seconds(); elapsed > 0 {
		writeGauge(b, "cpra_system_entities_per_second", "Aggregate entities processed per second.", nil, float64(entities)/elapsed)
		writeGauge(b, "cpra_system_updates_per_second", "Aggregate system updates per second.", nil, float64(updates)/elapsed)
	}
}

func (s *Server) writeSLOMetrics(b *metricWriter, now time.Time) {
	view := s.managementSLO(now)
	writeGauge(b, "cpra_slo_measurements_available", "Whether rolling health-check SLO measurements are available.", nil, boolMetric(view.Available))
	if !view.Available {
		return
	}
	window, _ := time.ParseDuration(string(view.Window))
	writeGauge(b, "cpra_slo_window_seconds", "Rolling SLO measurement window in seconds.", nil, window.Seconds())
	writeGauge(b, "cpra_slo_coverage_complete", "Whether the full rolling window has measurement coverage.", nil, boolMetric(!view.CoverageGap))
	writeGauge(b, "cpra_slo_queue_target_seconds", "Scheduling-plus-queue delay target in seconds.", nil, view.QueueTargetMS/1000)
	writeGauge(b, "cpra_slo_result_target_seconds", "Scheduled-to-committed-result target in seconds.", nil, view.ResultTargetMS/1000)
	for _, report := range view.Reports {
		labels := []kv{{"pipeline", report.Pipeline}, {"driver", report.Driver}}
		for _, value := range []struct {
			name, help string
			value      float64
		}{
			{"cpra_slo_window_samples", "Completed observations in the rolling window.", float64(report.Samples)},
			{"cpra_slo_window_expected", "Expected observations including missed and overdue obligations in the rolling window.", float64(report.Expected)},
			{"cpra_slo_window_missed", "Missed check obligations in the rolling window.", float64(report.Missed)},
			{"cpra_slo_window_overdue", "Overdue check obligations in the rolling window.", float64(report.Overdue)},
			{"cpra_slo_window_pending", "Not-yet-overdue check obligations in the rolling window.", float64(report.Pending)},
			{"cpra_slo_window_timeouts", "Timed-out observations in the rolling window.", float64(report.Timeouts)},
			{"cpra_slo_paused_monitors", "Current owner-observed intentionally paused monitors.", float64(report.PausedMonitors)},
			{"cpra_slo_window_paused_monitor_seconds", "Intentional pause exposure within the rolling window in monitor-seconds.", report.PausedMonitorSeconds},
		} {
			writeGauge(b, value.name, value.help, labels, value.value)
		}
		if report.QueueAttainment.Available {
			writeGauge(b, "cpra_slo_queue_attainment_ratio", "Fraction of expected checks meeting the queue target.", labels, report.QueueAttainment.Value)
		}
		if report.ResultAttainment.Available {
			writeGauge(b, "cpra_slo_result_attainment_ratio", "Fraction of expected checks meeting the total-result target.", labels, report.ResultAttainment.Value)
		}
		for _, stage := range []struct {
			name          string
			available     bool
			p50, p95, p99 float64
		}{
			{"scheduling_queue", report.QueueDelay.Available, report.QueueDelay.P50MS, report.QueueDelay.P95MS, report.QueueDelay.P99MS},
			{"execution", report.Execution.Available, report.Execution.P50MS, report.Execution.P95MS, report.Execution.P99MS},
			{"scheduled_result", report.TotalLatency.Available, report.TotalLatency.P50MS, report.TotalLatency.P95MS, report.TotalLatency.P99MS},
		} {
			if !stage.available {
				continue
			}
			for _, quantile := range []struct {
				name  string
				value float64
			}{{"0.5", stage.p50}, {"0.95", stage.p95}, {"0.99", stage.p99}} {
				writeGauge(b, "cpra_slo_latency_seconds", "Estimated rolling health-check latency quantile in seconds.", []kv{{"pipeline", report.Pipeline}, {"driver", report.Driver}, {"stage", stage.name}, {"quantile", quantile.name}}, quantile.value/1000)
			}
		}
	}
}
