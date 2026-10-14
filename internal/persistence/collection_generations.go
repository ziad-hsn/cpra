package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const collectionGenerationCatalogName = "generations.json"
const maxCollectionGenerations = 512

type collectionGenerationCatalog struct {
	Version       int      `json:"version"`
	StoreID       string   `json:"storeID"`
	NodeID        string   `json:"nodeID"`
	Current       string   `json:"current"`
	Previous      string   `json:"previous"`
	Completed     []string `json:"completed"`
	Retired       uint64   `json:"retired"`
	UnlinkedBytes int64    `json:"unlinkedBytes"`
}

func (c collectionGenerationCatalog) validate(nodeID string) error {
	if c.Version != 1 || c.NodeID != nodeID || !collectionGCUUID(c.StoreID) || len(c.Completed) > maxCollectionGenerations || c.UnlinkedBytes < 0 {
		return errCollectionLedgerCorrupt
	}
	seen := make(map[string]bool, len(c.Completed))
	for _, name := range c.Completed {
		if !collectionGCName(name) || seen[name] {
			return errCollectionLedgerCorrupt
		}
		seen[name] = true
	}
	if c.Current != "" && !seen[c.Current] || c.Previous != "" && (!seen[c.Previous] || c.Previous == c.Current) {
		return errCollectionLedgerCorrupt
	}
	return nil
}

// CollectionStorageAccounting is a sampled, path-free observation. Unlinked
// bytes describe apparent file sizes; they do not promise physical reclamation.
type CollectionStorageAccounting struct {
	Available           bool      `json:"available"`
	ObservedAt          time.Time `json:"observed_at,omitzero"`
	Active              int       `json:"active"`
	Retained            int       `json:"retained"`
	Pinned              int       `json:"pinned"`
	Retired             uint64    `json:"retired"`
	UnlinkedBytes       int64     `json:"unlinked_bytes"`
	CleanupCondition    string    `json:"cleanup_condition,omitempty"`
	StepDurationMS      float64   `json:"step_duration_ms"`
	UsageCoverage       string    `json:"usage_coverage"`
	UsageGenerations    int       `json:"usage_generations"`
	UsageExpected       int       `json:"usage_expected"`
	UsageOldestAt       time.Time `json:"usage_oldest_at,omitzero"`
	KnownApparentBytes  int64     `json:"known_apparent_bytes"`
	KnownAllocatedBytes int64     `json:"known_allocated_bytes"`
	AllocationAvailable bool      `json:"allocation_available"`
}

type collectionGenerations struct {
	mu                         sync.Mutex // Local metadata/retirement only; never a controller/FSM mutex.
	directory                  string
	catalog                    collectionGenerationCatalog
	active                     atomic.Pointer[collectionLedger]
	live                       sync.Map // generation -> ledger; registration has no filesystem work.
	status                     atomic.Pointer[CollectionStorageAccounting]
	retirement                 *collectionRetirement
	scan                       *os.File
	pending                    string
	retirementPublicationError error
	closed                     bool
	usage                      map[string]collectionUsageSample
	usageCursor                int
	usageAttempted             bool
}

