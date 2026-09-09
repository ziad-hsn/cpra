package systems

import (
	"net/url"
	"sort"
	"strconv"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/loader/schema"
	"cpra/internal/web/snapshot"

	"github.com/mlange-42/ark/ecs"
)

// perfStat tracks the rolling success/total counter used to derive a
// per-monitor successful-check sample ratio for the dashboard. Stored in BatchStatsSnapshotSystem
// keyed by entity ID.
type perfStat struct {
	success uint32
	total   uint32
}

// BatchStatsSnapshotSystem periodically projects the ECS world into a
// read-only snapshot.StatsSnapshot that the web server serves to the
// dashboard. All world reads happen inside this system tick, so HTTP handler
// goroutines never touch the (non-concurrent-safe) ECS world directly.
//
// Snapshot creation is limited by the configured interval. The summary index is capped at MaxIndex monitors; when
// the fleet exceeds the cap the system still computes aggregate counts but
// omits the per-monitor index (aggregates-only mode) to stay within the memory
// budget.
type BatchStatsSnapshotSystem struct {
	world  *ecs.World
	logger Logger
	holder *snapshot.Holder

	// Throttling.
	interval time.Duration
	last     time.Time

	// Index cap; when the active monitor count exceeds this, the per-monitor
	// index is skipped (aggregates are still produced).
	maxIndex int

	// Filters / mappers.
	allFilter      *ecs.Filter1[components.MonitorState]
	disabledFilter *ecs.Filter1[components.Disabled]
	activeFilter   *ecs.Filter2[components.MonitorState, components.PulseConfig]
	codeConfigMap  *ecs.Map1[components.CodeConfig]

	// perf map keys are entity IDs. It tracks a rolling per-monitor success
	// ratio since process start. lastSeenCheck prevents double-counting
	// when the snapshot ticks faster than the pulse interval. The maps are
	// pruned when their size exceeds 2x the observed monitor count.
	perf          map[uint32]*perfStat
	lastSeenCheck map[uint32]time.Time
}

// NewBatchStatsSnapshotSystem creates a new snapshot system that publishes
// into the given Holder every interval.
func NewBatchStatsSnapshotSystem(world *ecs.World, logger Logger, holder *snapshot.Holder, interval time.Duration, maxIndex int) *BatchStatsSnapshotSystem {
	if interval <= 0 {
		interval = time.Second
	}
	if maxIndex <= 0 {
		maxIndex = 50000
	}
	return &BatchStatsSnapshotSystem{
		world:          world,
		logger:         logger,
		holder:         holder,
		interval:       interval,
		maxIndex:       maxIndex,
		allFilter:      ecs.NewFilter1[components.MonitorState](world),
		disabledFilter: ecs.NewFilter1[components.Disabled](world),
		activeFilter:   ecs.NewFilter2[components.MonitorState, components.PulseConfig](world).Without(ecs.C[components.Disabled]()),
		codeConfigMap:  ecs.NewMap1[components.CodeConfig](world),
		perf:           map[uint32]*perfStat{},
		lastSeenCheck:  map[uint32]time.Time{},
	}
}

func (s *BatchStatsSnapshotSystem) Initialize(_ *ecs.World) {
	if s.allFilter != nil {
		s.allFilter.Register()
	}
	if s.disabledFilter != nil {
		s.disabledFilter.Register()
	}
	if s.activeFilter != nil {
		s.activeFilter.Register()
	}
}

// Update produces a snapshot at most once per interval.
func (s *BatchStatsSnapshotSystem) Update(_ *ecs.World) {
	now := time.Now()
	if now.Sub(s.last) < s.interval {
		return
	}
	s.last = now
	s.buildSnapshot(now)
}

