package cli

import (
	"fmt"
	"io"
	"sort"

	"cpra/internal/client"
	"cpra/internal/web/snapshot"
)

// ---------- monitors ----------

func writeMonitors(p *printer, list *client.MonitorsList) error {
	return p.render(list, func(w io.Writer) error {
		wide := p.wide()
		headers := []string{"ID", "NAME", "TYPE", "STATUS", "CODE", "FAILURES", "LAST CHECK"}
		if wide {
			headers = append(headers, "NEXT CHECK")
		}
		rows := make([][]string, 0, len(list.Monitors))
		for _, m := range list.Monitors {
			row := []string{
				fmt.Sprint(m.ID),
				m.Name,
				m.PulseType,
				m.Status,
				strOrDash(m.PendingCode),
				fmt.Sprint(m.ConsecutiveFailures),
				fmtAgo(m.LastCheck),
			}
			if wide {
				row = append(row, fmtNext(m.NextCheck))
			}
			rows = append(rows, row)
		}
		if err := table(w, headers, rows); err != nil {
			return err
		}
		if list.Total == 0 {
			_, err := fmt.Fprintln(w, "\nno monitors")
			return err
		}
		_, err := fmt.Fprintf(w, "\n(page %d, %d of %d monitors)\n", list.Page, len(list.Monitors), list.Total)
		return err
	})
}

func writeMonitorDetail(p *printer, m *snapshot.MonitorSummary) error {
	return p.render(m, func(w io.Writer) error {
		return kv(w, [][2]string{
			{"ID", fmt.Sprint(m.ID)},
			{"Name", m.Name},
			{"Pulse type", m.PulseType},
			{"Status", m.Status},
			{"Incident", fmtBool(m.Incident)},
			{"Pending code", strOrDash(m.PendingCode)},
			{"Consecutive failures", fmt.Sprint(m.ConsecutiveFailures)},
			{"Active codes", strOrDash(joinStrings(m.ActiveCodes))},
			{"Last check", fmtAgo(m.LastCheck)},
			{"Last success", fmtAgo(m.LastSuccess)},
			{"Next check", fmtNext(m.NextCheck)},
		})
	})
}