func newCollectionGenerations(directory, nodeID string) (*collectionGenerations, error) {
	owner := &collectionGenerations{directory: directory, catalog: collectionGenerationCatalog{Version: 1, StoreID: uuid.NewString(), NodeID: nodeID}}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		owner.publishStatus("", 0)
		return owner, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errCollectionLedgerCorrupt
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var saved collectionGenerationCatalog
	if err := readCollectionGenerationJSON(root, collectionGenerationCatalogName, &saved); err == nil {
		if err := saved.validate(nodeID); err != nil {
			return nil, err
		}
		owner.catalog = saved
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var selected collectionLedgerSelection
	if err := readCollectionGenerationJSON(root, "current.json", &selected); err == nil {
		if selected.Version != collectionLedgerFormat || !collectionGCName(selected.Generation) {
			return nil, errCollectionLedgerCorrupt
		}
		// A crash can occur after selector publication and before catalog
		// publication. Preserve that selected completed generation as the
		// next retention boundary; authoritative replay still rebuilds state.
		if err := owner.complete(selected.Generation); err != nil {
			return nil, err
		}
		owner.catalog.Current = selected.Generation
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	owner.publishStatus("", 0)
	return owner, nil
}

func readCollectionGenerationJSON(root *os.Root, name string, target any) error {
	before, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > 64<<10 {
		return errCollectionLedgerCorrupt
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return errCollectionLedgerCorrupt
	}
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return err
	}
	return decodeCollectionGenerationJSON(raw, target)
}

func decodeCollectionGenerationJSON(raw []byte, target any) error {
	if len(raw) > 64<<10 || api.StrictDecode(raw, target) != nil {
		return errCollectionLedgerCorrupt
	}
	var required []string
	switch target.(type) {
	case *collectionGenerationCatalog:
		required = []string{"version", "storeID", "nodeID", "current", "previous", "completed", "retired", "unlinkedBytes"}
	case *collectionLedgerSelection:
		required = []string{"version", "generation"}
	default:
		return errCollectionLedgerCorrupt
	}
	var fields map[string]json.RawMessage
	if api.StrictDecode(raw, &fields) != nil || len(fields) != len(required) {
		return errCollectionLedgerCorrupt
	}
	for _, field := range required {
		if len(fields[field]) == 0 || bytes.Equal(bytes.TrimSpace(fields[field]), []byte("null")) {
			return errCollectionLedgerCorrupt
		}
	}
	return nil
}

func (g *collectionGenerations) track(ledger *collectionLedger) {
	if g == nil || ledger == nil || ledger.db == nil {
		return
	}
	g.live.Store(ledger.generation, ledger)
	g.active.Store(ledger)
}

func (g *collectionGenerations) complete(name string) error {
	if slices.Contains(g.catalog.Completed, name) {
		return nil
	}
	if len(g.catalog.Completed) >= maxCollectionGenerations {
		return errCollectionLedgerQuota
	}
	g.catalog.Completed = append(g.catalog.Completed, name)
	return nil
}

func (g *collectionGenerations) publish(ledger *collectionLedger) error {
	if g == nil || ledger == nil || ledger.db == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.publishLocked(ledger)
}

func (g *collectionGenerations) publishLocked(ledger *collectionLedger) error {
	if g.closed {
		return errCollectionLedgerClosed
	}
	if g.catalog.Current == ledger.generation {
		return nil
	}
	before := g.catalog
	if err := g.complete(ledger.generation); err != nil {
		return err
	}
	if err := ledger.Publish(); err != nil {
		g.catalog = before
		return err
	}
	previous := g.catalog.Current
	g.catalog.Current, g.catalog.Previous = ledger.generation, previous
	if err := g.save(); err != nil {
		g.catalog = before
		return err
	}
	g.publishStatus("", 0)
	return nil
}

func (g *collectionGenerations) save() error {
	return atomicJSON(filepath.Join(g.directory, collectionGenerationCatalogName), g.catalog)
}

func (g *collectionGenerations) protection() collectionGCProtection {
	p := collectionGCProtection{StoreID: g.catalog.StoreID, NodeID: g.catalog.NodeID, Current: g.catalog.Current, Previous: g.catalog.Previous}
	g.live.Range(func(name, value any) bool {
		ledger := value.(*collectionLedger)
		if !ledger.closedNative.Load() || ledger.viewPins.Load() > 0 {
			p.Pinned = append(p.Pinned, name.(string))
		} else {
			g.live.Delete(name)
		}
		return true
	})
	slices.Sort(p.Pinned)
	p.RecordRetired = func(name string) error {
		if !slices.Contains(g.catalog.Completed, name) {
			return nil
		}
		before := g.catalog
		before.Completed = slices.Clone(g.catalog.Completed)
		g.catalog.Completed = slices.DeleteFunc(g.catalog.Completed, func(candidate string) bool { return candidate == name })
		g.catalog.Retired++
		if err := g.save(); err != nil {
			g.catalog = before
			g.retirementPublicationError = err
			return err
		}
		return nil
	}
	return p
}

func (g *collectionGenerations) publishStatus(condition string, duration time.Duration) {
	status := CollectionStorageAccounting{Available: true, ObservedAt: time.Now().UTC(), Retired: g.catalog.Retired, UnlinkedBytes: g.catalog.UnlinkedBytes, CleanupCondition: condition, StepDurationMS: float64(duration) / float64(time.Millisecond)}
	if active := g.active.Load(); active != nil && !active.closedNative.Load() {
		status.Active = 1
	}
	status.Retained = max(0, len(g.catalog.Completed)-status.Active)
	g.live.Range(func(_, value any) bool {
		if value.(*collectionLedger).viewPins.Load() > 0 {
			status.Pinned++
		}
		return true
	})
	g.publishUsage(&status)
	g.status.Store(&status)
}

func (g *collectionGenerations) accounting() CollectionStorageAccounting {
	if g == nil {
		return CollectionStorageAccounting{Available: true}
	}
	if status := g.status.Load(); status != nil {
		return *status
	}
	return CollectionStorageAccounting{}
}

// maintain performs one cooperative scan/retirement step. Native calls can
// exceed the budget; their elapsed duration is observed, never hidden.
func (g *collectionGenerations) maintain(ctx context.Context) (err error) {
	if g == nil {
		return nil
	}
	started := time.Now()
	if !g.mu.TryLock() {
		return nil
	}
	defer g.mu.Unlock()
	condition := ""
	g.retirementPublicationError = nil
	defer func() {
		// One generation at most, outside controller/FSM ownership. File calls
		// are cooperative; their time is included in the observed step duration.
		if ctx.Err() == nil && time.Since(started) < 25*time.Millisecond {
			g.sampleUsage(ctx)
		}
		if errors.Is(err, errCollectionGCUnavailable) {
			condition, err = "unavailable", nil
		}
		if errors.Is(err, errCollectionGCBlocked) {
			condition, err = "blocked", nil
		}
		if errors.Is(err, errCollectionGCBudget) || errors.Is(err, context.DeadlineExceeded) {
			condition, err = "budget_exhausted", nil
		}
		if time.Since(started) > 25*time.Millisecond && condition == "" {
			condition = "budget_exceeded"
		}
		g.publishStatus(condition, time.Since(started))
	}()
	if g.closed {
		return errCollectionLedgerClosed
	}
	if active := g.active.Load(); active != nil && !active.closedNative.Load() {
		if err := g.publishLocked(active); err != nil {
			return err
		}
	}
	if g.catalog.Current == "" {
		return nil
	}
	if g.retirement == nil {
		if err := g.openRetirement(); err != nil {
			return err
		}
	}
	if g.catalog.Previous != "" {
		// An admitted retirement has its own complete eligibility proof. Finish
		// one transition before scanning for more work, so directory discovery
		// cannot consume its budget or block it on an unrelated incomplete file.
		step, err := g.retirement.Step(ctx, g.pending)
		if g.retirementPublicationError != nil {
			return g.retirementPublicationError
		}
		if err != nil {
			return err
		}
		if step.Phase != "idle" {
			g.pending = step.Generation
			g.catalog.UnlinkedBytes += step.UnlinkedBytes
			if step.Phase == "complete" {
				g.pending = ""
			}
			return g.save()
		}
	}
	if g.scan == nil {
		g.scan, err = g.retirement.root.Open(".")
		if err != nil {
			return err
		}
	}
	names, scanErr := g.scan.Readdirnames(16)
	if scanErr != nil && !errors.Is(scanErr, io.EOF) {
		return scanErr
	}
	if errors.Is(scanErr, io.EOF) {
		_ = g.scan.Close()
		g.scan = nil
	}
	completedBefore := len(g.catalog.Completed)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !collectionGCName(name) {
			continue
		}
		// Only completed, strictly parsed markers enter the local catalog.
		info, err := g.retirement.root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errCollectionGCBlocked
		}
		root, err := g.retirement.root.OpenRoot(name)
		if err != nil {
			return errCollectionGCBlocked
		}
		opened, statErr := root.Stat(".")
		if statErr != nil || !os.SameFile(info, opened) {
			_ = root.Close()
			return errCollectionGCBlocked
		}
		var marker collectionLedgerSelection
		err = readCollectionGenerationJSON(root, "generation.json", &marker)
		_ = root.Close()
		if err != nil || marker.Version != collectionLedgerFormat || marker.Generation != name {
			// A partially retired generation can already lack its marker. The
			// persisted retirement intent, not a scan, validates that state.
			if slices.Contains(g.catalog.Completed, name) {
				continue
			}
			return errCollectionGCBlocked
		}
		if err := g.complete(name); err != nil {
			return err
		}
	}
	if len(g.catalog.Completed) != completedBefore {
		if err := g.save(); err != nil {
			return err
		}
	}
	if g.catalog.Previous == "" {
		return nil
	}
	// Admission runs on the next maintenance turn with a fresh budget and the
	// same authoritative native/protection checks. Selection alone deletes nothing.
	protected := g.protection()
	for _, name := range g.catalog.Completed {
		if name != protected.Current && name != protected.Previous && !slices.Contains(protected.Pinned, name) {
			g.pending = name
			break
		}
	}
	return nil
}

// openRetirement requires g.mu and Store ownership. The same native boundary
// is used for read-only file sampling and destructive retirement eligibility.
func (g *collectionGenerations) openRetirement() error {
	if g.retirement != nil {
		return nil
	}
	var err error
	g.retirement, err = newCollectionRetirement(g.directory, func(ctx context.Context, use func(collectionGCProtection) error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return use(g.protection())
	})
	return err
}

func (g *collectionGenerations) close() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	var err error
	if g.scan != nil {
		err = g.scan.Close()
		g.scan = nil
	}
	return errors.Join(err, g.retirement.Close())
}
