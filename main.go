package main

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	pprof "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/controller"
	"github.com/ziad-hsn/cpra/internal/encryptionsetup"
	"github.com/ziad-hsn/cpra/internal/httpserver"
	"github.com/ziad-hsn/cpra/internal/installpath"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/manifestcheck"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/internal/version"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

type runOptions struct {
	runtimeFile, manifest, dataDir, webAddr, webCors, webAuth, webAuthFile, pprofAddr string
	webAuthRoot, webAuthReaderGroup, webAuthReaderSID                                 string
	debug, pprof, web, ssrfProtect, allowEmpty, validate                              bool
	shutdownTimeout                                                                   time.Duration
	webStarted                                                                        func(string)
}
type shutdownLimitKey struct{}

func main() {
	var o runOptions
	flag.StringVar(&o.runtimeFile, "runtime-config", "", "Runtime storage, history and SLO configuration")
	alias := flag.String("config", "", "Alias for -yaml (monitor manifest)")
	flag.StringVar(&o.manifest, "yaml", "monitors.yaml", "YAML or JSON monitor manifest")
	flag.StringVar(&o.dataDir, "data-dir", "", "Override durable storage directory")
	flag.BoolVar(&o.debug, "debug", false, "Enable debug logging")
	flag.BoolVar(&o.pprof, "pprof", false, "Enable pprof web server on loopback")
	flag.StringVar(&o.pprofAddr, "pprof.addr", "localhost:6060", "pprof listen address (loopback only)")
	flag.BoolVar(&o.web, "web", true, "Enable dashboard, API and metrics; management writes require runtime configuration")
	flag.StringVar(&o.webAddr, "web.addr", "", "Web server listen address (overrides web.address; default localhost:8060)")
	flag.StringVar(&o.webCors, "web.cors", "", "Web API CORS allowlist")
	flag.StringVar(&o.webAuth, "web.auth", "", "API bearer token (prefer -web.auth-file; defaults to CPRA_AUTH_TOKEN)")
	flag.StringVar(&o.webAuthFile, "web.auth-file", "", "File containing API authentication token (defaults to CPRA_AUTH_TOKEN_FILE)")
	flag.StringVar(&o.webAuthRoot, "web.auth-root", "", "Trusted token directory or projected Secret mount (defaults to CPRA_AUTH_TOKEN_ROOT or token directory)")
	flag.StringVar(&o.webAuthReaderGroup, "web.auth-reader-group", "", "Explicit group name or GID for a root-owned Unix token (CPRA_AUTH_TOKEN_READER_GROUP)")
	flag.StringVar(&o.webAuthReaderSID, "web.auth-reader-sid", "", "Explicit Windows service SID permitted to read the token (CPRA_AUTH_TOKEN_READER_SID)")
	flag.BoolVar(&o.ssrfProtect, "ssrf-protect", false, "Reject HTTP targets resolving to private addresses")
	flag.BoolVar(&o.allowEmpty, "allow-empty", false, "Allow an intentionally empty manifest")
	flag.BoolVar(&o.validate, "validate", false, "Validate configuration and compiled drivers without opening storage or providers")
	flag.DurationVar(&o.shutdownTimeout, "shutdown-timeout", 45*time.Second, "Maximum shutdown time (Windows service capped at 15s)")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	capabilitiesFlag := flag.Bool("capabilities", false, "Print compiled driver capabilities as JSON and exit")
	flag.Parse()
	resolveAuthEnvironment(&o, flag.CommandLine, os.Getenv)
	if *versionFlag {
		fmt.Println(version.Info())
		return
	}
	if *capabilitiesFlag {
		if err := json.NewEncoder(os.Stdout).Encode(jobs.Capabilities()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *alias != "" {
		o.manifest = *alias
	}
	if err := runPlatform(func(ctx context.Context, ready func()) error { return runCPRa(ctx, o, ready) }); err != nil {
		fmt.Fprintln(os.Stderr, "CPRa:", err)
		os.Exit(1)
	}
}

// Secrets must never be flag defaults: flag usage prints those values even on
// an unrelated parse error. An explicitly supplied flag, including an empty
// value, takes precedence over its corresponding environment variable.
func resolveAuthEnvironment(o *runOptions, flags *flag.FlagSet, getenv func(string) string) {
	if o.validate {
		return // Static input validation does not use client authentication defaults.
	}
	var tokenSet, fileSet bool
	selected := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) {
		selected[f.Name] = true
		tokenSet = tokenSet || f.Name == "web.auth"
		fileSet = fileSet || f.Name == "web.auth-file"
	})
	if !tokenSet {
		o.webAuth = getenv("CPRA_AUTH_TOKEN")
	}
	if !fileSet {
		o.webAuthFile = getenv("CPRA_AUTH_TOKEN_FILE")
	}
	for _, setting := range []struct {
		flag, environment string
		target            *string
	}{
		{"web.auth-root", "CPRA_AUTH_TOKEN_ROOT", &o.webAuthRoot},
		{"web.auth-reader-group", "CPRA_AUTH_TOKEN_READER_GROUP", &o.webAuthReaderGroup},
		{"web.auth-reader-sid", "CPRA_AUTH_TOKEN_READER_SID", &o.webAuthReaderSID},
	} {
		if !selected[setting.flag] {
			*setting.target = getenv(setting.environment)
		}
	}
}