// buildSnapshot iterates the world (inside the tick) and publishes a snapshot.
func (s *BatchStatsSnapshotSystem) buildSnapshot(now time.Time) {
	snap := &snapshot.StatsSnapshot{
		Generated:   now,
		ByStatus:    map[string]int{},
		ByPulseType: map[string]int{},
		ByCode:      map[string]int{},
	}

	// Total entity count (all monitors with MonitorState).
	total := 0
	allQ := s.allFilter.Query()
	for allQ.Next() {
		total++
	}
	snap.Total = total

	// Disabled count.
	disabled := 0
	disabledQ := s.disabledFilter.Query()
	for disabledQ.Next() {
		disabled++
	}
	snap.Disabled = disabled
	snap.ByStatus["disabled"] = disabled

	// Active monitors: build aggregates and (if within cap) the index.
	active := s.activeFilter.Query()

	count := 0
	for active.Next() {
		count++
	}
	// Reset the query cursor by re-querying (ark queries are single-pass).
	active = s.activeFilter.Query()

	buildIndex := count <= s.maxIndex

	var monitors []snapshot.MonitorSummary
	byID := map[uint32]int{}
	if buildIndex {
		monitors = make([]snapshot.MonitorSummary, 0, count)
	}

	// Track which entity IDs are observed this pass so we can prune the
	// rolling perf/lastSeenCheck maps when they grow unboundedly. Only needed
	// when building the per-monitor index (uptime is per-monitor).
	var seen map[uint32]struct{}
	if buildIndex {
		seen = make(map[uint32]struct{}, count)
	}

	for active.Next() {
		entity := active.Entity()
		state, cfg := active.Get()
		id := entity.ID()
		if buildIndex {
			seen[id] = struct{}{}
		}

		status := classifyStatus(state)
		snap.ByStatus[status]++

		if cfg != nil && cfg.Type != "" {
			snap.ByPulseType[cfg.Type]++
		} else {
			snap.ByPulseType["unknown"]++
		}

		if state.PendingCode != "" {
			snap.ByCode[state.PendingCode]++
		}

		// Update rolling per-monitor uptime counters. A "new check" is detected
		// when LastCheckTime advances past the previously observed value (or
		// when it goes from zero to non-zero on the very first pass). The
		// snapshot ticks (default 1s) are typically coarser than pulse
		// intervals, so this naturally de-duplicates repeats.
		var uptime float64
		if buildIndex {
			uptime = s.recordCheck(id, state.LastCheckTime, status == "up")
		}

		if buildIndex {
			var activeCodes []string
			if cc := s.codeConfigMap.Get(entity); cc != nil && len(cc.Configs) > 0 {
				activeCodes = make([]string, 0, len(cc.Configs))
				for color := range cc.Configs {
					activeCodes = append(activeCodes, color)
				}
				sort.Strings(activeCodes)
			}
			monitors = append(monitors, snapshot.MonitorSummary{
				ID:                  id,
				Name:                state.Name,
				PulseType:           pulseTypeOrUnknown(cfg),
				Status:              status,
				Incident:            state.Flags&components.StateIncidentOpen != 0,
				PendingCode:         state.PendingCode,
				ConsecutiveFailures: state.ConsecutiveFailures,
				Warning:             state.PulseWarning,
				LastCheck:           state.LastCheckTime,
				LastSuccess:         state.LastSuccessTime,
				NextCheck:           state.NextCheckTime,
				ActiveCodes:         activeCodes,
				Target:              pulseTarget(cfg),
				IntervalMs:          pulseIntervalMs(cfg),
				Uptime:              uptime,
				// No per-monitor latency is currently captured anywhere; the
				// payload contains only {type,driver} and the Result struct
				// has no latency field. Leave at 0; can be wired up later
				// when a latency source is added (e.g. PulseResult.Payload).
				LatencyMs: 0,
			})
			byID[id] = len(monitors) - 1
		}
	}

	// Periodically prune perf/lastSeenCheck entries for IDs that no longer
	// correspond to active monitors. We prune when the map grows to 2x the
	// current active count to amortize the cost across many ticks.
	if buildIndex {
		s.pruneStalePerf(seen, count)
	}

	if buildIndex {
		// Stable ordering by name for deterministic listing.
		sort.SliceStable(monitors, func(i, j int) bool {
			return monitors[i].Name < monitors[j].Name
		})
		// Rebuild ByID after sort.
		for i := range monitors {
			byID[monitors[i].ID] = i
		}
		snap.Monitors = monitors
		snap.ByID = byID
	}

	s.holder.Set(snap)
}

