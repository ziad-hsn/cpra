package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"cpra/internal/runtimeconfig"
	"cpra/internal/slo"
	"github.com/google/uuid"
	"github.com/hashicorp/raft"
	raftbolt "github.com/hashicorp/raft-boltdb/v2"
	bolt "go.etcd.io/bbolt"
)

type nodeIdentity struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Initialized bool   `json:"initialized"`
}
type submission struct {
	commands []Command
	response chan reply
}
type reply struct {
	results []Result
	err     error
}

type Store struct {
	fsm          *machine
	raft         *raft.Raft
	log          *raftbolt.BoltStore
	transport    *raft.InmemTransport
	config       runtimeconfig.Config
	nodeID       string
	requests     chan submission
	stop         chan struct{}
	done         chan struct{}
	background   sync.WaitGroup
	closeOnce    sync.Once
	closeErr     error
	mu           sync.RWMutex
	err          error
	lastCommit   time.Duration
	lastSnapshot time.Duration
	recorder     *slo.Recorder
}

type Status struct {
	Mode               string  `json:"mode"`
	Ready              bool    `json:"ready"`
	NodeID             string  `json:"node_id,omitempty"`
	SingleNode         bool    `json:"single_node"`
	CommittedIndex     uint64  `json:"committed_index"`
	Error              string  `json:"error,omitempty"`
	CommitLatencyMS    float64 `json:"commit_latency_ms"`
	SnapshotDurationMS float64 `json:"snapshot_duration_ms"`
}

func Open(ctx context.Context, config runtimeconfig.Config) (_ *Store, err error) {
	if err = config.Validate(); err != nil {
		return nil, err
	}
	s := &Store{config: config, requests: make(chan submission, 1024), stop: make(chan struct{}), done: make(chan struct{})}
	historyDir := ""
	if config.Storage.Mode == "raft" {
		if err = os.MkdirAll(config.Storage.Directory, 0700); err != nil {
			return nil, err
		}
		// bbolt's exclusive file lock also covers node identity and bootstrap.
		// A finite timeout turns a second process into an explicit startup error.
		s.log, err = raftbolt.New(raftbolt.Options{Path: filepath.Join(config.Storage.Directory, "raft.db"), BoltOptions: &bolt.Options{Timeout: time.Second}, NoSync: false})
		if err != nil {
			return nil, fmt.Errorf("open durable directory (locked or corrupt): %w", err)
		}
		historyDir = filepath.Join(config.Storage.Directory, "history")
	}
	started := false
	defer func() {
		if err != nil && !started {
			s.closeResources()
		}
	}()
	h, err := openHistory(historyDir)
	if err != nil {
		return nil, err
	}
	s.fsm = &machine{image: image{Version: FormatVersion, Monitors: make(map[string]Monitor)}, history: h}
	if config.Storage.Mode == "raft" {
		if err = s.openRaft(ctx); err != nil {
			return nil, err
		}
	}
	if err = h.Expire(time.Now().UTC()); err != nil {
		return nil, err
	}
	s.recorder = slo.New(time.Now().UTC(), config.SLO.QueueTarget, config.SLO.ResultTarget)
	if s.fsm.image.SLO.QueueTarget == config.SLO.QueueTarget && s.fsm.image.SLO.ResultTarget == config.SLO.ResultTarget {
		s.recorder.Restore(s.fsm.image.SLO, time.Now().UTC())
	}
	started = true
	go s.run()
	s.background.Add(1)
	go s.maintain()
	if _, err = s.Submit(ctx, []Command{{Kind: "recover", At: time.Now().UTC()}}); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) openRaft(ctx context.Context) error {
	dir := s.config.Storage.Directory
	snaps, err := raft.NewFileSnapshotStore(dir, s.config.Storage.SnapshotRetain, io.Discard)
	if err != nil {
		return err
	}
	exists, err := raft.HasExistingState(s.log, s.log, snaps)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "identity.json")
	data, err := os.ReadFile(path)
	var node nodeIdentity
	if err == nil {
		if json.Unmarshal(data, &node) != nil || node.Version != FormatVersion {
			return fmt.Errorf("corrupt or incompatible durable node identity")
		}
		if _, err := uuid.Parse(node.ID); err != nil {
			return fmt.Errorf("invalid durable node identity")
		}
		if node.Initialized && !exists {
			return fmt.Errorf("initialized durable node has lost its log and snapshots; restore a backup")
		}
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if exists {
			return fmt.Errorf("durable state exists without node identity; restore a complete backup")
		}
		node = nodeIdentity{Version: FormatVersion, ID: uuid.NewString()}
		if err = atomicJSON(path, node); err != nil {
			return err
		}
	}
	s.nodeID = node.ID
	// Validate every completed snapshot before Raft can choose an older one.
	// Corruption must be visible rather than silently losing recent state.
	list, err := snaps.List()
	if err != nil {
		return err
	}
	for _, meta := range list {
		_, reader, err := snaps.Open(meta.ID)
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", meta.ID, err)
		}
		_, err = decodeImage(reader)
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			return fmt.Errorf("snapshot %s: %w", meta.ID, errors.Join(err, closeErr))
		}
	}
	addr, transport := raft.NewInmemTransport(raft.ServerAddress(node.ID))
	s.transport = transport
	c := raft.DefaultConfig()
	c.LocalID = raft.ServerID(node.ID)
	c.LogOutput = io.Discard
	c.HeartbeatTimeout = 100 * time.Millisecond
	c.ElectionTimeout = 100 * time.Millisecond
	c.LeaderLeaseTimeout = 100 * time.Millisecond
	c.CommitTimeout = 5 * time.Millisecond
	c.SnapshotThreshold = math.MaxUint64 // explicit periodic snapshots below
	c.TrailingLogs = 1000
	s.raft, err = raft.NewRaft(c, s.fsm, s.log, s.log, snaps, transport)
	if err != nil {
		return fmt.Errorf("open Raft: %w", err)
	}
	if !exists {
		if err = s.raft.BootstrapCluster(raft.Configuration{Servers: []raft.Server{{ID: c.LocalID, Address: addr, Suffrage: raft.Voter}}}).Error(); err != nil {
			return err
		}
		node.Initialized = true
		if err = atomicJSON(path, node); err != nil {
			return err
		}
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for s.raft.State() != raft.Leader {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("single-node Raft did not become ready")
		case <-ticker.C:
		}
	}
	if err = s.raft.Barrier(10 * time.Second).Error(); err != nil {
		return err
	}
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	return s.fsm.err
}