func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && (host == "localhost" || net.ParseIP(host).IsLoopback())
}

// runCPRa owns every runtime resource. On an uncooperative shutdown timeout,
// main exits the process; it must not close storage underneath its live owner.
func runCPRa(ctx context.Context, o runOptions, ready func()) (resultErr error) {
	if o.shutdownTimeout <= 0 {
		return errors.New("shutdown-timeout must be positive")
	}
	if limit, ok := ctx.Value(shutdownLimitKey{}).(time.Duration); ok && o.shutdownTimeout > limit {
		o.shutdownTimeout = limit
	}
	settings, err := runtimeconfig.Load(o.runtimeFile)
	if err != nil {
		return err
	}
	if o.webAddr == "" {
		o.webAddr = settings.Web.ListenAddress()
	}
	if o.validate {
		err = settings.ResolveValidationDirectory(o.dataDir)
	} else {
		err = settings.ResolveStorageDirectory(o.dataDir)
	}
	if err != nil {
		return err
	}
	if settings.Management.Enabled || settings.EncryptionSource() != nil {
		if err = resolveManagementDirectory(&settings, o.dataDir); err != nil {
			return err
		}
		if err = settings.Validate(); err != nil {
			return err
		}
	}
	if settings.Management.Enabled {
		if !o.web {
			return errors.New("management requires the authenticated web API; enable -web")
		}
		if settings.Management.TrustedProxy != nil && !loopbackAddress(o.webAddr) {
			return errors.New("trusted-proxy management requires a loopback web listener")
		}
		if o.webCors != "" {
			return errors.New("management uses one HTTPS origin; remove -web.cors")
		}
		if source := settings.Management.TLS; source != nil {
			if err := validateManagementTLS(ctx, *source, settings.Storage.Directory); err != nil {
				return err
			}
		}
	}
	if o.pprof && !loopbackAddress(o.pprofAddr) {
		return errors.New("pprof.addr must be a loopback address")
	}
	protectedLogPaths := settings.NotificationProtectedPaths()
	for _, file := range []string{o.runtimeFile, o.manifest, o.webAuthFile} {
		if file != "" {
			protectedLogPaths = append(protectedLogPaths, filepath.Dir(file))
		}
	}
	if err := jobs.ValidateNotificationLogLocation(settings.Notifications.LogDirectory, protectedLogPaths...); err != nil {
		return err
	}
	var inputWarnings []api.FieldError
	warningsTruncated := false
	captureWarning := func(warning api.FieldError) {
		if len(inputWarnings) < 10 {
			inputWarnings = append(inputWarnings, warning)
		} else {
			warningsTruncated = true
		}
	}
	if !settings.Management.Enabled {
		if err = manifestcheck.Manifest(ctx, o.manifest, o.allowEmpty, settings.Storage.Mode == "memory" || settings.EncryptionSource() != nil, settings.Notifications.LogDirectory != "", captureWarning); err != nil {
			return fmt.Errorf("manifest validation: %w", err)
		}
	}
	if o.validate {
		if err := validateAuthenticationInputs(ctx, settings, o); err != nil {
			return err
		}
		if settings.Management.Enabled {
			validation, err := management.ValidateStartupSource(ctx, managementStartupOptions(settings, o))
			if err != nil {
				return fmt.Errorf("management manifest validation: %w", err)
			}
			if validation.UnverifiedContractReferences != 0 {
				fmt.Printf("configuration shape valid; %d stored job contract references require validation during startup or Apply\n", validation.UnverifiedContractReferences)
			}
			inputWarnings, warningsTruncated = validation.Warnings, validation.WarningsTruncated
		}
		for _, warning := range inputWarnings {
			fmt.Printf("warning: %s: %s\n", api.SafeValidationField(warning.Field), api.ValidationMessage(warning.Reason))
		}
		if warningsTruncated {
			fmt.Println("additional configuration warnings omitted; inspect individual monitor configurations")
		}
		fmt.Println("supplied configuration valid; not applied to an existing catalog. Provider access, persistent storage, committed authentication and stored job contracts were not tested")
		return nil
	}
	var encryption *encryptionsetup.Handle
	if settings.Management.Enabled || settings.EncryptionSource() != nil || settings.Storage.Mode == "memory" {
		encryption, err = encryptionsetup.Open(ctx, encryptionsetup.Options{StorageMode: settings.Storage.Mode, DataDirectory: settings.Storage.Directory, Encryption: settings.EncryptionSource()})
		if err != nil {
			return err
		}
	}
	if os.Getenv("CPRA_ENTITY_THRESHOLD") != "" {
		_ = encryption.Close()
		return errors.New("CPRA_ENTITY_THRESHOLD is unsupported: all local pipelines now use bounded fair queues; remove this environment setting")
	}
	jobs.SSRFProtect = o.ssrfProtect
	controller.InitializeLoggers(o.debug)
	store, err := persistence.Open(ctx, settings)
	if err != nil {
		_ = encryption.Close()
		controller.CloseLoggers()
		return fmt.Errorf("durable startup: %w", err)
	}
	config := controller.DefaultConfig()
	config.Debug, config.Store, config.Runtime = o.debug, store, settings
	config.Encryption = encryption.Sealer()
	// Leave part of the process budget for publishing cancellation results and
	// flushing storage. Each operation retains its own configured deadline.
	config.WorkerConfig.DrainTimeout = o.shutdownTimeout * 2 / 3

	var oc *controller.Controller
	var collections runtimeCollectionOwners
	var webSrv *httpserver.Server
	var profiling *http.Server
	var profileDone chan struct{}
	defer func() {
		controller.SystemLogger.Info("Shutdown started; stopping admission and draining accepted work (budget %s)", o.shutdownTimeout)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), o.shutdownTimeout)
		defer cancel()
		// Cancel every owned collection worker before waiting for HTTP admission.
		// Each keeps its dependencies until the shared join boundary below.
		collections.BeginStop()
		// Finish already-admitted commits before asking their operational owner
		// to drain. Reads and diagnostics stay available at this barrier.
		if webSrv != nil {
			if err := webSrv.StopAdmission(shutdownCtx); err != nil {
				if oc != nil {
					oc.CancelWork()
				}
				resultErr = errors.Join(resultErr, fmt.Errorf("shutdown admission deadline exceeded; storage remains owned until process exit: %w", err))
				return
			}
		}
		joined, waitErr := collections.Join(shutdownCtx)
		if !joined {
			if oc != nil {
				oc.CancelWork()
			}
			resultErr = errors.Join(resultErr, fmt.Errorf("collection coordinator shutdown deadline exceeded; storage and encryption remain owned until process exit: %w", waitErr))
			return
		}
		if waitErr != nil && !errors.Is(resultErr, waitErr) {
			resultErr = errors.Join(resultErr, waitErr)
		}
		if settings.Management.Enabled && (webSrv != nil || config.Catalog != nil) {
			// HTTP or collection cancellation can leave a Raft write unconfirmed,
			// including an early startup failure before the HTTP server exists.
			// Join collection workers before ordering all writes for owner drain.
			if err := store.Flush(shutdownCtx); err != nil {
				if oc != nil {
					oc.CancelWork()
				}
				resultErr = errors.Join(resultErr, fmt.Errorf("shutdown durable admission deadline exceeded; storage remains owned until process exit: %w", err))
				return
			}
		}
		if oc != nil {
			oc.BeginStop()
		}
		// The process owns this goroutine until completion or process termination.
		// Never report a successful shutdown or close loggers on deadline expiry.
		type shutdownResult struct {
			err    error
			closed bool
		}
		finished := make(chan shutdownResult, 1)
		go func() {
			if oc != nil {
				oc.Stop()
			}
			var closeErr error
			if !store.Status().Ready {
				closeErr = errors.New("durable storage unavailable during shutdown; final outcomes may not have committed")
			}
			if webSrv != nil {
				if err := webSrv.StopContext(shutdownCtx); err != nil {
					finished <- shutdownResult{err: errors.Join(closeErr, fmt.Errorf("HTTP completion shutdown deadline exceeded; storage and encryption remain owned until process exit: %w", err))}
					return
				}
				// Completion requests remain available during controller drain.
				// Their canceled waits may leave admitted writes unconfirmed; order
				// all of them after the HTTP join before closing their dependencies.
				if err := store.Flush(shutdownCtx); err != nil {
					finished <- shutdownResult{err: errors.Join(closeErr, fmt.Errorf("HTTP completion flush deadline exceeded; storage and encryption remain owned until process exit: %w", err))}
					return
				}
			}
			if profiling != nil {
				_ = profiling.Close()
				<-profileDone
			}
			closeErr = errors.Join(closeErr, config.NotificationLog.Close(), store.Close())
			finished <- shutdownResult{err: errors.Join(closeErr, encryption.Close()), closed: true}
		}()
		select {
		case stopped := <-finished:
			resultErr = errors.Join(resultErr, stopped.err)
			if stopped.closed {
				controller.CloseLoggers()
			}
		case <-shutdownCtx.Done():
			if oc != nil {
				oc.CancelWork()
			}
			resultErr = errors.Join(resultErr, fmt.Errorf("shutdown deadline exceeded; interrupted actions will be held unknown on restart: %w", shutdownCtx.Err()))
		}
	}()
	if directory := settings.Notifications.LogDirectory; directory != "" {
		config.NotificationLog, err = jobs.OpenNotificationLog(directory, protectedLogPaths...)
		if err != nil {
			return err
		}
	}
	authority, auth, err := initializeRuntimeAuthentication(ctx, store, settings, o)
	if err != nil {
		return err
	}
	// Plaintext compatibility sources are consumed only during bootstrap. The
	// committed authority is the sole credential source for every API version.
	o.webAuth = ""
	if settings.Management.Enabled {
		startup, err := management.StartupCatalog(ctx, store, encryption.Sealer(), managementStartupOptions(settings, o))
		if err != nil {
			return fmt.Errorf("management startup: %w", err)
		}
		config.Catalog = startup.Catalog
		if startup.ReusedCatalog {
			controller.SystemLogger.Info("Using committed management configuration; bootstrap file changes are not imported on restart. Use cpractl apply to change monitors.")
		}
		validation, err := config.Catalog.StartCollectionValidationCoordinator(ctx, time.Now)
		if err != nil {
			return fmt.Errorf("collection validation coordinator startup: %w", err)
		}
		collections.validation = validation
	} else {
		exists, err := store.HasCatalog()
		if err != nil {
			return errors.New("cannot verify absence of durable management configuration")
		}
		if exists {
			return errors.New("durable management configuration exists; enable management to recover the authoritative catalog")
		}
	}
	oc = controller.NewController(config)
	if config.Catalog != nil {
		err = oc.LoadCatalog(ctx)
	} else {
		err = oc.LoadMonitors(ctx, o.manifest)
	}
	if err != nil {
		return err
	}
	if err = oc.Start(); err != nil {
		return err
	}
	// An execution worker may commit desired resources immediately. Establish
	// controller ownership first so every accepted child has its live owner.
	if collections.validation != nil {
		err = waitRuntimeInitialization(ctx, oc.WaitReady, collections.validation)
	} else {
		err = oc.WaitReady(ctx)
	}
	if err != nil {
		return err
	}
	if config.Catalog != nil {
		execution, err := config.Catalog.StartCollectionExecutionCoordinator(ctx, time.Now)
		if err != nil {
			return fmt.Errorf("collection execution coordinator startup: %w", err)
		}
		collections.execution = execution
	}
	if o.web {
		var reselection *management.CollectionReselectionManager
		if config.Catalog != nil {
			reselection, err = management.StartCollectionReselectionManager(ctx, config.Catalog, settings.Storage.Mode, settings.Storage.Directory, time.Now)
			if err != nil {
				return fmt.Errorf("collection reselection startup: %w", err)
			}
			collections.reselection = reselection
		}
		queueType := "fair"
		admissionReady := func(ctx context.Context) bool {
			return oc.ReadyContext(ctx) && collections.Ready(ctx)
		}
		webConfig := httpserver.ServerConfig{Addr: o.webAddr, Store: store, Ready: admissionReady, AllowEmpty: o.allowEmpty, CORSAllowOrigins: splitCSV(o.webCors), AuthenticationRequired: !authority.AnonymousLoopback, AllowAnonymousLoopback: authority.AnonymousLoopback, LegacyTokenSHA256: authority.LegacyTokenSHA256, Management: config.Catalog, ManagementAuth: auth, Reselection: reselection}
		configureExternalJobsServer(&webConfig, settings)
		if source := settings.Management.TLS; source != nil {
			webConfig.TLSCertFile, webConfig.TLSKeyFile = source.CertFile, source.KeyFile
		}
		webSrv = httpserver.New(webConfig,
			oc.SnapshotHolder(), oc.Metrics(), oc.PulseQueue(), oc.InterventionQueue(), oc.CodeQueue(), oc.PulsePool(), oc.InterventionPool(), oc.CodePool(),
			httpserver.PublicConfig{QueueCapacity: uint64(oc.PulseQueue().Stats().Capacity), BatchSize: config.BatchSize, AlertCooldown: config.AlertCooldown, RecoveryBypass: config.RecoveryBypass, UseAdaptive: false, QueueType: queueType})
		if err = webSrv.Start(); err != nil {
			return err
		}
		if o.webStarted != nil {
			o.webStarted(webSrv.Address())
		}
	}
	if o.pprof {
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		listener, e := net.Listen("tcp", o.pprofAddr)
		if e != nil {
			return e
		}
		profiling = &http.Server{Handler: httpserver.LoopbackHandler(mux), ReadHeaderTimeout: 5 * time.Second}
		profileDone = make(chan struct{})
		go func() { defer close(profileDone); _ = profiling.Serve(listener) }()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if !oc.ReadyContext(ctx) {
		return errors.New("controller became unavailable before application readiness")
	}
	if err = collections.Failure(ctx); err != nil {
		return err
	}
	ready()
	return collections.Supervise(ctx)
}

