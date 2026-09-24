package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ziad-hsn/cpra/internal/slo"
	"io"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/google/btree"
	"github.com/hashicorp/raft"
)

type envelope struct {
	Version  int       `json:"version"`
	Commands []Command `json:"commands"`
}

// decodeEnvelope applies the same fail-closed schema checks during replay and
// stopped backup validation. Silently ignoring a future field can discard a
// precondition and apply a mutation the writer intended to reject.
func decodeEnvelope(data []byte) (envelope, error) {
	var batch envelope
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&batch); err != nil {
		return batch, errors.New("corrupt command envelope")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return batch, errors.New("unexpected trailing command data")
	}
	if !supportedFormat(batch.Version) {
		return batch, fmt.Errorf("incompatible command format %d", batch.Version)
	}
	if len(batch.Commands) == 0 {
		return batch, errors.New("committed command envelope is empty")
	}
	for _, c := range batch.Commands {
		if c.CollectionExecute != nil && (!collectionExecutionStorageFormat(batch.Version) || len(batch.Commands) != 1) {
			return batch, errors.New("collection execution requires an isolated execution log entry")
		}
		if batch.Version < commandMinimumFormat(c) {
			return batch, fmt.Errorf("command requires storage format %d", commandMinimumFormat(c))
		}
		if err := validateCommand(c); err != nil {
			return batch, err
		}
	}
	return batch, nil
}

type image struct {
	imageExtensions
	CatalogMutationSequence      uint64                          `json:"catalog_mutation_sequence,omitempty"`
	CollectionAdmissions         map[string]CollectionAdmission  `json:"collection_admissions,omitempty"`
	CollectionAdmissionWatermark time.Time                       `json:"collection_admission_watermark,omitempty"`
	Collections                  map[string]CollectionState      `json:"collections,omitempty"`
	OperationHighWater           uint64                          `json:"operation_high_water,omitempty"`
	OperationReservations        map[string]OperationReservation `json:"operation_reservations,omitempty"`
	Authentication               *AuthenticationState            `json:"authentication,omitempty"`
	Restore                      *RestoreState                   `json:"restore,omitempty"`
	OperationEpoch               string                          `json:"operation_epoch,omitempty"`
	LocalExecutorSession         string                          `json:"local_executor_session,omitempty"`
	Bootstrap                    *BootstrapState                 `json:"bootstrap,omitempty"`
	Operations                   map[string]OperationReceipt     `json:"operations,omitempty"`
	Catalog                      map[string]CatalogRecord        `json:"catalog,omitempty"`
	SLO                          slo.State                       `json:"slo"`
	Version                      int                             `json:"version"`
	Index                        uint64                          `json:"index"`
	Monitors                     map[string]Monitor              `json:"monitors"`
}
type machine struct {
	// Apply owns this across its outside-mu verification and mutation phases.
	// Restore takes the same gate before replacing the authoritative generation.
	transition                    sync.Mutex
	collectionSourceCertificate   *collectionExecutionSourceCertificate
	collectionPublicationIndex    *collectionExecutionItemIndex
	collectionPublicationLedger   *collectionLedger
	collectionExecutionIndex      *collectionExecutionIndex
	collectionExecutionLedger     *collectionLedger
	collectionChildren            collectionChildLinks
	collectionTerminalTrees       map[string]*collectionExecutionTerminalTree
	collectionOutcomeCommitments  map[string]*collectionExecutionOutcomeCommitments
	collections                   *collectionLedger
	planPrefixes                  map[string]*collectionPlanPrefix
	validationPlanItems           map[string]map[uint64]CollectionValidationItem // Derived only; never snapshot authority.
	collectionDirectory           string
	operationVersions             map[operationVersionKey]string
	actionIndex, actionsByMonitor *btree.BTreeG[actionItem]
	incidents                     *btree.BTreeG[IncidentRecord]
	incidentsByMonitor            *btree.BTreeG[IncidentRecord]
	controls                      *btree.BTreeG[ControlChange]
	controlChanges                []ControlChange
	controlSequence, controlEpoch uint64
	catalogChanges                []CatalogChange
	catalogSequence               uint64
	catalogEpoch                  uint64
	catalog                       *btree.BTreeG[catalogItem]
	catalogIncoming               map[string]map[string]struct{}
	mu                            sync.RWMutex
	image                         image
	history                       *HistoryStore
	err                           error
}

