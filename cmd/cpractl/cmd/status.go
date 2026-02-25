package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"cpra/cmd/cpractl/client"
	"cpra/cmd/cpractl/output"
)

func init() {
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show CPRA system status (queues, workers, entities)",
		RunE:  runStatus,
	}
	rootCmd.AddCommand(statusCmd)
}

type statusResponse struct {
	Service       string                   `json:"service"`
	Version       string                   `json:"version"`
	Timestamp     time.Time                `json:"timestamp"`
	UptimeSeconds float64                  `json:"uptime_seconds"`
	Queues        map[string]queueSummary  `json:"queues"`
	Workers       map[string]workerSummary `json:"workers"`
	Entities      entitiesSummary          `json:"entities"`
}

type queueSummary struct {
	Depth        int           `json:"depth"`
	Capacity     int           `json:"capacity"`
	Enqueued     int64         `json:"enqueued"`
	Dequeued     int64         `json:"dequeued"`
	Dropped      int64         `json:"dropped"`
	EnqueueRate  float64       `json:"enqueue_rate"`
	DequeueRate  float64       `json:"dequeue_rate"`
	AvgWait      time.Duration `json:"avg_wait"`
	RollingP95   time.Duration `json:"p95_wait"`
	SampleWindow time.Duration `json:"sample_window"`
}

type workerSummary struct {
	Running        int       `json:"running"`
	Idle           int       `json:"idle"`
	Capacity       int       `json:"capacity"`
	Target         int       `json:"target"`
	Waiting        int       `json:"waiting"`
	PendingResults int       `json:"pending_results"`
	LastScaleTime  time.Time `json:"last_scale_time"`
	ScalingEvents  int64     `json:"scaling_events"`
}

type entitiesSummary struct {
	Used     int `json:"used"`
	Recycled int `json:"recycled"`
	Total    int `json:"total"`
	Capacity int `json:"capacity"`
}

func runStatus(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), viper.GetDuration("timeout"))
	defer cancel()

	cli, err := client.New()
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cli.BaseURL()+"/status", nil)
	if err != nil {
		return err
	}

	resp, err := cli.HTTP().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var data statusResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return err
	}

	f := output.New(viper.GetString("output"), os.Stdout)
	outFormat := viper.GetString("output")
	if outFormat == "table" || outFormat == "wide" {
		return f.Format(statusTable(data, outFormat == "wide"))
	}
	return f.Format(data)
}

func statusTable(resp statusResponse, wide bool) output.TableData {
	rows := [][]string{
		{
			"meta",
			"service",
			fmt.Sprintf("%s %s", resp.Service, resp.Version),
		},
		{
			"meta",
			"uptime",
			formatUptime(resp.UptimeSeconds),
		},
		{
			"meta",
			"timestamp",
			resp.Timestamp.Format(time.RFC3339),
		},
	}

	queueOrder := []string{"pulse", "intervention", "code"}
	for _, name := range queueOrder {
		q, ok := resp.Queues[name]
		if !ok {
			continue
		}
		rows = append(rows, []string{
			"queue",
			name,
			formatQueueDetails(q, wide),
		})
	}

	workerOrder := []string{"pulse", "intervention", "code"}
	for _, name := range workerOrder {
		w, ok := resp.Workers[name]
		if !ok {
			continue
		}
		rows = append(rows, []string{
			"workers",
			name,
			formatWorkerDetails(w, wide),
		})
	}

	rows = append(rows, []string{
		"entities",
		"world",
		formatEntities(resp.Entities),
	})

	return output.TableData{
		Headers: []string{"CATEGORY", "NAME", "DETAILS"},
		Rows:    rows,
	}
}

func formatQueueDetails(q queueSummary, wide bool) string {
	base := fmt.Sprintf(
		"depth=%d cap=%d enq/s=%.2f deq/s=%.2f dropped=%d",
		q.Depth,
		q.Capacity,
		q.EnqueueRate,
		q.DequeueRate,
		q.Dropped,
	)
	if !wide {
		return base
	}
	return fmt.Sprintf(
		"%s avg=%s p95=%s window=%s",
		base,
		q.AvgWait,
		q.RollingP95,
		q.SampleWindow,
	)
}

func formatWorkerDetails(w workerSummary, wide bool) string {
	base := fmt.Sprintf(
		"running=%d idle=%d target=%d capacity=%d waiting=%d pending=%d",
		w.Running,
		w.Idle,
		w.Target,
		w.Capacity,
		w.Waiting,
		w.PendingResults,
	)
	if !wide {
		return base
	}
	return fmt.Sprintf("%s last_scale=%s events=%d", base, w.LastScaleTime.Format(time.RFC3339), w.ScalingEvents)
}

func formatEntities(e entitiesSummary) string {
	return fmt.Sprintf(
		"used=%d recycled=%d total=%d capacity=%d",
		e.Used,
		e.Recycled,
		e.Total,
		e.Capacity,
	)
}

func formatUptime(seconds float64) string {
	if seconds <= 0 {
		return "0s"
	}
	return time.Duration(seconds * float64(time.Second)).String()
}