// The narrow readiness boundary lets startup join its controller wait when the
// independently owned compiler fails. It is not a factory or mutable test hook.
type runtimeCollectionObserver interface {
	Done() <-chan struct{}
	Err() error
	ReadyContext(context.Context) bool
}

// Collection process owners share one shutdown deadline and Store/key lifetime.
// Fields are assigned during startup, before the HTTP readiness callback runs.
type runtimeCollectionOwner interface {
	runtimeCollectionObserver
	BeginStop()
	Wait(context.Context) error
}

type runtimeCollectionOwners struct {
	validation  runtimeCollectionOwner
	execution   runtimeCollectionOwner
	reselection runtimeCollectionOwner
}

func (owners runtimeCollectionOwners) BeginStop() {
	if owners.validation != nil {
		owners.validation.BeginStop()
	}
	if owners.execution != nil {
		owners.execution.BeginStop()
	}
	if owners.reselection != nil {
		owners.reselection.BeginStop()
	}
}

func (owners runtimeCollectionOwners) Ready(ctx context.Context) bool {
	return ctx.Err() == nil && (owners.validation == nil || owners.validation.ReadyContext(ctx)) && (owners.execution == nil || owners.execution.ReadyContext(ctx)) && (owners.reselection == nil || owners.reselection.ReadyContext(ctx))
}