func (f *machine) Apply(log *raft.Log) any {
	f.transition.Lock()
	defer f.transition.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	batch, err := decodeEnvelope(log.Data)
	if err != nil {
		f.err = fmt.Errorf("invalid committed command at %d: %w", log.Index, err)
		return f.err
	}
	var executionIndex *collectionExecutionIndex
	var executionErr error
	var publicationPage *collectionExecutionItemPage
	var sourceRetirement *collectionExecutionSourceRetirementBatch
	if len(batch.Commands) == 1 && batch.Commands[0].CollectionExecute != nil {
		c := batch.Commands[0]
		if c.CollectionExecute.Action == "retire_sources" {
			sourceRetirement, executionErr = f.prepareCollectionExecutionSourceRetirement(*c.CollectionExecute, c.At)
		} else if c.CollectionExecute.Action == "retire" {
			executionErr = f.prepareCollectionExecutionRetirement(*c.CollectionExecute, c.At)
		} else if c.CollectionExecute.Action == "publish" {
			publicationPage, executionErr = f.prepareCollectionExecutionPublication(*c.CollectionExecute, c.At)
		} else {
			executionIndex, executionErr = f.prepareCollectionExecutionIndex(*c.CollectionExecute, c.At)
		}
		if f.err != nil {
			return f.err
		}
	}
	results := make([]Result, 0, len(batch.Commands))
	var events []Event
	var at time.Time
	for _, c := range batch.Commands {
		if c.At.After(at) {
			at = c.At
		}
		if mutatesCatalog(c) && !catalogMutationFormat(batch.Version) && catalogMutationFormat(f.image.Version) {
			results = append(results, Result{Err: ErrCatalogFormatDowngrade})
			continue
		}
		if c.Kind == "authentication" {
			r := f.applyAuthentication(*c.Authentication)
			results = append(results, r)
			events = append(events, r.Events...)
			continue
		}
		if c.Kind == "restore_reset" {
			r := f.applyRestore(*c.Restore)
			if f.err != nil {
				return f.err
			}
			results = append(results, r)
			events = append(events, r.Events...)
			continue
		}
		if f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired {
			results = append(results, Result{Err: ErrAuthenticationResetRequired})
			continue
		}
		if c.Kind == "barrier" {
			results = append(results, Result{Allowed: true})
			continue
		}
		if c.Kind == "bootstrap" {
			results = append(results, f.applyBootstrap(*c.Bootstrap, log.Index, c.At, batch.Version))
			continue
		}
		if f.bootstrapPending() && c.Kind != "recover" && c.Kind != "slo" && c.Kind != "local_session" {
			results = append(results, Result{Err: ErrBootstrapPending})
			continue
		}
		if r, handled := f.applyCommandExtensions(c, log.Index); handled {
			results = append(results, r)
			events = append(events, r.Events...)
			continue
		}
		if c.Kind == "local_session" || c.Kind == "executor_finished" {
			r := f.applyExecutorCommand(c)
			if r.Monitor != nil {
				events = append(events, f.installMonitor(f.image.Monitors[c.MonitorID], *r.Monitor, c.At)...)
			}
			results = append(results, r)
			continue
		}
		if c.Kind == "start" && c.ExecutorSession != "" && c.ExecutorSession != f.image.LocalExecutorSession {
			results = append(results, Result{Err: ErrExecutorUnfenced})
			continue
		}
		if c.Kind == "operation_reserve" {
			r := f.applyOperationAllocation(*c.OperationAllocation, c.At)
			results = append(results, r)
			events = append(events, r.Events...)
			continue
		}
		if c.Kind == "collection" {
			r := f.applyCollection(*c.Collection, c.At, batch.Version)
			if f.err != nil {
				return f.err
			}
			results = append(results, r)
			events = append(events, r.Events...)
			continue
		}
		if c.Kind == "collection_execute" {
			r := Result{Err: executionErr}
			if executionErr == nil {
				if c.CollectionExecute.Action == "retire_sources" {
					r = f.retireCollectionExecutionSources(*c.CollectionExecute, c.At, sourceRetirement)
				} else if c.CollectionExecute.Action == "retire" {
					r = f.retireCollectionExecution(*c.CollectionExecute, c.At)
				} else if c.CollectionExecute.Action == "publish" {
					r = f.publishCollectionExecution(*c.CollectionExecute, c.At, publicationPage)
				} else {
					r = f.applyCollectionExecution(*c.CollectionExecute, log.Index, c.At, executionIndex)
				}
			}
			if f.err != nil {
				return f.err
			}
			results = append(results, r)
			events = append(events, r.Events...)
			continue
		}
		if c.Kind == "operation_expire" {
			r := Result{Allowed: true, Events: f.expireOperationReservations(c.At)}
			results = append(results, r)
			events = append(events, r.Events...)
			continue
		}
		if c.Kind == "catalog" || c.Kind == "control" || c.Kind == "manual_recovery" || c.Kind == "action_review" {
			r := f.applyOperationTarget(c, log.Index, batch.Version)
			if f.err != nil {
				return f.err
			}
			results = append(results, r)
			events = append(events, r.Events...)
			continue
		}
		if c.Kind == "operation" {
			r := f.applyOperation(*c.Operation, c.At)
			if f.err != nil {
				return f.err
			}
			results = append(results, r)
			events = append(events, r.Events...)
			continue
		}
		if c.Kind == "slo" {
			f.image.SLO = c.SLO.Clone()
			results = append(results, Result{Allowed: true})
			continue
		}
		if c.Kind == "recover" {
			ids := make([]string, 0)
			for id, m := range f.image.Monitors {
				for _, a := range m.Actions {
					if a.State == Started {
						ids = append(ids, id)
						break
					}
				}
			}
			slices.Sort(ids)
			for _, id := range ids {
				r := transition(f.image.Monitors[id], c)
				events = append(events, f.installMonitor(f.image.Monitors[id], *r.Monitor, c.At)...)
				events = append(events, r.Events...)
			}
			results = append(results, Result{Allowed: true})
			continue
		}
		if c.Kind == "configure" || c.Kind == "start" || c.Kind == "pulse" || c.Kind == "remove" {
			if err := f.checkCatalogGuard(c.MonitorID, c.Guard); err != nil {
				results = append(results, Result{Err: err})
				continue
			}
			if c.Guard != nil {
				uid := c.Guard.monitorUID(c.MonitorID)
				if c.Kind == "configure" {
					config := c.Config.Clone()
					config.CatalogUID = uid
					config.DependencyRevision = c.Guard.revision()
					for _, condition := range c.Guard.Conditions {
						if condition.Key == (CatalogKey{Kind: "Monitor", ID: c.MonitorID}) {
							config.CatalogRevision = condition.Revision
							break
						}
					}
					c.Config = &config
				} else if c.Kind != "remove" && f.image.Monitors[c.MonitorID].CatalogUID != uid {
					results = append(results, Result{Err: ErrCatalogDependency})
					continue
				}
			}
		}
		r := transition(f.image.Monitors[c.MonitorID], c)
		if r.Monitor != nil {
			events = append(events, f.installMonitor(f.image.Monitors[c.MonitorID], *r.Monitor, c.At)...)
		}
		events = append(events, r.Events...)
		results = append(results, r)
	}
	for n := range events {
		events[n].ID = fmt.Sprintf("%020d:%08d", log.Index, n)
	}
	if err := f.history.append(log.Index, events, at); err != nil {
		f.err = fmt.Errorf("durable history commit failed: %w", err)
		return f.err
	}
	f.image.Index = log.Index
	// Every command path returns detached observations. Control/action handlers
	// also install immutable maps in the FSM and must not expose those maps.
	for n := range results {
		if results[n].Monitor != nil {
			copy := results[n].Monitor.Clone()
			results[n].Monitor = &copy
		}
	}
	return results
}

