package management

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

const (
	startupStageDirectory   = "management-bootstrap"
	startupStageIdentity    = "startup-v1"
	startupLoadingDirectory = ".management-bootstrap-loading"
)

var (
	ErrStartupInput      = errors.New("startup input could not be read or validated; inspect the selected configuration without logging protected values")
	ErrStartupEmpty      = errors.New("startup input has no monitors; explicitly allow an empty initial configuration")
	ErrStartupIncomplete = errors.New("startup input loading is incomplete; stop CPRa and inspect it with cpractl local bootstrap inspect; discard requires the exact authenticated loading-stage identity and proof that nothing was admitted; never remove Raft state")
	ErrStartupStage      = errors.New("the original encrypted startup stage is unavailable; restore management-bootstrap from the matching stopped backup; do not replace it with changed input")
	ErrStartupDriver     = errors.New("startup input selects a driver not compiled into this server; inspect capabilities and select the required build")
	ErrStartupCleanup    = errors.New("temporary encrypted startup staging could not be closed or removed")
	ErrStartupMemory     = errors.New("memory-mode startup was interrupted; stop CPRa and restart its disposable store before reloading corrected input")
)

// StartupOptions bounds initial configuration loading. OpenSource is lazy: it is
// never called when a catalog is authoritative or an original frozen import can
// resume. The callback transfers ownership of its reader to this operation. A nil
// callback is an intentionally empty input and requires AllowEmpty on first use.
//
// DataDirectory must name the caller's opened Raft directory. Store/sealer remain
// caller-owned. Memory mode uses only an encrypted private temporary stage and
// removes it before returning; any failed memory startup requires discarding that
// disposable Store. It never creates a plaintext spool or persists a wrapping key.
type StartupOptions struct {
	Catalog         CatalogOptions
	DataDirectory   string
	OpenSource      func(context.Context) (io.ReadCloser, error)
	Decode          collection.DecodeOptions
	MaxResources    uint64
	MaxEncodedBytes uint64
	AllowEmpty      bool
}

type StartupResult struct {
	Catalog   *Catalog
	Bootstrap BootstrapProgress
	// ReusedCatalog means startup input was deliberately not opened. The
	// committed catalog, including an empty one, remains authoritative.
	ReusedCatalog bool
}

// StartupValidation counts fully validated resources, including extracted private
// credentials. It describes input validity and compiled driver support, never
// provider connectivity or controller readiness.
type StartupValidation struct {
	Resources                    uint64
	Monitors                     uint64
	UnverifiedContractReferences uint64
	Warnings                     []api.FieldError
	WarningsTruncated            bool
}

