package management

import (
	"context"
	"errors"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

var ErrBootstrapOutcomeUnconfirmed = errors.New("bootstrap outcome is unconfirmed; resume the original encrypted stage")

// BootstrapProgress reports the last observed durable import position. Seeded
// records remain unavailable until CatalogActivated. Activation establishes the
// desired catalog only; it does not establish controller application or readiness.
// OutcomeUnconfirmed requires reconciliation using this exact original stage.
type BootstrapProgress struct {
	Manifest           persistence.BootstrapManifest
	Phase              string
	SeededRecords      uint64
	CatalogActivated   bool
	OutcomeUnconfirmed bool
}

type bootstrapStore interface {
	Submit(context.Context, []persistence.Command) ([]persistence.Result, error)
	Bootstrap() (persistence.BootstrapState, bool)
	Status() persistence.Status
	CommandLimits() (int, int)
}

// ApplyBootstrap activates only a previously frozen, fully validated stage. It
// never reads source files or normalizes resources. It receives no provider
// plaintext and creates no new identities or ciphertext. An interrupted
// invocation returns progress and may be called again explicitly with the same
// stage. It does not retry an
// uncertain submission or replace an existing import with changed inputs.
func ApplyBootstrap(ctx context.Context, store *persistence.Store, stage *Stage) (BootstrapProgress, error) {
	if store == nil {
		return BootstrapProgress{}, ErrValidation
	}
	return applyBootstrap(ctx, store, stage)
}

func applyBootstrap(ctx context.Context, store bootstrapStore, stage *Stage) (BootstrapProgress, error) {
	if ctx == nil || stage == nil {
		return BootstrapProgress{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return BootstrapProgress{}, err
	}
	info, err := stage.Info()
	if err != nil {
		return BootstrapProgress{}, err
	}
	if info.Phase != "frozen" {
		return BootstrapProgress{}, ErrStageNotFrozen
	}
	if info.StoreID != store.Status().NodeID {
		return BootstrapProgress{}, persistence.ErrBootstrapConflict
	}
	// Reauthenticate the complete record set and semantic graph before any Raft
	// change. Freeze is read-only for an already frozen stage.
	verified, err := stage.Freeze(ctx)
	if err != nil {
		return BootstrapProgress{}, err
	}
	if info != verified {
		return BootstrapProgress{}, persistence.ErrBootstrapConflict
	}
	manifest := persistence.BootstrapManifest{StageID: info.StageID, InventoryDigest: info.Digest, CatalogDigest: info.CatalogDigest, Count: info.Count}
	initial, exists := store.Bootstrap()
	progress := observeBootstrap(store, manifest)
	if exists && initial.Manifest != manifest {
		return progress, persistence.ErrBootstrapConflict
	}
	if exists && initial.Phase == "active" {
		return progress, nil
	}
	if exists && initial.Phase != "seeding" {
		return progress, persistence.ErrBootstrapConflict
	}
	maxCommands, maxBytes := store.CommandLimits()
	maxCommands = min(maxCommands, 1000)
	if maxCommands < 1 || maxBytes <= 64 {
		return progress, ErrValidation
	}
	// Validate every seed's admission bound before begin; an oversized final
	// resource cannot leave an otherwise empty catalog stuck in migration.
	if err := preflightBootstrap(ctx, stage, manifest, initial, exists, maxBytes); err != nil {
		return observeBootstrap(store, manifest), err
	}
	at := time.Now().UTC()
	if exists && initial.StartedAt.After(at) {
		at = initial.StartedAt
	}
	if !exists {
		command := persistence.Command{Kind: "bootstrap", At: at, Bootstrap: &persistence.BootstrapCommand{Action: "begin", StageID: manifest.StageID, Manifest: &manifest}}
		if err := submitBootstrap(ctx, store, []persistence.Command{command}); err != nil {
			return bootstrapFailure(store, manifest, err)
		}
	}
	current, ok := store.Bootstrap()
	if !ok || current.Manifest != manifest {
		return observeBootstrap(store, manifest), persistence.ErrBootstrapConflict
	}
	if current.Phase == "active" {
		return observeBootstrap(store, manifest), nil
	}
	expectedApplied := uint64(0)
	expectedDigest := persistence.BootstrapInitialDigest()
	expectedLastKey := persistence.CatalogKey{}
	if exists {
		expectedApplied, expectedDigest = initial.Applied, initial.ProgressDigest
		expectedLastKey = initial.LastKey
	}
	if current.Applied != expectedApplied || current.ProgressDigest != expectedDigest || current.LastKey != expectedLastKey {
		return observeBootstrap(store, manifest), persistence.ErrBootstrapConflict
	}
	if current.StartedAt.After(at) {
		at = current.StartedAt
	}
	iterator := stageIterator{stage: stage}
	ordinal := uint64(0)
	batch := make([]persistence.Command, 0, min(maxCommands, 100))
	batchBytes := 64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := submitBootstrap(ctx, store, batch); err != nil {
			return err
		}
		clear(batch)
		batch = batch[:0]
		batchBytes = 64
		return nil
	}
	for {
		record, ok, err := iterator.next(ctx)
		if err != nil {
			return observeBootstrap(store, manifest), err
		}
		if !ok {
			break
		}
		ordinal++
		if record.UpdatedAt.After(at) {
			at = record.UpdatedAt
		}
		if ordinal <= expectedApplied {
			continue
		}
		command := persistence.Command{Kind: "bootstrap", At: at, Bootstrap: &persistence.BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: ordinal, Record: &record}}
		bound, err := persistence.CommandEncodedBound(command)
		if err != nil || bound+65 > maxBytes {
			return observeBootstrap(store, manifest), ErrStageQuota
		}
		if len(batch) == maxCommands || batchBytes+bound+1 > maxBytes {
			if err := flush(); err != nil {
				return bootstrapFailure(store, manifest, err)
			}
		}
		batch = append(batch, command)
		batchBytes += bound + 1
	}
	if ordinal != manifest.Count {
		return observeBootstrap(store, manifest), persistence.ErrBootstrapConflict
	}
	if err := flush(); err != nil {
		return bootstrapFailure(store, manifest, err)
	}
	current, ok = store.Bootstrap()
	if !ok || current.Manifest != manifest || current.Applied != manifest.Count || current.ProgressDigest != manifest.CatalogDigest {
		return observeBootstrap(store, manifest), persistence.ErrBootstrapConflict
	}
	if current.Phase == "active" {
		return observeBootstrap(store, manifest), nil
	}
	if now := time.Now().UTC(); now.After(at) {
		at = now
	}
	command := persistence.Command{Kind: "bootstrap", At: at, Bootstrap: &persistence.BootstrapCommand{Action: "activate", StageID: manifest.StageID, Manifest: &manifest}}
	if err := submitBootstrap(ctx, store, []persistence.Command{command}); err != nil {
		return bootstrapFailure(store, manifest, err)
	}
	progress = observeBootstrap(store, manifest)
	if !progress.CatalogActivated {
		return bootstrapFailure(store, manifest, ErrBootstrapOutcomeUnconfirmed)
	}
	return progress, nil
}