func (owners runtimeCollectionOwners) Failure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owners.validation != nil && !owners.validation.ReadyContext(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("validation owner: %w", runtimeCollectionFailure(owners.validation))
	}
	if owners.execution != nil && !owners.execution.ReadyContext(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("execution owner: %w", runtimeCollectionFailure(owners.execution))
	}
	if owners.reselection != nil && !owners.reselection.ReadyContext(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("reselection owner: %w", runtimeCollectionFailure(owners.reselection))
	}
	return ctx.Err()
}

func (owners runtimeCollectionOwners) Join(ctx context.Context) (bool, error) {
	joined := true
	var result error
	// BeginStop already reached every owner. Even when one wait expires, inspect
	// the other owner's Done and terminal failure before deciding to retain state.
	for _, entry := range []struct {
		name  string
		owner runtimeCollectionOwner
	}{{"validation", owners.validation}, {"execution", owners.execution}, {"reselection", owners.reselection}} {
		if entry.owner == nil {
			continue
		}
		stopped, err := joinRuntimeCollection(ctx, entry.owner)
		joined = joined && stopped
		if err != nil {
			result = errors.Join(result, fmt.Errorf("%s collection coordinator stopped: %w", entry.name, err))
		}
	}
	return joined, result
}

func (owners runtimeCollectionOwners) Supervise(ctx context.Context) error {
	var validationDone, executionDone, reselectionDone <-chan struct{}
	if owners.validation != nil {
		validationDone = owners.validation.Done()
	}
	if owners.execution != nil {
		executionDone = owners.execution.Done()
	}
	if owners.reselection != nil {
		reselectionDone = owners.reselection.Done()
	}
	select {
	case <-ctx.Done():
		return nil
	case <-validationDone:
		if ctx.Err() == nil {
			return fmt.Errorf("validation owner: %w", runtimeCollectionFailure(owners.validation))
		}
	case <-executionDone:
		if ctx.Err() == nil {
			return fmt.Errorf("execution owner: %w", runtimeCollectionFailure(owners.execution))
		}
	case <-reselectionDone:
		if ctx.Err() == nil {
			return fmt.Errorf("reselection owner: %w", runtimeCollectionFailure(owners.reselection))
		}
	}
	return nil
}

