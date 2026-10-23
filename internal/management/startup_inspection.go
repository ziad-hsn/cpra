package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	bolt "go.etcd.io/bbolt"
)

var ErrStartupDiscard = errors.New("bootstrap discard refused: require the exact authenticated loading-stage identity, no admitted bootstrap or catalog, and exclusive stopped ownership")

// StartupStageReport exposes only authenticated inventory metadata. Discardable
// is an inspection result, never a promise that a later conditional discard wins.
type StartupStageReport struct {
	Stage        string `json:"stage"`
	Identity     string `json:"identity"`
	StoreID      string `json:"storeId"`
	Phase        string `json:"phase"`
	Resources    uint64 `json:"resources"`
	EncodedBytes uint64 `json:"encodedBytes"`
	Admitted     bool   `json:"admitted"`
	Discardable  bool   `json:"discardable"`
}

// StartupInspection holds the stage lock and native identity handles. The caller
// must retain exclusive Store ownership until Close. It creates no stage, starts
// no controller and grants no authority to alter Raft state.
type StartupInspection struct {
	stage  *Stage
	files  *startupFiles
	store  *persistence.Store
	report StartupStageReport
}

func InspectStartupStage(ctx context.Context, store *persistence.Store, sealer *secureconfig.Sealer, directory, selected string, options CatalogOptions) (_ *StartupInspection, err error) {
	if ctx == nil || store == nil || sealer == nil || store.Status().Mode != "raft" {
		return nil, ErrValidation
	}
	name := startupStageDirectory
	if selected == "temporary" {
		name = startupLoadingDirectory
	} else if selected != "published" {
		return nil, ErrValidation
	}
	files, err := openStartupFiles(directory, name)
	if err != nil {
		return nil, err
	}
	inspection := &StartupInspection{files: files, store: store}
	defer func() {
		if err != nil {
			_ = inspection.Close()
		}
	}()
	opts := StageOptions{Directory: filepath.Join(directory, name), StoreID: store.Status().NodeID, StageID: startupStageIdentity,
		MaxResources: 2_000_000, MaxEncodedBytes: 4 << 30, Validation: options, inspectOnly: true}
	if options.requiresCatalogValidation() {
		opts.Catalog, err = NewCatalog(store, sealer, options)
		if err != nil {
			return nil, err
		}
	}
	stage, err := ResumeStage(ctx, opts, sealer)
	if err != nil {
		return nil, err
	}
	inspection.stage = stage
	info, err := stage.Info()
	if err != nil {
		return nil, err
	}
	identity, err := inspection.identity()
	if err != nil {
		return nil, err
	}
	admitted, err := startupAdmitted(store)
	if err != nil {
		return nil, err
	}
	inspection.report = StartupStageReport{Stage: selected, Identity: identity, StoreID: info.StoreID, Phase: info.Phase,
		Resources: info.Count, EncodedBytes: info.EncodedBytes, Admitted: admitted, Discardable: !admitted && info.Phase == "loading"}
	return inspection, nil
}

func startupAdmitted(store *persistence.Store) (bool, error) {
	_, bootstrap := store.Bootstrap()
	catalog, err := store.HasCatalog()
	if err != nil {
		return true, ErrUnavailable
	}
	return bootstrap || catalog, nil
}

func (i *StartupInspection) Report() StartupStageReport { return i.report }

func (i *StartupInspection) identity() (string, error) {
	if err := i.files.check(false); err != nil {
		return "", err
	}
	i.stage.mu.Lock()
	defer i.stage.mu.Unlock()
	if err := i.stage.check(); err != nil {
		return "", err
	}
	h := sha256.New()
	// Ciphertext includes a random authenticated envelope even for an empty
	// loading stage; a recreated empty stage cannot reuse an old approval.
	err := i.stage.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(stageMetaBucket)
		if bucket == nil {
			return ErrStageUnavailable
		}
		raw := bucket.Get(stageMetaKey)
		if len(raw) == 0 || len(raw) > stageMetadataBudget {
			return ErrStageUnavailable
		}
		_, _ = h.Write(raw)
		return nil
	})
	if err != nil {
		return "", ErrStageUnavailable
	}
	identities, _ := json.Marshal([]startupIdentity{i.files.parentID, i.files.directoryID, i.files.fileID})
	_, _ = h.Write(identities)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Discard rechecks admission, inventory identity and native files after the
// caller records its durable audit intent. Frozen stages are never discardable.
func (i *StartupInspection) Discard(ctx context.Context, expected string) error {
	if ctx == nil || i == nil || i.stage == nil || expected == "" {
		return ErrStartupDiscard
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	admitted, err := startupAdmitted(i.store)
	if err != nil || admitted {
		return ErrStartupDiscard
	}
	info, err := i.stage.Info()
	if err != nil || info.Phase != "loading" {
		return ErrStartupDiscard
	}
	identity, err := i.identity()
	if err != nil || identity != expected || identity != i.report.Identity {
		return ErrStartupDiscard
	}
	if err := i.stage.Close(); err != nil {
		return ErrStageUnavailable
	}
	i.stage = nil
	return i.files.remove()
}

func (i *StartupInspection) Close() error {
	if i == nil {
		return nil
	}
	var err error
	if i.stage != nil {
		err = i.stage.Close()
		i.stage = nil
	}
	if i.files != nil {
		err = errors.Join(err, i.files.Close())
		i.files = nil
	}
	return err
}