func (f *machine) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.err != nil {
		return nil, f.err
	}
	f.history.mu.RLock()
	index := f.history.catalog.Index
	f.history.mu.RUnlock()
	if index < f.image.Index {
		return nil, fmt.Errorf("history has not reached the snapshot position")
	}
	i := image{LocalExecutorSession: f.image.LocalExecutorSession, Version: f.image.Version, Index: f.image.Index, SLO: f.image.SLO.Clone(), Monitors: make(map[string]Monitor, len(f.image.Monitors))}
	i.imageExtensions = cloneImageExtensions(f.image)
	i.CatalogMutationSequence = f.image.CatalogMutationSequence
	i.OperationEpoch = f.image.OperationEpoch
	i.OperationHighWater = f.image.OperationHighWater
	i.CollectionAdmissionWatermark = f.image.CollectionAdmissionWatermark
	if f.image.CollectionAdmissions != nil {
		i.CollectionAdmissions = make(map[string]CollectionAdmission, len(f.image.CollectionAdmissions))
		for id, admission := range f.image.CollectionAdmissions {
			i.CollectionAdmissions[id] = admission
		}
	}
	if f.image.Collections != nil {
		i.Collections = make(map[string]CollectionState, len(f.image.Collections))
		for id, state := range f.image.Collections {
			i.Collections[id] = state // Envelopes are immutable after admission.
		}
	}
	if f.image.OperationReservations != nil {
		i.OperationReservations = make(map[string]OperationReservation, len(f.image.OperationReservations))
		for id, r := range f.image.OperationReservations {
			i.OperationReservations[id] = r
		}
	}
	if f.image.Authentication != nil {
		copy := f.image.Authentication.Clone()
		i.Authentication = &copy
	}
	if f.image.Restore != nil {
		copy := *f.image.Restore
		i.Restore = &copy
	}
	if f.image.Bootstrap != nil {
		copy := *f.image.Bootstrap
		i.Bootstrap = &copy
	}
	if f.image.Operations != nil {
		i.Operations = make(map[string]OperationReceipt, len(f.image.Operations))
		for id, receipt := range f.image.Operations {
			i.Operations[id] = receipt
		}
	}
	if f.image.Catalog != nil {
		i.Catalog = make(map[string]CatalogRecord, len(f.image.Catalog))
		for k, record := range f.image.Catalog {
			// Ciphertext/reference slices are immutable once admitted by Submit.
			// A map copy freezes the record generations without copying all blobs.
			i.Catalog[k] = record
		}
	}
	for k, m := range f.image.Monitors {
		i.Monitors[k] = m.Clone()
	}
	snapshot := &frozenSnapshot{image: i}
	if collectionFormat(i.Version) {
		var err error
		snapshot.collections, err = f.collectionSnapshotView()
		if err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func decodeImage(reader io.Reader) (image, error) {
	d := json.NewDecoder(reader)
	d.DisallowUnknownFields()
	i, err := decodeImageValue(d)
	if err != nil {
		return i, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return i, fmt.Errorf("unexpected trailing snapshot data")
	}
	return i, nil
}

func decodeImageValue(d *json.Decoder) (image, error) {
	var i image
	if err := d.Decode(&i); err != nil {
		return i, fmt.Errorf("corrupt snapshot: %w", err)
	}
	if (i.Version != FormatVersion && !catalogFormat(i.Version)) || i.Monitors == nil {
		return i, fmt.Errorf("incompatible snapshot format %d", i.Version)
	}
	if err := validateImageExtensions(i); err != nil {
		return i, err
	}
	if err := validateCollectionHeaders(i); err != nil {
		return i, err
	}
	if err := i.SLO.Validate(); err != nil {
		return i, err
	}
	if err := validateAuthenticationImage(i); err != nil {
		return i, err
	}
	if err := validateCatalogImage(i); err != nil {
		return i, err
	}
	if err := validateBootstrapImage(i); err != nil {
		return i, err
	}
	if err := validateRecoveryReviewImage(i); err != nil {
		return i, err
	}
	if len(i.Operations)+len(i.OperationReservations) > maxPendingCatalogOperations || (len(i.Operations) > 0 && !catalogFormat(i.Version)) {
		return i, errors.New("invalid pending operation snapshot")
	}
	if err := validateOperationAllocationImage(i); err != nil {
		return i, err
	}
	for id, receipt := range i.Operations {
		if receipt.validate() != nil || id != receipt.ID || receipt.State != "committed" || receipt.CommittedIndex > i.Index ||
			!operationCurrent(i, receipt) {
			return i, errors.New("invalid pending operation receipt")
		}
	}
	for id, m := range i.Monitors {
		if id == "" || m.ID != id || m.Revision == "" || m.Policy.Interval <= 0 {
			return i, fmt.Errorf("invalid monitor in snapshot")
		}
		if err := validateMonitorControls(m); err != nil {
			return i, err
		}
		if err := m.Policy.Validate(); err != nil {
			return i, err
		}
		if m.InterventionAttempts < 0 || m.InterventionAttempts > math.MaxInt32 {
			return i, errors.New("invalid durable recovery attempt count")
		}
		if m.CatalogUID != "" && (!catalogFormat(i.Version) || !catalogIdentifier(m.CatalogUID, 256)) {
			return i, errors.New("invalid durable monitor incarnation")
		}
		if m.CatalogUID != "" {
			if _, exists := i.Catalog[(CatalogKey{Kind: "Monitor", ID: m.ID}).indexKey()]; !exists {
				return i, errors.New("managed monitor has no retained catalog identity")
			}
		}
		for _, action := range m.Actions {
			if action.LateEvidence != nil {
				if err := action.LateEvidence.validate(action); err != nil {
					return i, err
				}
			}
			if action.CatalogUID != "" && (!catalogFormat(i.Version) || !catalogIdentifier(action.CatalogUID, 256)) {
				return i, errors.New("invalid durable action incarnation")
			}
			if (action.State == Queued || action.State == Started) && action.CatalogUID != m.CatalogUID {
				return i, errors.New("active action belongs to a different monitor incarnation")
			}
		}
	}
	return i, nil
}

func (f *machine) Restore(reader io.ReadCloser) error {
	f.transition.Lock()
	defer f.transition.Unlock()
	defer reader.Close()
	i, ledger, err := decodeSnapshot(reader, f.collectionDirectory)
	if err != nil {
		return err
	}
	f.history.mu.RLock()
	index := f.history.catalog.Index
	f.history.mu.RUnlock()
	if index < i.Index {
		if ledger != nil {
			_ = ledger.Close()
		}
		return fmt.Errorf("history catalog is missing committed snapshot history; restore the complete backup")
	}
	recovery, err := recoverCollectionExecution(i, ledger)
	if err != nil {
		if ledger != nil {
			_ = ledger.Close()
		}
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.collections != nil {
		if err := f.collections.Close(); err != nil {
			if ledger != nil {
				_ = ledger.Close()
			}
			return err
		}
	}
	f.collections = ledger
	for _, prefix := range f.planPrefixes {
		prefix.close()
	}
	f.planPrefixes = nil
	f.validationPlanItems = nil
	f.collectionExecutionIndex, f.collectionExecutionLedger = nil, nil
	f.collectionPublicationIndex, f.collectionPublicationLedger = nil, nil
	f.collectionSourceCertificate = nil
	f.installCollectionExecutionRecovery(recovery)
	f.image = i
	f.rebuildOperationIndex()
	f.rebuildCatalogIndexes()
	f.rebuildControlIndexes()
	f.rebuildActionIndexes()
	f.controlChanges, f.controlSequence = nil, 0
	f.controlEpoch++
	// Restore happens before runtime subscribers are admitted. Discard previous
	// hints; authoritative resources come from the restored snapshot and replay.
	f.catalogChanges, f.catalogSequence = nil, 0
	f.catalogEpoch++
	return nil
}

type frozenSnapshot struct {
	image       image
	collections *collectionLedgerView
}

func (s *frozenSnapshot) Persist(sink raft.SnapshotSink) error {
	if collectionFormat(s.image.Version) {
		return s.persistCollections(sink)
	}
	if err := json.NewEncoder(sink).Encode(s.image); err != nil {
		_ = sink.Cancel()
		return err
	}
	if err := sink.Close(); err != nil {
		_ = sink.Cancel()
		return err
	}
	return nil
}
func (s *frozenSnapshot) Release() {
	if s.collections != nil {
		_ = s.collections.Close()
		s.collections = nil
	}
	s.image.Collections = nil
	s.image.Monitors = nil
	s.image.Catalog = nil
	s.image.Operations = nil
	s.image.Bootstrap = nil
}
