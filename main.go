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
	"strconv"
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
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

type runOptions struct {
	runtimeFile, manifest, dataDir, webAddr, webCors, webAuth, webAuthFile, pprofAddr string
	debug, pprof, web, ssrfProtect, allowEmpty, validate                              bool
	shutdownTimeout                                                                   time.Duration
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
	flag.StringVar(&o.webAddr, "web.addr", "localhost:8060", "Web server listen address")
	flag.StringVar(&o.webCors, "web.cors", "", "Web API CORS allowlist")
	flag.StringVar(&o.webAuth, "web.auth", os.Getenv("CPRA_AUTH_TOKEN"), "API bearer token (prefer -web.auth-file)")
	flag.StringVar(&o.webAuthFile, "web.auth-file", os.Getenv("CPRA_AUTH_TOKEN_FILE"), "File containing API authentication token")
	flag.BoolVar(&o.ssrfProtect, "ssrf-protect", false, "Reject HTTP targets resolving to private addresses")
	flag.BoolVar(&o.allowEmpty, "allow-empty", false, "Allow an intentionally empty manifest")
	flag.BoolVar(&o.validate, "validate", false, "Validate configuration and compiled drivers without opening storage or providers")
	flag.DurationVar(&o.shutdownTimeout, "shutdown-timeout", 45*time.Second, "Maximum shutdown time (Windows service capped at 15s)")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	capabilitiesFlag := flag.Bool("capabilities", false, "Print compiled driver capabilities as JSON and exit")
	flag.Parse()
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
	if err = settings.ResolveStorageDirectory(o.dataDir); err != nil {
		return err
	}
	if settings.Management.Enabled {
		if err = resolveManagementDirectory(&settings, o.dataDir); err != nil {
			return err
		}
		if err = settings.Management.ValidateDataDirectory(settings.Storage.Directory); err != nil {
			return err
		}
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
	if !settings.Management.Enabled {
		if err = manifestcheck.Manifest(ctx, o.manifest, o.allowEmpty); err != nil {
			return fmt.Errorf("manifest validation: %w", err)
		}
	}
	if o.validate {
		if err := validateAuthenticationInputs(ctx, settings, o); err != nil {
			return err
		}
		if settings.Management.Enabled {
			if _, err := management.ValidateStartupSource(ctx, managementStartupOptions(settings, o)); err != nil {
				return fmt.Errorf("management manifest validation: %w", err)
			}
		}
		fmt.Println("configuration valid; provider access, persistent storage and committed authentication were not tested")
		return nil
	}
	var encryption *encryptionsetup.Handle
	if settings.Management.Enabled {
		encryption, err = encryptionsetup.Open(ctx, encryptionsetup.Options{StorageMode: settings.Storage.Mode, DataDirectory: settings.Storage.Directory, Encryption: settings.Management.Encryption})
		if err != nil {
			return err
		}
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
	// Leave part of the process budget for publishing cancellation results and
	// flushing storage. Each operation retains its own configured deadline.
	config.WorkerConfig.DrainTimeout = o.shutdownTimeout * 2 / 3
	if v := os.Getenv("CPRA_ENTITY_THRESHOLD"); v != "" {
		if n, e := strconv.ParseInt(v, 10, 64); e == nil && n > 0 {
			config.EntityCountThreshold = n
		}
	}
	var oc *controller.Controller
	var collections runtimeCollectionOwners
	var webSrv *httpserver.Server
	var profiling *http.Server
	var profileDone chan struct{}
	defer func() {
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
		finished := make(chan error, 1)
		go func() {
			if oc != nil {
				oc.Stop()
			}
			var closeErr error
			if !store.Status().Ready {
				closeErr = errors.New("durable storage unavailable during shutdown; final outcomes may not have committed")
			}
			if webSrv != nil {
				closeErr = errors.Join(closeErr, webSrv.StopContext(shutdownCtx))
			}
			if profiling != nil {
				_ = profiling.Close()
				<-profileDone
			}
			closeErr = errors.Join(closeErr, store.Close())
			finished <- errors.Join(closeErr, encryption.Close())
		}()
		select {
		case e := <-finished:
			resultErr = errors.Join(resultErr, e)
			controller.CloseLoggers()
		case <-shutdownCtx.Done():
			if oc != nil {
				oc.CancelWork()
			}
			resultErr = errors.Join(resultErr, fmt.Errorf("shutdown deadline exceeded; interrupted actions will be held unknown on restart: %w", shutdownCtx.Err()))
		}
	}()
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
		queueType := "hybrid"
		if oc.UseAdaptiveQueue() {
			queueType = "adaptive"
		}
		admissionReady := func() bool {
			return oc.Ready() && collections.Ready()
		}
		webConfig := httpserver.ServerConfig{Addr: o.webAddr, Store: store, Ready: admissionReady, AllowEmpty: o.allowEmpty, CORSAllowOrigins: splitCSV(o.webCors), AuthenticationRequired: !authority.AnonymousLoopback, AllowAnonymousLoopback: authority.AnonymousLoopback, LegacyTokenSHA256: authority.LegacyTokenSHA256, Management: config.Catalog, ManagementAuth: auth, Reselection: reselection}
		configureExternalJobsServer(&webConfig, settings)
		if source := settings.Management.TLS; source != nil {
			webConfig.TLSCertFile, webConfig.TLSKeyFile = source.CertFile, source.KeyFile
		}
		webSrv = httpserver.New(webConfig,
			oc.SnapshotHolder(), oc.Metrics(), oc.PulseQueue(), oc.InterventionQueue(), oc.CodeQueue(), oc.PulsePool(), oc.InterventionPool(), oc.CodePool(),
			httpserver.PublicConfig{QueueCapacity: uint64(oc.PulseQueue().Stats().Capacity), BatchSize: config.BatchSize, AlertCooldown: config.AlertCooldown, RecoveryBypass: config.RecoveryBypass, UseAdaptive: oc.UseAdaptiveQueue(), QueueType: queueType})
		if err = webSrv.Start(); err != nil {
			return err
		}
	}
	if o.pprof {
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		listener, e := net.Listen("tcp", o.pprofAddr)
		if e != nil {
			return e
		}
		profiling = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		profileDone = make(chan struct{})
		go func() { defer close(profileDone); _ = profiling.Serve(listener) }()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if !oc.Ready() {
		return errors.New("controller became unavailable before application readiness")
	}
	if err = collections.Failure(); err != nil {
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
	Ready() bool
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

func (owners runtimeCollectionOwners) Ready() bool {
	return (owners.validation == nil || owners.validation.Ready()) && (owners.execution == nil || owners.execution.Ready()) && (owners.reselection == nil || owners.reselection.Ready())
}

func (owners runtimeCollectionOwners) Failure() error {
	if owners.validation != nil && !owners.validation.Ready() {
		return fmt.Errorf("validation owner: %w", runtimeCollectionFailure(owners.validation))
	}
	if owners.execution != nil && !owners.execution.Ready() {
		return fmt.Errorf("execution owner: %w", runtimeCollectionFailure(owners.execution))
	}
	if owners.reselection != nil && !owners.reselection.Ready() {
		return fmt.Errorf("reselection owner: %w", runtimeCollectionFailure(owners.reselection))
	}
	return nil
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
	if !coordinator.Ready() {
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
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("web auth source must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open web auth file")
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("web auth source must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		clear(data)
		return nil, errors.New("cannot read bounded web auth file")
	}
	return data, nil
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
	if o.manifest != "" {
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