// StartupCatalog restores an authoritative catalog or completes exactly one
// staged initial import. Existing API edits, including an all-deleted catalog,
// win over startup source flags. A successful result has authenticated catalog
// contents; the owner must still reconcile its runtime projection before serving
// application readiness. No check, recovery or notification is executed here.
func StartupCatalog(ctx context.Context, store *persistence.Store, sealer *secureconfig.Sealer, options StartupOptions) (result StartupResult, err error) {
	if ctx == nil || store == nil || sealer == nil {
		return result, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	state, hasBootstrap := store.Bootstrap()
	if hasBootstrap {
		result.Bootstrap = observeBootstrap(store, state.Manifest)
	}
	hasCatalog, err := store.HasCatalog()
	if err != nil {
		return result, ErrUnavailable
	}
	if (hasBootstrap && state.Phase == "active") || (!hasBootstrap && hasCatalog) {
		result.ReusedCatalog = true
		if hasBootstrap {
			result.Bootstrap = observeBootstrap(store, state.Manifest)
		} else {
			result.Bootstrap.Phase = "active"
			result.Bootstrap.CatalogActivated = true
		}
		return verifyStartupCatalog(ctx, store, sealer, result, options.Catalog)
	}
	if hasBootstrap && (state.Phase != "seeding" || state.Manifest.StageID != startupStageIdentity) {
		return result, errors.Join(ErrStartupStage, persistence.ErrBootstrapConflict)
	}
	options, err = normalizeStartupOptions(options)
	if err != nil {
		return result, err
	}
	mode := store.Status().Mode
	var directory string
	var created, temporary bool
	switch mode {
	case "raft":
		if !filepath.IsAbs(options.DataDirectory) {
			return result, ErrValidation
		}
		directory = filepath.Join(filepath.Clean(options.DataDirectory), startupStageDirectory)
		if _, statErr := os.Lstat(directory); statErr == nil {
			if err := checkStartupDirectory(directory); err != nil {
				if hasBootstrap {
					return result, errors.Join(ErrStartupStage, err)
				}
				return result, errors.Join(ErrStartupIncomplete, err)
			}
			if _, loadingErr := os.Lstat(filepath.Join(options.DataDirectory, startupLoadingDirectory)); !errors.Is(loadingErr, os.ErrNotExist) {
				return result, ErrStartupIncomplete
			}
		} else if !errors.Is(statErr, os.ErrNotExist) || hasBootstrap {
			return result, ErrStartupStage
		} else {
			temporary = true
			directory = filepath.Join(filepath.Clean(options.DataDirectory), startupLoadingDirectory)
			created, err = createStartupDirectory(directory)
			if err != nil {
				return result, errors.Join(ErrStartupIncomplete, err)
			}
		}
	case "memory":
		if hasBootstrap {
			return result, errors.Join(ErrStartupMemory, persistence.ErrBootstrapPending)
		}
		directory, err = temporaryStartupDirectory()
		if err != nil {
			return result, err
		}
		created = true
		defer func() {
			if removeErr := os.RemoveAll(directory); removeErr != nil {
				err = errors.Join(err, ErrStartupCleanup)
			}
		}()
	default:
		return result, ErrValidation
	}
	stageOptions := startupStageOptions(options, directory, store.Status().NodeID)
	if options.Catalog.requiresCatalogValidation() {
		stageOptions.Catalog, err = NewCatalog(store, sealer, options.Catalog)
		if err != nil {
			return result, err
		}
	}
	var stage *Stage
	if created {
		stage, err = CreateStage(ctx, stageOptions, sealer)
	} else {
		stage, err = ResumeStage(ctx, stageOptions, sealer)
	}
	if err != nil {
		if mode == "memory" {
			return result, errors.Join(ErrStartupMemory, safeStartupError(ctx, err))
		}
		if hasBootstrap {
			return result, errors.Join(ErrStartupStage, safeStartupError(ctx, err))
		}
		return result, errors.Join(ErrStartupIncomplete, safeStartupError(ctx, err))
	}
	defer func() {
		if stage != nil {
			if closeErr := stage.Close(); closeErr != nil {
				err = errors.Join(err, ErrStageUnavailable)
			}
		}
	}()
	var files *startupFiles
	if temporary {
		files, err = openStartupFiles(options.DataDirectory, startupLoadingDirectory)
		if err != nil {
			return result, errors.Join(ErrStartupIncomplete, err)
		}
		defer files.Close()
	}
	if created {
		result.Bootstrap.Phase = "loading"
		if _, inputErr := fillStartupStage(ctx, stage, options); inputErr != nil {
			if mode == "memory" {
				return result, errors.Join(ErrStartupMemory, inputErr)
			}
			// Only an authenticated, still-loading stage created by this invocation is
			// disposable. Failed writes, uncertain state and failed cleanup are retained.
			info, inspectErr := stage.Info()
			if inspectErr != nil || info.Phase != "loading" || errors.Is(inputErr, ErrStageUnavailable) {
				return result, errors.Join(ErrStartupIncomplete, inputErr)
			}
			if closeErr := stage.Close(); closeErr != nil {
				return result, errors.Join(ErrStartupCleanup, inputErr)
			}
			stage = nil
			if removeErr := files.remove(); removeErr != nil {
				return result, errors.Join(ErrStartupCleanup, inputErr)
			}
			return result, inputErr
		}
	} else {
		info, infoErr := stage.Info()
		if infoErr != nil {
			return result, errors.Join(ErrStartupStage, safeStartupError(ctx, infoErr))
		}
		if info.Phase != "frozen" {
			if hasBootstrap {
				return result, errors.Join(ErrStartupStage, ErrStageNotFrozen)
			}
			return result, ErrStartupIncomplete
		}
	}
	if temporary {
		// The source is never reopened. A crash after Freeze resumes this exact
		// authenticated inventory; publication never replaces an existing stage.
		if err := stage.Close(); err != nil {
			return result, ErrStartupStage
		}
		stage = nil
		if err := files.publish(); err != nil {
			return result, errors.Join(ErrStartupStage, err)
		}
		stageOptions.Directory = filepath.Join(options.DataDirectory, startupStageDirectory)
		stage, err = ResumeStage(ctx, stageOptions, sealer)
		if err != nil {
			return result, errors.Join(ErrStartupStage, safeStartupError(ctx, err))
		}
	}
	result.Bootstrap, err = ApplyBootstrap(ctx, store, stage)
	if err != nil {
		if mode == "memory" {
			return result, errors.Join(ErrStartupMemory, err)
		}
		return result, err
	}
	return verifyStartupCatalog(ctx, store, sealer, result, options.Catalog)
}

func verifyStartupCatalog(ctx context.Context, store *persistence.Store, sealer *secureconfig.Sealer, result StartupResult, options CatalogOptions) (StartupResult, error) {
	catalog, err := NewCatalog(store, sealer, options)
	if err != nil {
		return result, err
	}
	if err := catalog.Verify(ctx); err != nil {
		return result, err
	}
	result.Catalog = catalog
	return result, nil
}

// ValidateStartupSource shares startup parsing, credential extraction, semantic
// graph validation and build capability checks, using an ephemeral local key.
// It does not open durable storage, load a wrapping backend or activate records.
func ValidateStartupSource(ctx context.Context, options StartupOptions) (result StartupValidation, err error) {
	if ctx == nil {
		return result, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	options, err = normalizeStartupOptions(options)
	if err != nil {
		return result, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return result, ErrUnavailable
	}
	defer clear(key)
	wrapper, err := secureconfig.NewLocalWrapper(key)
	if err != nil {
		return result, ErrUnavailable
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		return result, ErrUnavailable
	}
	directory, err := temporaryStartupDirectory()
	if err != nil {
		return result, err
	}
	defer func() {
		if removeErr := os.RemoveAll(directory); removeErr != nil {
			err = errors.Join(err, ErrStartupCleanup)
		}
	}()
	stage, err := CreateStage(ctx, startupStageOptions(options, directory, uuid.NewString()), sealer)
	if err != nil {
		return result, safeStartupError(ctx, err)
	}
	defer func() {
		if closeErr := stage.Close(); closeErr != nil {
			err = errors.Join(err, ErrStartupCleanup)
		}
	}()
	return fillStartupStage(ctx, stage, options)
}

func normalizeStartupOptions(options StartupOptions) (StartupOptions, error) {
	if options.MaxResources == 0 {
		options.MaxResources = 2_000_000
	}
	if options.MaxEncodedBytes == 0 {
		options.MaxEncodedBytes = 4 << 30
	}
	if options.MaxResources > 10_000_000 || options.MaxEncodedBytes < stageMetadataBudget || options.MaxEncodedBytes > 1<<40 {
		return options, ErrValidation
	}
	if options.Decode.MaxResources < 0 || options.Decode.MaxBytes < 0 || options.Decode.MaxBytes == math.MaxInt64 || options.Decode.MaxResourceBytes < 0 || options.Decode.MaxResourceBytes > 1<<20 || options.Decode.MaxDocumentBytes < 0 {
		return options, ErrValidation
	}
	if options.Decode.MaxBytes == 0 {
		options.Decode.MaxBytes = 1 << 30
	}
	if options.Decode.MaxResourceBytes == 0 {
		options.Decode.MaxResourceBytes = 1 << 20
	}
	if options.Decode.MaxDocumentBytes == 0 {
		options.Decode.MaxDocumentBytes = 16 << 20
	}
	if options.Decode.MaxDocumentBytes < options.Decode.MaxResourceBytes {
		return options, ErrValidation
	}
	if options.Decode.MaxResources == 0 || uint64(options.Decode.MaxResources) > options.MaxResources {
		options.Decode.MaxResources = int(options.MaxResources)
	}
	// Decode's source label is caller-selected non-secret metadata. Preserve the
	// filename without ever deriving a diagnostic from provider configuration.
	if options.Decode.SourceName == "" {
		options.Decode.SourceName = "startup input"
	}
	return options, nil
}

func startupStageOptions(options StartupOptions, directory, storeID string) StageOptions {
	return StageOptions{Directory: directory, StoreID: storeID, StageID: startupStageIdentity,
		MaxResources: options.MaxResources, MaxEncodedBytes: options.MaxEncodedBytes, Validation: options.Catalog}
}

func fillStartupStage(ctx context.Context, stage *Stage, options StartupOptions) (StartupValidation, error) {
	var result StartupValidation
	if options.OpenSource != nil {
		reader, err := options.OpenSource(ctx)
		if err != nil {
			if reader != nil {
				_ = reader.Close()
			}
			return result, safeStartupError(ctx, err)
		}
		if reader == nil {
			return result, ErrStartupInput
		}
		ordinal := 0
		err = collection.Decode(ctx, reader, options.Decode, func(item collection.Item) error {
			index := ordinal
			ordinal++
			defer clear(item.Resource.Spec)
			resource, credentials, err := ExtractInlineCredentials(item.Resource)
			if err != nil {
				return item.ValidationError(err)
			}
			defer clear(resource.Spec)
			defer func() {
				for _, credential := range credentials {
					clear(credential.Spec)
				}
			}()
			if err := options.Catalog.startupCapabilities(resource); err != nil {
				return item.ValidationError(err)
			}
			for _, credential := range credentials {
				if err := stage.Add(ctx, credential); err != nil {
					return err
				}
			}
			if err := stage.Add(ctx, resource); err != nil {
				return item.ValidationError(err)
			}
			if stage.catalog == nil {
				result.UnverifiedContractReferences += options.Catalog.contractReferenceCount(resource)
			}
			if resource.Kind == "Monitor" {
				result.Monitors++
				for _, warning := range api.ResourceWarnings(resource) {
					warning.Field = fmt.Sprintf("items[%d].resource.%s", index, warning.Field)
					if len(result.Warnings) < 10 {
						result.Warnings = append(result.Warnings, warning)
					} else {
						result.WarningsTruncated = true
					}
				}
			}
			return nil
		})
		closeErr := reader.Close()
		if err != nil {
			return result, safeStartupError(ctx, err)
		}
		if closeErr != nil {
			return result, safeStartupError(ctx, closeErr)
		}
	}
	if result.Monitors == 0 && !options.AllowEmpty {
		return result, ErrStartupEmpty
	}
	info, err := stage.Freeze(ctx)
	if err != nil {
		return result, safeStartupError(ctx, err)
	}
	result.Resources = info.Count
	return result, nil
}

func (o CatalogOptions) startupCapabilities(resource api.Resource) error {
	if err := visitDrivers(&resource, func(category string, driver *api.DriverConfig) error {
		return o.validateNotificationLog(category, *driver)
	}); err != nil {
		return err
	}
	check := func(kind, driver string) error {
		if o.validateDriverType(kind, driver) != nil {
			return ErrStartupDriver
		}
		return nil
	}
	switch resource.Kind {
	case "NotificationEndpoint":
		var driver api.DriverConfig
		if api.StrictDecode(resource.Spec, &driver) != nil {
			return ErrValidation
		}
		return check("notification", driver.Type)
	case "Monitor":
		var spec api.MonitorSpec
		if api.StrictDecode(resource.Spec, &spec) != nil {
			return ErrValidation
		}
		if err := check("check", spec.Check.Driver.Type); err != nil {
			return err
		}
		if spec.Recovery != nil {
			if err := check("recovery", spec.Recovery.Driver.Type); err != nil {
				return err
			}
		}
		if spec.Notifications != nil {
			for _, code := range *spec.Notifications {
				if code.Driver != nil {
					if err := check("notification", code.Driver.Type); err != nil {
						return err
					}
				}
				if code.NotifyType != nil {
					if err := check("notification", *code.NotifyType); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func safeStartupError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var details *collection.InputErrors
	if errors.As(err, &details) {
		return errors.Join(ErrStartupInput, details)
	}
	if field, ok := api.ValidationFieldError(err); ok {
		cause := api.InvalidField(field.Field, field.Reason)
		var source *collection.SourceError
		if errors.As(err, &source) {
			return errors.Join(ErrStartupInput, &collection.SourceError{Location: source.Location, Field: api.SafeValidationField(source.Field), Cause: cause})
		}
		return errors.Join(ErrStartupInput, cause)
	}
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, ErrValidation, ErrStageUnavailable, ErrStageExists, ErrStageMissing, ErrStageLocked, ErrStageQuota, ErrStageDuplicate, ErrStartupDriver, persistence.ErrCatalogDependency} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ErrStartupInput
}

func temporaryStartupDirectory() (string, error) {
	// Resolve the OS-selected temporary root once before creating a private
	// child. For example, macOS commonly exposes /var through /private/var.
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil || !filepath.IsAbs(root) {
		return "", ErrStageUnavailable
	}
	directory, err := os.MkdirTemp(root, "cpra-encrypted-bootstrap-")
	if err != nil {
		return "", ErrStageUnavailable
	}
	if err := protectNewStartupDirectory(directory); err != nil {
		_ = os.RemoveAll(directory)
		return "", err
	}
	return directory, nil
}