// A joined fatal error permits dependency closure. A canceled wait alone does
// not: Done is the ownership boundary even when both Wait outcomes are errors.
func joinRuntimeCollection(ctx context.Context, coordinator interface {
	Wait(context.Context) error
	Done() <-chan struct{}
}) (bool, error) {
	err := coordinator.Wait(ctx)
	select {
	case <-coordinator.Done():
		return true, err
	default:
		return false, err
	}
}

func runtimeCollectionFailure(coordinator runtimeCollectionObserver) error {
	if err := coordinator.Err(); err != nil {
		return fmt.Errorf("collection coordinator failed: %w", err)
	}
	return errors.New("collection coordinator stopped before application shutdown")
}

func waitRuntimeInitialization(ctx context.Context, waitController func(context.Context) error, coordinator runtimeCollectionObserver) error {
	initializing, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- waitController(initializing) }()
	select {
	case err := <-finished:
		if err != nil {
			return err
		}
	case <-coordinator.Done():
		cancel()
		<-finished // Controller.WaitReady observes cancellation; do not abandon it.
		if err := ctx.Err(); err != nil {
			return err
		}
		return runtimeCollectionFailure(coordinator)
	case <-ctx.Done():
		cancel()
		<-finished
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !coordinator.ReadyContext(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return runtimeCollectionFailure(coordinator)
	}
	return nil
}

