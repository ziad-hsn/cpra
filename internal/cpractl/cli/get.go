package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"cpra/internal/client"
)

// monitorFlags holds the list-filter flags for "get monitors".
type monitorFlags struct {
	status    string
	pulseType string
	code      string
	query     string
	page      int
	size      int
}

// newGetCommand builds the "get" command tree. Following kubectl, a resource
// command merges list and detail: "get monitors" lists, "get monitors 2"
// shows the monitor with ID 2.
func newGetCommand(o *options) *cobra.Command {
	get := &cobra.Command{
		Use:   "get",
		Short: "Display one or many resources (monitors, queues, pools, ...)",
		Args:  cobra.NoArgs,
	}

	mf := &monitorFlags{}
	monitors := &cobra.Command{
		Use:     "monitors [id]",
		Aliases: []string{"monitor", "mons", "mon"},
		Short:   "List monitors, or show a single monitor by ID",
		Args:    cobra.MaximumNArgs(1),
		Example: "  cpractl get monitors --status down\n  cpractl get monitor 2 -o json",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				id, err := strconv.ParseUint(args[0], 10, 32)
				if err != nil {
					return fmt.Errorf("invalid monitor ID %q: must be an unsigned integer", args[0])
				}
				return runGetMonitor(cmd, o, uint32(id))
			}
			return runGetMonitors(cmd, o, mf)
		},
	}
	monitors.Flags().StringVar(&mf.status, "status", "", "filter by status (unknown|up|degraded|down|verifying|incident|disabled)")
	monitors.Flags().StringVar(&mf.pulseType, "type", "", "filter by pulse type (http|tcp|icmp|dns|...)")
	monitors.Flags().StringVar(&mf.code, "code", "", "filter by pending code (red|yellow|green|cyan|gray)")
	monitors.Flags().StringVarP(&mf.query, "query", "q", "", "substring match on monitor name")
	monitors.Flags().IntVar(&mf.page, "page", 1, "page number")
	monitors.Flags().IntVar(&mf.size, "size", 50, "page size (max 500)")

	incidents := &cobra.Command{
		Use:     "incidents",
		Aliases: []string{"incident", "inc"},
		Short:   "List current incidents",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetIncidents(cmd, o)
		},
	}

	queues := &cobra.Command{
		Use:     "queues",
		Aliases: []string{"queue", "q"},
		Short:   "Show queue statistics",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetQueues(cmd, o)
		},
	}

	pools := &cobra.Command{
		Use:     "pools",
		Aliases: []string{"pool"},
		Short:   "Show worker-pool statistics",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetPools(cmd, o)
		},
	}

	systems := &cobra.Command{
		Use:     "systems",
		Aliases: []string{"system", "sys"},
		Short:   "Show ECS system performance metrics",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetSystems(cmd, o)
		},
	}

	config := &cobra.Command{
		Use:   "config",
		Short: "Show the runtime configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetConfig(cmd, o)
		},
	}

	overview := &cobra.Command{
		Use:     "overview",
		Aliases: []string{"status", "ov"},
		Short:   "Show the fleet overview",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetOverview(cmd, o)
		},
	}

	get.AddCommand(monitors, incidents, queues, pools, systems, config, overview)
	return get
}

// ---------- run functions: call the client, then render ----------

func runGetMonitors(cmd *cobra.Command, o *options, mf *monitorFlags) error {
	c, err := o.mustClient()
	if err != nil {
		return err
	}
	list, err := c.ListMonitors(cmd.Context(), client.MonitorListOptions{
		Status:    mf.status,
		PulseType: mf.pulseType,
		Code:      mf.code,
		Query:     mf.query,
		Page:      mf.page,
		Size:      mf.size,
	})
	if err != nil {
		return err
	}
	return writeMonitors(newPrinter(cmd, o), list)
}

func runGetMonitor(cmd *cobra.Command, o *options, id uint32) error {
	c, err := o.mustClient()
	if err != nil {
		return err
	}
	m, err := c.GetMonitor(cmd.Context(), id)
	if err != nil {
		return err
	}
	return writeMonitorDetail(newPrinter(cmd, o), m)
}

func runGetIncidents(cmd *cobra.Command, o *options) error {
	c, err := o.mustClient()
	if err != nil {
		return err
	}
	inc, err := c.ListIncidents(cmd.Context())
	if err != nil {
		return err
	}
	return writeIncidents(newPrinter(cmd, o), inc)
}

func runGetQueues(cmd *cobra.Command, o *options) error {
	c, err := o.mustClient()
	if err != nil {
		return err
	}
	qs, err := c.Queues(cmd.Context())
	if err != nil {
		return err
	}
	return writeQueues(newPrinter(cmd, o), qs)
}

func runGetPools(cmd *cobra.Command, o *options) error {
	c, err := o.mustClient()
	if err != nil {
		return err
	}
	ps, err := c.Pools(cmd.Context())
	if err != nil {
		return err
	}
	return writePools(newPrinter(cmd, o), ps)
}

func runGetSystems(cmd *cobra.Command, o *options) error {
	c, err := o.mustClient()
	if err != nil {
		return err
	}
	s, err := c.Systems(cmd.Context())
	if err != nil {
		return err
	}
	return writeSystems(newPrinter(cmd, o), s)
}

func runGetConfig(cmd *cobra.Command, o *options) error {
	c, err := o.mustClient()
	if err != nil {
		return err
	}
	cfg, err := c.Config(cmd.Context())
	if err != nil {
		return err
	}
	return writeConfig(newPrinter(cmd, o), cfg)
}

func runGetOverview(cmd *cobra.Command, o *options) error {
	c, err := o.mustClient()
	if err != nil {
		return err
	}
	ov, err := c.Overview(cmd.Context())
	if err != nil {
		return err
	}
	return writeOverview(newPrinter(cmd, o), ov)
}