func joinStrings(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// ---------- incidents ----------

func writeIncidents(p *printer, inc *client.Incidents) error {
	return p.render(inc, func(w io.Writer) error {
		wide := p.wide()
		headers := []string{"ID", "NAME", "TYPE", "CODE", "FAILURES", "LAST CHECK"}
		if wide {
			headers = append(headers, "ACTIVE CODES")
		}
		rows := make([][]string, 0, len(inc.Incidents))
		for _, m := range inc.Incidents {
			row := []string{
				fmt.Sprint(m.ID),
				m.Name,
				m.PulseType,
				strOrDash(m.PendingCode),
				fmt.Sprint(m.ConsecutiveFailures),
				fmtAgo(m.LastCheck),
			}
			if wide {
				row = append(row, strOrDash(joinStrings(m.ActiveCodes)))
			}
			rows = append(rows, row)
		}
		if err := table(w, headers, rows); err != nil {
			return err
		}
		_, err := fmt.Fprintf(w, "\n(%d incidents)\n", inc.Count)
		return err
	})
}

// ---------- queues ----------

func writeQueues(p *printer, qs map[string]client.QueueStats) error {
	return p.render(qs, func(w io.Writer) error {
		wide := p.wide()
		headers := []string{"NAME", "DEPTH", "CAPACITY", "ENQ/S", "DEQ/S", "AVG LAT", "DROPPED"}
		if wide {
			headers = append(headers, "ENQUEUED", "DEQUEUED", "MAX LAT")
		}
		rows := make([][]string, 0, len(qs))
		for _, n := range sortedNames(qs) {
			s := qs[n]
			row := []string{
				s.Name,
				fmt.Sprint(s.Stats.QueueDepth),
				fmt.Sprint(s.Stats.Capacity),
				fmtRate(s.Stats.EnqueueRate),
				fmtRate(s.Stats.DequeueRate),
				fmtDur(s.Stats.AvgJobLatency),
				fmt.Sprint(s.Stats.Dropped),
			}
			if wide {
				row = append(row,
					fmt.Sprint(s.Stats.Enqueued),
					fmt.Sprint(s.Stats.Dequeued),
					fmtDur(s.Stats.MaxJobLatency),
				)
			}
			rows = append(rows, row)
		}
		return table(w, headers, rows)
	})
}

// ---------- pools ----------

func writePools(p *printer, ps map[string]client.PoolStats) error {
	return p.render(ps, func(w io.Writer) error {
		wide := p.wide()
		headers := []string{"NAME", "RUNNING", "CAPACITY", "TARGET", "WAITING", "COMPLETED"}
		if wide {
			headers = append(headers, "SUBMITTED", "SCALING", "PENDING", "MIN", "MAX")
		}
		rows := make([][]string, 0, len(ps))
		for _, n := range sortedNames(ps) {
			s := ps[n]
			row := []string{
				s.Name,
				fmt.Sprint(s.RunningWorkers),
				fmt.Sprint(s.CurrentCapacity),
				fmt.Sprint(s.TargetWorkers),
				fmt.Sprint(s.WaitingTasks),
				fmt.Sprint(s.TasksCompleted),
			}
			if wide {
				row = append(row,
					fmt.Sprint(s.TasksSubmitted),
					fmt.Sprint(s.ScalingEvents),
					fmt.Sprint(s.PendingResults),
					fmt.Sprint(s.MinWorkers),
					fmt.Sprint(s.MaxWorkers),
				)
			}
			rows = append(rows, row)
		}
		return table(w, headers, rows)
	})
}

// sortedNames returns the map keys sorted ascending, for stable output.
func sortedNames[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// ---------- systems ----------

func writeSystems(p *printer, s *client.Systems) error {
	return p.render(s, func(w io.Writer) error {
		headers := []string{"NAME", "UPDATES", "ENTITIES", "BATCHES", "TOTAL DUR", "MAX DUR"}
		rows := make([][]string, 0, len(s.Systems))
		for _, n := range sortedNames(s.Systems) {
			m := s.Systems[n]
			rows = append(rows, []string{
				m.SystemName,
				fmt.Sprint(m.TotalUpdates),
				fmt.Sprint(m.TotalEntitiesProcessed),
				fmt.Sprint(m.TotalBatchesCreated),
				fmtDur(m.TotalDuration),
				fmtDur(m.MaxUpdateDuration),
			})
		}
		if err := table(w, headers, rows); err != nil {
			return err
		}
		a := s.Aggregate
		_, err := fmt.Fprintf(w, "\naggregate: %d updates, %d entities, %.1f entities/s, %.1f updates/s across %d systems\n",
			a.TotalUpdates, a.TotalEntitiesProcessed, a.EntitiesPerSecond, a.UpdatesPerSecond, a.SystemCount)
		return err
	})
}

// ---------- config ----------

func writeConfig(p *printer, cfg *client.RuntimeConfig) error {
	return p.render(cfg, func(w io.Writer) error {
		return kv(w, [][2]string{
			{"Queue capacity", fmt.Sprint(cfg.QueueCapacity)},
			{"Batch size", fmt.Sprint(cfg.BatchSize)},
			{"Alert cooldown", fmtDur(cfg.AlertCooldown)},
			{"Recovery bypass", fmtBool(cfg.RecoveryBypass)},
			{"Adaptive queue", fmtBool(cfg.UseAdaptive)},
			{"Queue type", cfg.QueueType},
		})
	})
}

// ---------- overview ----------

func writeOverview(p *printer, ov *client.Overview) error {
	return p.render(ov, func(w io.Writer) error {
		if err := kv(w, [][2]string{
			{"Generated", ov.Generated.Format("2006-01-02 15:04:05")},
			{"Total monitors", fmt.Sprint(ov.Total)},
			{"Disabled", fmt.Sprint(ov.Disabled)},
			{"Up", fmt.Sprintf("%.1f%%", ov.UpPercent)},
		}); err != nil {
			return err
		}
		if err := writeCountTable(w, "STATUS", ov.ByStatus); err != nil {
			return err
		}
		if err := writeCountTable(w, "PULSE TYPE", ov.ByPulseType); err != nil {
			return err
		}
		return writeCountTable(w, "CODE", ov.ByCode)
	})
}

// writeCountTable renders a two-column breakdown table (label, count).
func writeCountTable(w io.Writer, label string, counts map[string]int) error {
	if len(counts) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	headers := []string{label, "COUNT"}
	rows := make([][]string, 0, len(counts))
	for _, k := range sortedNames(counts) {
		rows = append(rows, []string{k, fmt.Sprint(counts[k])})
	}
	return table(w, headers, rows)
}