func resolveManagementDirectory(settings *runtimeconfig.Config, override string) error {
	if settings.Storage.Mode != "memory" {
		return nil
	}
	// Even explicit memory mode needs an absolute exclusion boundary for key
	// sources. Resolving this location does not create it or make state persistence.
	directory := override
	if directory == "" {
		directory = settings.Storage.Directory
	}
	if directory == "" {
		layout, err := installpath.Resolve("user")
		if err != nil {
			return errors.New("cannot resolve management state boundary")
		}
		directory = layout.StateDir
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return errors.New("cannot resolve management state boundary")
	}
	settings.Storage.Directory = absolute
	return nil
}

func readLegacyWebToken(path string) ([]byte, error) {
	return secureconfig.ReadTokenFile(context.Background(), secureconfig.TokenFileOptions{Path: path, MaxBytes: 4096})
}

func validateManagementTLS(ctx context.Context, source runtimeconfig.ManagementTLS, directory string) error {
	policy := secureconfig.ProtectedFileOptions{Path: source.CertFile, DataDirectory: directory, ReaderGroupID: source.ReaderGroupID, ReaderSID: source.ReaderSID, MaxBytes: 1 << 20}
	certificate, err := secureconfig.ReadProtectedFile(ctx, policy)
	if err != nil {
		return fmt.Errorf("management TLS certificate source: %w", err)
	}
	defer clear(certificate)
	policy.Path = source.KeyFile
	key, err := secureconfig.ReadProtectedFile(ctx, policy)
	if err != nil {
		return fmt.Errorf("management TLS private-key source: %w", err)
	}
	defer clear(key)
	if _, err := tls.X509KeyPair(certificate, key); err != nil {
		return errors.New("cannot load configured management TLS certificate and key")
	}
	return nil
}

