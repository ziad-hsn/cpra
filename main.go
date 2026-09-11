package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	pprof "net/http/pprof"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/controller"
	"github.com/ziad-hsn/cpra/internal/durable"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/preflight"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/version"
	"github.com/ziad-hsn/cpra/internal/web/server"
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
	flag.BoolVar(&o.web, "web", true, "Enable read-only dashboard, API and metrics")
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
	if o.webAuthFile != "" {
		data, err := os.ReadFile(o.webAuthFile)
		if err != nil {
			return fmt.Errorf("read web auth file: %w", err)
		}
		o.webAuth = strings.TrimSpace(string(data))
		if o.webAuth == "" {
			return errors.New("web auth file is empty")
		}
	}
	if o.web && o.webAuth == "" && !loopbackAddress(o.webAddr) {
		return errors.New("a non-loopback web listener requires CPRA_AUTH_TOKEN or -web.auth-file")
	}
	if o.pprof && !loopbackAddress(o.pprofAddr) {
		return errors.New("pprof.addr must be a loopback address")
	}
	settings, err := runtimeconfig.Load(o.runtimeFile)
	if err != nil {
		return err
	}
	if err = settings.ResolveStorageDirectory(o.dataDir); err != nil {
		return err
	}
	if err = preflight.Manifest(ctx, o.manifest, o.allowEmpty); err != nil {
		return fmt.Errorf("manifest validation: %w", err)
	}
	if o.validate {
		fmt.Println("configuration valid; provider access and storage were not tested")
		return nil
	}
	jobs.SSRFProtect = o.ssrfProtect
	controller.InitializeLoggers(o.debug)
	store, err := durable.Open(ctx, settings)
	if err != nil {
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
	oc := controller.NewController(config)
	var webSrv *server.Server
	var profiling *http.Server
	var profileDone chan struct{}
	defer func() {
		oc.BeginStop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), o.shutdownTimeout)
		defer cancel()
		// The process owns this goroutine until completion or process termination.
		// Never report a successful shutdown or close loggers on deadline expiry.
		finished := make(chan error, 1)
		go func() {
			oc.Stop()
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
			finished <- errors.Join(closeErr, store.Close())
		}()
		select {
		case e := <-finished:
			resultErr = errors.Join(resultErr, e)
			controller.CloseLoggers()
		case <-shutdownCtx.Done():
			oc.CancelWork()
			resultErr = errors.Join(resultErr, fmt.Errorf("shutdown deadline exceeded; interrupted actions will be held unknown on restart: %w", shutdownCtx.Err()))
		}
	}()
	if err = oc.LoadMonitors(ctx, o.manifest); err != nil {
		return err
	}
	if err = oc.Start(); err != nil {
		return err
	}
	if o.web {
		queueType := "hybrid"
		if oc.UseAdaptiveQueue() {
			queueType = "adaptive"
		}
		webSrv = server.New(server.ServerConfig{Addr: o.webAddr, Store: store, Ready: oc.Ready, AllowEmpty: o.allowEmpty, CORSAllowOrigins: splitCSV(o.webCors), AuthToken: o.webAuth},
			oc.SnapshotHolder(), oc.Metrics(), oc.PulseQueue(), oc.InterventionQueue(), oc.CodeQueue(), oc.PulsePool(), oc.InterventionPool(), oc.CodePool(),
			server.PublicConfig{QueueCapacity: uint64(oc.PulseQueue().Stats().Capacity), BatchSize: config.BatchSize, AlertCooldown: config.AlertCooldown, RecoveryBypass: config.RecoveryBypass, UseAdaptive: oc.UseAdaptiveQueue(), QueueType: queueType})
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
	if err = oc.WaitReady(ctx); err != nil {
		return err
	}
	ready()
	<-ctx.Done()
	return nil
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
