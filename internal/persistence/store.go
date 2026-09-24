package persistence

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

	"github.com/google/uuid"
	"github.com/hashicorp/raft"
	raftbolt "github.com/hashicorp/raft-boltdb/v2"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/slo"
	bolt "go.etcd.io/bbolt"
)

type nodeIdentity struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Initialized bool   `json:"initialized"`
	RestoreID   string `json:"restore_id,omitempty"`
}
type submission struct {
	isolated bool   // Derived from validated commands, never a caller option.
	data     []byte // Private comma-separated encoded commands, without envelope.
	count    int
	version  int
	response chan reply
	bytes    int
}
type reply struct {
	results []Result
	err     error
}

// ErrCommitUnconfirmed means admission occurred but the caller cannot establish
// whether the command committed. Reconcile its original identity and version;
// cancellation does not roll it back or make a new mutation safe to retry.
var ErrCommitUnconfirmed = errors.New("durable commit outcome unknown")

type Store struct {
	administrative  bool
	opening         bool
	restoreMarker   *RestoreMarker
	executorMu      sync.Mutex
	executorSession string
	executorClosing bool
	validationRunID string
	executionRunID  string
	executors       map[string]*LocalExecution
	admissionMu     sync.Mutex
	submitters      sync.WaitGroup
	budget          commitBudget
	fsm             *machine
	raft            *raft.Raft
	log             *raftbolt.BoltStore
	transport       *raft.InmemTransport
	config          runtimeconfig.Config
	nodeID          string
	requests        chan submission
	stop            chan struct{}
	done            chan struct{}
	background      sync.WaitGroup
	closeOnce       sync.Once
	closeErr        error
	mu              sync.RWMutex
	err             error
	lastCommit      time.Duration
	lastSnapshot    time.Duration
	recorder        *slo.Recorder
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

func Open(ctx context.Context, config runtimeconfig.Config) (*Store, error) {
	return openStore(ctx, config, false)
}

// OpenAdministrative takes exclusive stopped-store ownership and opens only
// Raft/history needed for local authentication commits. It never runs lifecycle
// recovery, starts a local executor session, expires history or snapshots in the
// background. Existing committed restore reset phases are finished before return.
func OpenAdministrative(ctx context.Context, config runtimeconfig.Config) (*Store, error) {
	if config.Storage.Mode != "raft" {
		return nil, ErrAuthenticationAdminRequired
	}
	return openStore(ctx, config, true)
}

func openStore(ctx context.Context, config runtimeconfig.Config, administrative bool) (_ *Store, err error) {
	if ctx == nil {
		return nil, errors.New("durable opening requires a context")
	}
	if err = config.Validate(); err != nil {
		return nil, err
	}
	if config.Storage.Mode == "raft" && config.Storage.Directory == "" {
		return nil, fmt.Errorf("storage.directory must be resolved before opening Raft storage")
	}
	s := &Store{administrative: administrative, opening: true, executorSession: uuid.NewString(), config: config, nodeID: uuid.NewString(), requests: make(chan submission, 1024), stop: make(chan struct{}), done: make(chan struct{})}
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
	h, err := openHistoryWithContext(ctx, historyDir, false)
	if err != nil {
		return nil, err
	}
	s.fsm = &machine{image: image{Version: FormatVersion, Monitors: make(map[string]Monitor)}, history: h}
	if config.Storage.Mode == "raft" {
		s.fsm.collectionDirectory = filepath.Join(config.Storage.Directory, "collections")
		if err = s.openRaft(ctx); err != nil {
			return nil, err
		}
	}
	s.recorder = slo.New(time.Now().UTC(), config.SLO.QueueTarget, config.SLO.ResultTarget)
	if s.fsm.image.SLO.QueueTarget == config.SLO.QueueTarget && s.fsm.image.SLO.ResultTarget == config.SLO.ResultTarget {
		s.recorder.Restore(s.fsm.image.SLO, time.Now().UTC())
	}
	started = true
	go s.run()
	if err = s.finishRestore(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	// The generation is selected only after complete snapshot/log recovery and
	// explicit restore fencing. An old materialization never decides replay.
	if s.fsm.collections != nil {
		if err = s.fsm.collections.Publish(); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	s.opening = false
	if administrative {
		return s, nil
	}
	if s.fsm.image.Authentication != nil && s.fsm.image.Authentication.ResetRequired {
		_ = s.Close()
		return nil, ErrAuthenticationResetRequired
	}
	if err = h.migrateOperationIndexes(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("operation history index migration: %w", err)
	}
	if err = h.Expire(time.Now().UTC()); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.background.Add(1)
	go s.maintain()
	if _, err = s.Submit(ctx, []Command{{Kind: "recover", At: time.Now().UTC()}}); err != nil {
		s.Close()
		return nil, err
	}
	if _, err = s.Submit(ctx, []Command{{Kind: "local_session", At: time.Now().UTC(), ExecutorSession: s.executorSession}}); err != nil {
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
		if json.Unmarshal(data, &node) != nil || node.Version != FormatVersion && node.Version != restoredIdentityVersion {
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
	s.restoreMarker, err = readRestoreMarker(dir, node)
	if err != nil {
		return err
	}
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
		validationDir, tempErr := os.MkdirTemp(dir, ".collection-snapshot-validation-")
		if tempErr != nil {
			_ = reader.Close()
			return tempErr
		}
		_, ledger, decodeErr := decodeSnapshot(reader, validationDir)
		err = decodeErr
		if ledger != nil {
			err = errors.Join(err, ledger.Close())
		}
		err = errors.Join(err, os.RemoveAll(validationDir))
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
	if s.fsm.err == nil {
		s.fsm.history.mu.RLock()
		watermark := s.fsm.history.catalog.Index
		s.fsm.history.mu.RUnlock()
		if s.fsm.image.Index < watermark {
			return errors.New("committed application log does not reach retained history; restore a complete backup")
		}
	}
	return s.fsm.err
}

func (s *Store) Submit(ctx context.Context, commands []Command) ([]Result, error) {
	if ctx == nil {
		return nil, errors.New("durable submission requires a context")
	}
	if len(commands) == 0 {
		return nil, nil
	}
	for _, c := range commands {
		if c.CollectionExecute != nil && len(commands) != 1 {
			return nil, ErrCollectionInvalid
		}
		if administrativeCommandExtension(c) && !s.administrative {
			return nil, ErrAuthenticationAdminRequired
		}
		if s.administrative && c.Kind != "authentication" && c.Kind != "restore_reset" && c.Kind != "barrier" && !administrativeCommandExtension(c) {
			return nil, ErrAuthenticationAdminRequired
		}
		if c.Kind == "authentication" && c.Authentication != nil && c.Authentication.Mode != "bootstrap" && !s.administrative {
			return nil, ErrAuthenticationAdminRequired
		}
		if c.Kind == "restore_reset" && (!s.opening || s.restoreMarker == nil || c.Restore == nil || c.Restore.Marker != *s.restoreMarker) {
			return nil, ErrRestoreInvalid
		}
	}
	// Close prevents new Add calls before the submission loop waits for callers.
	s.admissionMu.Lock()
	select {
	case <-s.stop:
		s.admissionMu.Unlock()
		return nil, errors.New("durable store closed")
	default:
		s.submitters.Add(1)
	}
	s.admissionMu.Unlock()
	defer s.submitters.Done()
	if len(commands) > s.config.Storage.BatchSize {
		return nil, fmt.Errorf("durable batch exceeds %d commands", s.config.Storage.BatchSize)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bound, err := encodedBound(envelope{Version: CatalogFormatVersion, Commands: commands})
	if err != nil {
		return nil, err
	}
	if err := s.budget.acquire(ctx, s.stop, bound); err != nil {
		return nil, err
	}
	reserved := bound
	defer func() { s.budget.release(reserved) }()
	// Validate and encode only after reserving scratch space. Keeping encoded
	// commands avoids cloning entire object graphs and encoding them again later.
	data := make([]byte, 0, min(bound, 4096))
	version := FormatVersion
	for n, c := range commands {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validateCommand(c); err != nil {
			return nil, err
		}
		version = max(version, commandWriteFormat(c))
		encoded, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		if n != 0 {
			data = append(data, ',')
		}
		data = append(data, encoded...)
	}
	// Reserve encoded bytes until the submission goroutine finishes the commit,
	// including when the caller stops waiting for an ambiguous outcome.
	size := len(data) + len(commandPrefix(version)) + 2
	if size > bound || size > maxCommitBytes {
		return nil, errors.New("durable encoded command exceeds its admission bound")
	}
	s.budget.release(bound - size)
	reserved = size
	req := submission{data: data, count: len(commands), version: version, response: make(chan reply, 1), bytes: size,
		isolated: commands[0].CollectionExecute != nil}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-s.stop:
		return nil, fmt.Errorf("durable store closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	case s.requests <- req:
		reserved = 0 // The submission loop now owns the reservation.
	}
	select {
	case r := <-req.response:
		return r.results, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrCommitUnconfirmed, ctx.Err())
	case <-s.stop:
		return nil, fmt.Errorf("%w: durable store closed", ErrCommitUnconfirmed)
	}
}

func (s *Store) run() {
	var pending *submission
	defer func() {
		// Canceled callers stop waiting even when they raced with channel send.
		// Wait until all such sends have finished before releasing the final queue.
		s.submitters.Wait()
		finish := func(req submission) {
			s.budget.release(req.bytes)
			req.response <- reply{err: errors.New("durable store closed before commit")}
		}
		if pending != nil {
			finish(*pending)
		}
		for {
			select {
			case req := <-s.requests:
				finish(req)
			default:
				close(s.done)
				return
			}
		}
	}()
	for {
		select {
		case <-s.stop:
			return
		default:
		}
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
		count := first.count
		bytes := first.bytes
		timer := time.NewTimer(s.config.Storage.BatchDelay)
	collect:
		for !first.isolated && count < s.config.Storage.BatchSize {
			select {
			case req := <-s.requests:
				if req.isolated || count+req.count > s.config.Storage.BatchSize || bytes+req.bytes > maxCommitBytes {
					pending = &req
					break collect
				}
				requests = append(requests, req)
				count += req.count
				bytes += req.bytes
			case <-timer.C:
				break collect
			case <-s.stop:
				break collect
			}
		}
		timer.Stop()
		version := FormatVersion
		for _, req := range requests {
			version = max(version, req.version)
		}
		data := make([]byte, 0, bytes)
		data = append(data, commandPrefix(version)...)
		for n, req := range requests {
			if n != 0 {
				data = append(data, ',')
			}
			data = append(data, req.data...)
		}
		data = append(data, ']', '}')
		results, err := s.apply(data)
		pos := 0
		for _, req := range requests {
			r := reply{err: err}
			if err == nil {
				r.results = results[pos : pos+req.count]
			}
			pos += req.count
			s.budget.release(req.bytes)
			req.response <- r
		}
	}
}

func commandPrefix(version int) string {
	return fmt.Sprintf(`{"version":%d,"commands":[`, version)
}

func (s *Store) apply(data []byte) ([]Result, error) {
	s.mu.RLock()
	err := s.err
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if len(data) > maxCommitBytes {
		return nil, fmt.Errorf("durable batch exceeds encoded byte limit")
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
	if err != nil && !IsLeadershipUnavailable(err) {
		s.err = err
	}
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCommitUnconfirmed, err)
	}
	results, ok := response.([]Result)
	if !ok {
		return nil, fmt.Errorf("%w: invalid state machine response", ErrCommitUnconfirmed)
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
	if s.fsm.bootstrapPending() {
		status.Ready = false
		status.Error = "configuration migration is incomplete"
	}
	if s.fsm.restorePending() || s.fsm.image.Authentication != nil && s.fsm.image.Authentication.ResetRequired {
		status.Ready = false
		status.Error = "explicit restore requires stopped authentication provisioning"
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
	if err != nil && !IsLeadershipUnavailable(err) {
		s.err = err
	}
	s.mu.Unlock()
	return err
}

func (s *Store) maintain() {
	defer s.background.Done()
	ticker := time.NewTicker(s.config.Storage.SnapshotInterval)
	defer ticker.Stop()
	collectionTicker := time.NewTicker(time.Second)
	defer collectionTicker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-collectionTicker.C:
			if err := s.maintainCollections(time.Now().UTC()); err != nil {
				s.mu.Lock()
				s.err = err
				s.mu.Unlock()
			}
		case <-ticker.C:
			at := time.Now().UTC()
			if err := s.maintainOperationReservations(at); err != nil {
				s.mu.Lock()
				s.err = err
				s.mu.Unlock()
				continue
			}
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
	s.executorMu.Lock()
	if len(s.executors) != 0 {
		s.executorMu.Unlock()
		return ErrLocalExecutorActive
	}
	s.executorClosing = true
	s.executorMu.Unlock()
	s.closeOnce.Do(func() {
		s.admissionMu.Lock()
		close(s.stop)
		s.admissionMu.Unlock()
		<-s.done
		s.background.Wait()
		s.closeErr = s.closeResources()
	})
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
	if s.fsm != nil && s.fsm.collections != nil {
		err = errors.Join(err, s.fsm.collections.Close())
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
			command := Command{Kind: "remove", MonitorID: id, Revision: m.Revision, At: at}
			if record, ok := s.fsm.image.Catalog[(CatalogKey{Kind: "Monitor", ID: id}).indexKey()]; ok {
				if !record.Removed {
					s.fsm.mu.RUnlock()
					return ErrCatalogDependency
				}
				command.Guard = &CatalogGuard{Removed: true, Conditions: []CatalogCondition{{Key: record.Key, UID: record.UID, Revision: record.Revision}}}
			}
			commands = append(commands, command)
		}
	}
	s.fsm.mu.RUnlock()
	slices.SortFunc(commands, func(a, b Command) int { return strings.Compare(a.MonitorID, b.MonitorID) })
	for len(commands) > 0 {
		n := min(len(commands), s.config.Storage.BatchSize)
		results, err := s.Submit(ctx, commands[:n])
		if err != nil {
			return err
		}
		for _, result := range results {
			if result.Err != nil {
				return result.Err
			}
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