// classifyStatus derives a dashboard status string from monitor state flags.
func classifyStatus(state *components.MonitorState) string {
	if state.Flags&components.StateVerifying != 0 {
		return "verifying"
	}
	if state.Flags&components.StateIncidentOpen != 0 {
		return "incident"
	}
	if state.PulseFailures > 0 || state.LastError != nil {
		return "down"
	}
	if state.LastSuccessTime.IsZero() {
		return "unknown"
	}
	if state.PulseWarning != "" {
		return "degraded"
	}
	return "up"
}

func pulseTypeOrUnknown(cfg *components.PulseConfig) string {
	if cfg == nil || cfg.Type == "" {
		return "unknown"
	}
	return cfg.Type
}

// recordCheck updates the rolling uptime counter for a monitor when a new
// pulse check is observed and returns the current uptime ratio in [0,1].
// A new check is detected by comparing LastCheckTime to the previously
// observed value, so a snapshot ticking faster than the pulse interval does
// not double-count the same check.
func (s *BatchStatsSnapshotSystem) recordCheck(id uint32, lastCheck time.Time, ok bool) float64 {
	if lastCheck.IsZero() {
		return 0
	}
	st, okStat := s.perf[id]
	if !okStat {
		st = &perfStat{}
		s.perf[id] = st
	}
	prev, seen := s.lastSeenCheck[id]
	if !seen || lastCheck.After(prev) {
		st.total++
		if ok {
			st.success++
		}
		s.lastSeenCheck[id] = lastCheck
	}
	if st.total == 0 {
		return 1.0
	}
	return float64(st.success) / float64(st.total)
}

// pruneStalePerf removes perf/lastSeenCheck entries for entity IDs that are no
// longer present in the active snapshot. It runs only when the map has grown
// beyond 2x the active monitor count, amortizing cost across ticks.
func (s *BatchStatsSnapshotSystem) pruneStalePerf(seen map[uint32]struct{}, activeCount int) {
	if len(s.perf) <= 2*maxInt(activeCount, 1) {
		return
	}
	for id := range s.perf {
		if _, ok := seen[id]; !ok {
			delete(s.perf, id)
		}
	}
	for id := range s.lastSeenCheck {
		if _, ok := seen[id]; !ok {
			delete(s.lastSeenCheck, id)
		}
	}
}

// maxInt returns the larger of a and b.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// pulseTarget returns a human-readable endpoint for display, derived from the
// monitor's pulse config. Returns an empty string when unknown.
func pulseTarget(cfg *components.PulseConfig) string {
	if cfg == nil || cfg.Config == nil {
		return ""
	}
	switch c := cfg.Config.(type) {
	case *schema.PulseHTTPConfig:
		return redactURL(c.Url)
	case *schema.PulseTCPConfig:
		return joinHostPort(c.Host, c.Port)
	case *schema.PulseICMPConfig:
		return c.Host
	case *schema.PulseDNSConfig:
		return c.Host
	case *schema.PulseUDPConfig:
		return joinHostPort(c.Host, c.Port)
	case *schema.PulseGRPCConfig:
		return joinHostPort(c.Host, c.Port)
	case *schema.PulseDockerConfig:
		return c.Container
	default:
		return ""
	}
}

// redactURL strips userinfo, query, and fragment from a URL so embedded
// credentials (e.g. http://user:pass@host/) are not leaked through the
// read-only API.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// joinHostPort joins a host and port for display; returns host alone when port is 0.
func joinHostPort(host string, port int) string {
	if host == "" {
		return ""
	}
	if port == 0 {
		return host
	}
	return host + ":" + strconv.Itoa(port)
}

// pulseIntervalMs returns the pulse interval in milliseconds.
func pulseIntervalMs(cfg *components.PulseConfig) int64 {
	if cfg == nil {
		return 0
	}
	return int64(cfg.Interval / time.Millisecond)
}

// Finalize is a no-op for this system.
func (s *BatchStatsSnapshotSystem) Finalize(_ *ecs.World) {}