func (s *Store) Submit(ctx context.Context, commands []Command) ([]Result, error) {
	if len(commands) == 0 {
		return nil, nil
	}
	if len(commands) > s.config.Storage.BatchSize {
		return nil, fmt.Errorf("durable batch exceeds %d commands", s.config.Storage.BatchSize)
	}
	for _, c := range commands {
		if err := validateCommand(c); err != nil {
			return nil, err
		}
	}
	// Transfer private command data to the submission goroutine.
	copyCommands := append([]Command(nil), commands...)
	for n := range copyCommands {
		if copyCommands[n].Config != nil {
			m := copyCommands[n].Config.Clone()
			copyCommands[n].Config = &m
		}
		if copyCommands[n].SLO != nil {
			v := copyCommands[n].SLO.Clone()
			copyCommands[n].SLO = &v
		}
	}
	req := submission{commands: copyCommands, response: make(chan reply, 1)}
	select {
	case <-s.stop:
		return nil, fmt.Errorf("durable store closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	case s.requests <- req:
	}
	select {
	case r := <-req.response:
		return r.results, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("commit outcome unknown: %w", ctx.Err())
	case <-s.stop:
		return nil, fmt.Errorf("durable store closed; commit outcome unknown")
	}
}

func (s *Store) run() {
	defer close(s.done)
	var pending *submission
	for {
		var first submission
		if pending != nil {
			first = *pending
			pending = nil
		} else {
			select {
			case <-s.stop:
				return
			case first = <-s.requests:
			}
		}
		requests := []submission{first}
		count := len(first.commands)
		timer := time.NewTimer(s.config.Storage.BatchDelay)
	collect:
		for count < s.config.Storage.BatchSize {
			select {
			case req := <-s.requests:
				if count+len(req.commands) > s.config.Storage.BatchSize {
					pending = &req
					break collect
				}
				requests = append(requests, req)
				count += len(req.commands)
			case <-timer.C:
				break collect
			case <-s.stop:
				break collect
			}
		}
		timer.Stop()
		commands := make([]Command, 0, count)
		for _, req := range requests {
			commands = append(commands, req.commands...)
		}
		results, err := s.apply(commands)
		pos := 0
		for _, req := range requests {
			r := reply{err: err}
			if err == nil {
				r.results = results[pos : pos+len(req.commands)]
			}
			pos += len(req.commands)
			req.response <- r
		}
	}
}

func (s *Store) apply(commands []Command) ([]Result, error) {
	s.mu.RLock()
	err := s.err
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(envelope{Version: FormatVersion, Commands: commands})
	if err != nil {
		return nil, err
	}
	start := time.Now()
	var response any
	if s.raft != nil {
		future := s.raft.Apply(data, 10*time.Second)
		err = future.Error()
		if err == nil {
			response = future.Response()
		}
	} else {
		response = s.fsm.Apply(&raft.Log{Index: s.fsm.image.Index + 1, Data: data})
	}
	if e, ok := response.(error); ok {
		err = e
	}
	s.mu.Lock()
	s.lastCommit = time.Since(start)
	if err != nil {
		s.err = err
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	results, ok := response.([]Result)
	if !ok {
		return nil, fmt.Errorf("invalid state machine response")
	}
	return results, nil
}

func (s *Store) Get(id string) (Monitor, bool) {
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	m, ok := s.fsm.image.Monitors[id]
	return m.Clone(), ok
}

func (s *Store) SLO() *slo.Recorder { return s.recorder }

func (s *Store) History() *HistoryStore { return s.fsm.history }
func (s *Store) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	status := Status{Mode: s.config.Storage.Mode, NodeID: s.nodeID, SingleNode: true, Ready: s.err == nil && s.fsm.err == nil, CommittedIndex: s.fsm.image.Index, CommitLatencyMS: float64(s.lastCommit) / float64(time.Millisecond), SnapshotDurationMS: float64(s.lastSnapshot) / float64(time.Millisecond)}
	select {
	case <-s.stop:
		status.Ready = false
	default:
	}
	if s.raft != nil && s.raft.State() != raft.Leader {
		status.Ready = false
	}
	if s.err != nil || s.fsm.err != nil {
		status.Error = "durable storage unavailable; inspect local diagnostics"
	}
	return status
}

func (s *Store) Snapshot() error {
	if s.raft == nil {
		return nil
	}
	start := time.Now()
	err := s.raft.Snapshot().Error()
	if errors.Is(err, raft.ErrNothingNewToSnapshot) {
		return nil
	}
	s.mu.Lock()
	s.lastSnapshot = time.Since(start)
	if err != nil {
		s.err = err
	}
	s.mu.Unlock()
	return err
}

func (s *Store) maintain() {
	defer s.background.Done()
	ticker := time.NewTicker(s.config.Storage.SnapshotInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case at := <-ticker.C:
			if err := s.fsm.history.Expire(at); err != nil {
				s.mu.Lock()
				s.err = err
				s.mu.Unlock()
				continue
			}
			_ = s.Snapshot()
		}
	}
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() { close(s.stop); <-s.done; s.background.Wait(); s.closeErr = s.closeResources() })
	return s.closeErr
}
func (s *Store) closeResources() error {
	var err error
	if s.raft != nil {
		err = errors.Join(err, s.raft.Shutdown().Error())
	}
	if s.transport != nil {
		err = errors.Join(err, s.transport.Close())
	}
	if s.fsm != nil && s.fsm.history != nil {
		err = errors.Join(err, s.fsm.history.Close())
	}
	if s.log != nil {
		err = errors.Join(err, s.log.Close())
	}
	return err
}

// Reconcile cancels unsent work for IDs removed from the manifest. The caller
// supplies the complete, already validated set before any workers start.
func (s *Store) Reconcile(ctx context.Context, present map[string]bool, at time.Time) error {
	s.fsm.mu.RLock()
	var commands []Command
	for id, m := range s.fsm.image.Monitors {
		if !present[id] && !m.Removed {
			commands = append(commands, Command{Kind: "remove", MonitorID: id, Revision: m.Revision, At: at})
		}
	}
	s.fsm.mu.RUnlock()
	slices.SortFunc(commands, func(a, b Command) int { return strings.Compare(a.MonitorID, b.MonitorID) })
	for len(commands) > 0 {
		n := min(len(commands), s.config.Storage.BatchSize)
		if _, err := s.Submit(ctx, commands[:n]); err != nil {
			return err
		}
		commands = commands[n:]
	}
	return nil
}
func (s *Store) MarkUnavailable(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}
func (s *Store) SLOView(at time.Time) slo.View {
	return s.recorder.View(at, s.config.SLO.Window, uint64(s.config.SLO.MinimumSamples))
}
