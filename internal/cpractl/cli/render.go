package cli

import (
	"fmt"
	"io"
	"math"
	"strconv"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func observedCount(available bool, value int64) string {
	if !available || value < 0 {
		return "unavailable"
	}
	return strconv.FormatInt(value, 10)
}
func observedNumber(available bool, value float64) string {
	if !available || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return "unavailable"
	}
	return fmt.Sprintf("%.3f", value)
}
func measurement(value api.Measurement) string { return observedNumber(value.Available, value.Value) }
func measurementPercent(value api.Measurement) string {
	if !value.Available || value.Value > 1 || math.IsNaN(value.Value) || math.IsInf(value.Value, 0) || value.Value < 0 {
		return "unavailable"
	}
	return fmt.Sprintf("%.3f%%", value.Value*100)
}
func percentile(available bool, p api.Percentiles) string {
	return observedNumber(available && p.Available, p.P99MS)
}
func observedText(available bool, value string) string {
	if !available || value == "" {
		return "unavailable"
	}
	return managementDisplay(value)
}
func observedBool(available bool, value bool) string {
	if !available {
		return "unavailable"
	}
	return strconv.FormatBool(value)
}
func observedMeasurement(available bool, value api.Measurement) string {
	if !available {
		return "unavailable"
	}
	return measurement(value)
}
func observationPage(w io.Writer, available bool, cursor string) error {
	if !available {
		if _, err := fmt.Fprintln(w, "Observation unavailable"); err != nil {
			return err
		}
	}
	if cursor != "" {
		_, err := fmt.Fprintf(w, "Next cursor: %s\n", managementDisplay(cursor))
		return err
	}
	return nil
}
func writeQueues(p *printer, page api.QueueList) error {
	return p.render(page, func(w io.Writer) error {
		headers := []string{"NAME", "DEPTH", "CAPACITY", "ENQUEUED/S", "DEQUEUED/S", "AVERAGE WAIT MS", "DROPS"}
		if p.wide() {
			headers = append(headers, "COMPLETED/S", "MAXIMUM WAIT MS", "SATURATED", "LIMIT REASON")
		}
		rows := make([][]string, 0, len(page.Items))
		for _, q := range page.Items {
			a := page.Available && q.Available
			row := []string{managementDisplay(q.Name), observedCount(a, q.Depth), observedCount(a, q.Capacity), observedNumber(a && q.RatesAvailable, q.EnqueuedPerSecond), observedNumber(a && q.RatesAvailable, q.DequeuedPerSecond), observedMeasurement(a, q.AverageWait), observedCount(a, q.Drops)}
			if p.wide() {
				row = append(row, observedNumber(a && q.CompletedRateAvailable, q.CompletedPerSecond), observedMeasurement(a, q.MaximumWait), observedBool(a, q.Saturated), observedText(a, q.LimitReason))
			}
			rows = append(rows, row)
		}
		if err := table(w, headers, rows); err != nil {
			return err
		}
		return observationPage(w, page.Available, page.NextCursor)
	})
}
func writePools(p *printer, page api.PoolList) error {
	return p.render(page, func(w io.Writer) error {
		headers := []string{"NAME", "WORKERS", "BUSY", "TARGET", "CAPACITY", "WAITING", "COMPLETED"}
		if p.wide() {
			headers = append(headers, "MINIMUM", "MAXIMUM", "SERVICE TIME MS", "MODEL", "CONDITION")
		}
		rows := make([][]string, 0, len(page.Items))
		for _, pool := range page.Items {
			a := page.Available && pool.Available
			row := []string{managementDisplay(pool.Name), observedCount(a, pool.Workers), observedCount(a && pool.BusyAvailable, pool.Busy), observedCount(a, pool.Target), observedCount(a, pool.Capacity), observedCount(a, pool.WaitingTasks), observedCount(a, pool.TasksCompleted)}
			if p.wide() {
				row = append(row, observedCount(a, pool.Minimum), observedCount(a, pool.Maximum), observedMeasurement(a, pool.ServiceTime), observedText(a, pool.SizingModel), observedText(a, pool.SloCondition))
			}
			rows = append(rows, row)
		}
		if err := table(w, headers, rows); err != nil {
			return err
		}
		return observationPage(w, page.Available, page.NextCursor)
	})
}
func writeSystems(p *printer, page api.SystemList) error {
	return p.render(page, func(w io.Writer) error {
		headers := []string{"NAME", "UPDATES", "ENTITIES", "LAST UPDATE MS", "AVERAGE UPDATE MS"}
		if p.wide() {
			headers = append(headers, "MINIMUM UPDATE MS", "MAXIMUM UPDATE MS")
		}
		rows := make([][]string, 0, len(page.Items))
		for _, system := range page.Items {
			a := page.Available && system.Available
			row := []string{managementDisplay(system.Name), observedCount(a, system.Updates), observedCount(a, system.EntitiesProcessed), observedMeasurement(a, system.LastUpdateDurationMS), observedMeasurement(a, system.AverageUpdateDuration)}
			if p.wide() {
				row = append(row, observedMeasurement(a, system.MinimumUpdateDuration), observedMeasurement(a, system.MaximumUpdateDuration))
			}
			rows = append(rows, row)
		}
		if err := table(w, headers, rows); err != nil {
			return err
		}
		return observationPage(w, page.Available, page.NextCursor)
	})
}
func writeConfig(p *printer, c api.RuntimeConfig) error {
	return p.render(c, func(w io.Writer) error {
		return kv(w, [][2]string{{"Available", fmtBool(c.Available)}, {"Read only", fmtBool(c.ReadOnly)}, {"Storage mode", observedText(c.Available, c.StorageMode)}, {"History retention", observedText(c.Available, c.HistoryRetention)}, {"Queue capacity", observedCount(c.Available, c.QueueCapacity)}, {"Queue target", observedText(c.Available, c.QueueTarget)}, {"Result target", observedText(c.Available, c.ResultTarget)}, {"SLO window", observedText(c.Available, c.SloWindow)}})
	})
}