func managementStartupOptions(settings runtimeconfig.Config, o runOptions) management.StartupOptions {
	options := management.StartupOptions{DataDirectory: settings.Storage.Directory, AllowEmpty: o.allowEmpty,
		Decode: collection.DecodeOptions{SourceName: "startup manifest"}}
	options.Catalog.NotificationLogEnabled = settings.Notifications.LogDirectory != ""
	configureCatalogOptions(&options.Catalog, settings)
	if o.manifest != "" {
		options.Decode.SourceName = filepath.Base(o.manifest)
		options.OpenSource = func(ctx context.Context) (io.ReadCloser, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			info, err := os.Stat(o.manifest)
			if err != nil || !info.Mode().IsRegular() {
				return nil, errors.New("startup manifest must be a regular file")
			}
			file, err := os.Open(o.manifest)
			if err != nil {
				return nil, errors.New("cannot open startup manifest")
			}
			info, err = file.Stat()
			if err != nil || !info.Mode().IsRegular() {
				_ = file.Close()
				return nil, errors.New("startup manifest must be a regular file")
			}
			if !strings.HasSuffix(strings.ToLower(o.manifest), ".gz") {
				return file, nil
			}
			compressed, err := gzip.NewReader(file)
			if err != nil {
				_ = file.Close()
				return nil, errors.New("cannot read compressed startup manifest")
			}
			return &compressedManifest{Reader: compressed, file: file}, nil
		}
	}
	return options
}

type compressedManifest struct {
	*gzip.Reader
	file *os.File
}

func (source *compressedManifest) Close() error {
	return errors.Join(source.Reader.Close(), source.file.Close())
}

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
func bToMb(b uint64) uint64 { return b / 1024 / 1024 }
func PrintMemUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("Alloc=%d MiB Sys=%d MiB NumGC=%d\n", bToMb(m.Alloc), bToMb(m.Sys), m.NumGC)
}
