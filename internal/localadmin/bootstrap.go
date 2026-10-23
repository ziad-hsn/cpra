package localadmin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/encryptionsetup"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

type BootstrapRequest struct {
	DataDirectory  string
	RuntimeConfig  string
	Stage          string
	Identity       string
	Actor          string
	Reason         string
	AuditDirectory string
}

type BootstrapDiscardReport struct {
	Stage          management.StartupStageReport `json:"stage"`
	AttemptID      string                        `json:"attemptId"`
	Outcome        string                        `json:"outcome"`
	AuditDirectory string                        `json:"auditDirectory"`
}

type bootstrapAudit struct {
	Version   int                           `json:"version"`
	AttemptID string                        `json:"attemptId"`
	Phase     string                        `json:"phase"`
	At        time.Time                     `json:"at"`
	Actor     string                        `json:"actor"`
	Reason    string                        `json:"reason"`
	Stage     management.StartupStageReport `json:"stage"`
}

func openBootstrap(ctx context.Context, request BootstrapRequest) (_ *management.StartupInspection, closeAll func() error, err error) {
	if request.RuntimeConfig == "" {
		return nil, nil, errors.New("bootstrap inspection requires --runtime-config with the original encryption configuration")
	}
	cfg, err := runtimeconfig.Load(request.RuntimeConfig)
	if err != nil {
		return nil, nil, &authenticationError{"cannot read bootstrap runtime configuration", err}
	}
	store, err := openActionStore(ctx, request.DataDirectory)
	if err != nil {
		return nil, nil, err
	}
	var keys *encryptionsetup.Handle
	var inspection *management.StartupInspection
	cleanup := func() error { return errors.Join(inspection.Close(), keys.Close(), store.Close()) }
	closeAll = cleanup
	defer func() {
		if err != nil {
			_ = cleanup()
		}
	}()
	keys, err = encryptionsetup.Open(ctx, encryptionsetup.Options{StorageMode: "raft", DataDirectory: request.DataDirectory, Encryption: cfg.EncryptionSource()})
	if err != nil {
		return nil, nil, &authenticationError{"cannot authenticate bootstrap staging with the configured encryption key", err}
	}
	options := management.CatalogOptions{NotificationLogEnabled: cfg.Notifications.LogDirectory != ""}
	bootstrapCatalogOptions(&options, cfg)
	inspection, err = management.InspectStartupStage(ctx, store, keys.Sealer(), request.DataDirectory, request.Stage, options)
	if err != nil {
		return nil, nil, &authenticationError{"bootstrap stage is missing, locked, unsafe, or failed authentication; preserve it and the Raft directory", err}
	}
	return inspection, closeAll, nil
}

// InspectBootstrap requires stopped exclusive ownership and reports a bounded,
// authenticated inventory. It never activates or repairs configuration.
func InspectBootstrap(ctx context.Context, request BootstrapRequest) (report management.StartupStageReport, err error) {
	inspection, closeAll, err := openBootstrap(ctx, request)
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, closeAll()) }()
	return inspection.Report(), nil
}

func printableAudit(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, c := range value {
		if !unicode.IsPrint(c) {
			return false
		}
	}
	return true
}

// DiscardBootstrap writes an immutable protected intent before conditional
// removal, then a separate completion record. An absent completion is never a
// successful discard assertion. Audit files live outside the state directory.
func DiscardBootstrap(ctx context.Context, request BootstrapRequest) (report BootstrapDiscardReport, err error) {
	identity, parseErr := hex.DecodeString(request.Identity)
	if parseErr != nil || len(identity) != 32 || !printableAudit(request.Actor, 128) || !printableAudit(request.Reason, 512) {
		return report, errors.New("bootstrap discard requires its exact 64-character identity, a printable actor (1–128 bytes), and reason (1–512 bytes)")
	}
	inspection, closeAll, err := openBootstrap(ctx, request)
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, closeAll()) }()
	report.Stage = inspection.Report()
	if !report.Stage.Discardable || report.Stage.Identity != request.Identity {
		return report, management.ErrStartupDiscard
	}
	directory := request.AuditDirectory
	if directory == "" {
		directory = request.DataDirectory + "-bootstrap-audit"
	}
	if !filepath.IsAbs(directory) || directory != filepath.Clean(directory) {
		return report, errors.New("bootstrap audit directory must be an absolute clean path outside state")
	}
	// WriteNewProtectedFile enforces native ownership, aliases and outside-state
	// placement. No installation privilege or key-policy relaxation is introduced.
	report.AttemptID, report.Outcome, report.AuditDirectory = uuid.NewString(), "not_started", directory
	audit := bootstrapAudit{Version: 1, AttemptID: report.AttemptID, Phase: "intent", At: time.Now().UTC(), Actor: request.Actor, Reason: request.Reason, Stage: report.Stage}
	write := func() error {
		raw, encodeErr := json.Marshal(audit)
		if encodeErr != nil {
			return encodeErr
		}
		defer clear(raw)
		path := filepath.Join(directory, report.AttemptID+"-"+audit.Phase+".json")
		return secureconfig.WriteNewProtectedFile(ctx, secureconfig.ProtectedFileOptions{Path: path, DataDirectory: request.DataDirectory, MaxBytes: 16 << 10}, raw)
	}
	if err := write(); err != nil {
		return report, &authenticationError{"bootstrap discard did not start: protected audit intent could not be committed", err}
	}
	report.Outcome = "unconfirmed"
	if err := inspection.Discard(ctx, request.Identity); err != nil {
		return report, &authenticationError{"bootstrap discard was not confirmed; inspect the original stage and audit intent before further action", err}
	}
	audit.Phase, audit.At = "completed", time.Now().UTC()
	if err := write(); err != nil {
		return report, &authenticationError{"stage removed but audit completion was not confirmed; preserve the audit intent", err}
	}
	report.Outcome = "completed"
	return report, nil
}