func observeBootstrap(store bootstrapStore, manifest persistence.BootstrapManifest) BootstrapProgress {
	if state, ok := store.Bootstrap(); ok {
		return BootstrapProgress{Manifest: state.Manifest, Phase: state.Phase, SeededRecords: state.Applied, CatalogActivated: state.Phase == "active"}
	}
	return BootstrapProgress{Manifest: manifest, Phase: "validated"}
}
func bootstrapFailure(store bootstrapStore, manifest persistence.BootstrapManifest, err error) (BootstrapProgress, error) {
	progress := observeBootstrap(store, manifest)
	if errors.Is(err, persistence.ErrCommitUnconfirmed) || errors.Is(err, ErrBootstrapOutcomeUnconfirmed) {
		progress.OutcomeUnconfirmed = true
		return progress, errors.Join(ErrBootstrapOutcomeUnconfirmed, err)
	}
	return progress, err
}
func submitBootstrap(ctx context.Context, store bootstrapStore, commands []persistence.Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	results, err := store.Submit(ctx, commands)
	if err != nil {
		return err
	}
	if len(results) != len(commands) {
		return ErrBootstrapOutcomeUnconfirmed
	}
	for _, result := range results {
		if result.Err != nil {
			return result.Err
		}
		if !result.Allowed {
			return ErrBootstrapOutcomeUnconfirmed
		}
	}
	return nil
}

func preflightBootstrap(ctx context.Context, stage *Stage, manifest persistence.BootstrapManifest, current persistence.BootstrapState, exists bool, maxBytes int) error {
	if exists && (current.Applied > manifest.Count || current.Manifest != manifest) {
		return persistence.ErrBootstrapConflict
	}
	iterator := stageIterator{stage: stage}
	digest := persistence.BootstrapInitialDigest()
	count := uint64(0)
	last := persistence.CatalogKey{}
	if exists && current.Applied == 0 && (current.ProgressDigest != digest || current.LastKey != last) {
		return persistence.ErrBootstrapConflict
	}
	for {
		record, ok, err := iterator.next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		count++
		if count > manifest.Count {
			return persistence.ErrBootstrapConflict
		}
		digest, err = persistence.BootstrapDigest(digest, record)
		if err != nil {
			return persistence.ErrBootstrapConflict
		}
		last = record.Key
		if exists && count == current.Applied && (digest != current.ProgressDigest || last != current.LastKey) {
			return persistence.ErrBootstrapConflict
		}
		command := persistence.Command{Kind: "bootstrap", At: record.UpdatedAt, Bootstrap: &persistence.BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: count, Record: &record}}
		bound, err := persistence.CommandEncodedBound(command)
		if err != nil || bound+65 > maxBytes {
			return ErrStageQuota
		}
	}
	if count != manifest.Count || digest != manifest.CatalogDigest {
		return persistence.ErrBootstrapConflict
	}
	return nil
}

// stageIterator retains at most one Stage.Page, which already has record-count
// and encoded-byte limits. Cursors carry the frozen stage and content identity.
type stageIterator struct {
	stage    *Stage
	page     []persistence.CatalogRecord
	position int
	cursor   string
	finished bool
}

func (i *stageIterator) next(ctx context.Context) (persistence.CatalogRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return persistence.CatalogRecord{}, false, err
	}
	for i.position == len(i.page) {
		if i.finished {
			return persistence.CatalogRecord{}, false, nil
		}
		page, next, err := i.stage.Page(ctx, i.cursor, 100)
		if err != nil {
			return persistence.CatalogRecord{}, false, err
		}
		i.page, i.position, i.cursor, i.finished = page, 0, next, next == ""
	}
	record := i.page[i.position]
	i.page[i.position] = persistence.CatalogRecord{}
	i.position++
	return record, true, nil
}
