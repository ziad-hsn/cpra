package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	pprof "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cpra/internal/controller"
	"cpra/internal/durable"
	"cpra/internal/jobs"
	"cpra/internal/runtimeconfig"
	"cpra/internal/version"
	"cpra/internal/web/server"
)

func main() {
	// Command line flags
	var (
		runtimeFile = flag.String("runtime-config", "", "Runtime storage, history and SLO configuration")
		configFile  = flag.String("config", "", "Alias for -yaml (monitor manifest)")
		yamlFile    = flag.String("yaml", "monitors.yaml", "YAML or JSON monitor manifest")
		debug       = flag.Bool("debug", false, "Enable debug logging")
		pprofEnable = flag.Bool("pprof", false, "Enable pprof web server (debug only; binds loopback)")
		pprofAddr   = flag.String("pprof.addr", "localhost:6060", "pprof listen address (host:port)")
		webEnable   = flag.Bool("web", true, "Enable the read-only web server (dashboard + API + /metrics)")
		webAddr     = flag.String("web.addr", "localhost:8060", "Web server listen address (host:port)")
		webCors     = flag.String("web.cors", "", "CORS allowed origins for the web API (comma-separated, or * for all; dev only)")
		webAuth     = flag.String("web.auth", os.Getenv("CPRA_AUTH_TOKEN"), "Optional bearer token required for the web API (empty disables auth)")
		ssrfProtect = flag.Bool("ssrf-protect", false, "Reject HTTP(S) targets resolving to private/loopback/link-local addresses (SSRF protection)")
		webAuthFile = flag.String("web.auth-file", os.Getenv("CPRA_AUTH_TOKEN_FILE"), "File containing the web authentication token")
		allowEmpty  = flag.Bool("allow-empty", false, "Allow a manifest with no monitors")
		versionFlag = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *versionFlag {
		fmt.Println(version.Info())
		return
	}

	if *configFile != "" {
		*yamlFile = *configFile
	}
	if *webAuthFile != "" {
		data, err := os.ReadFile(*webAuthFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Read web auth file:", err)
			os.Exit(1)
		}
		*webAuth = strings.TrimSpace(string(data))
		if *webAuth == "" {
			fmt.Fprintln(os.Stderr, "Web auth file is empty")
			os.Exit(1)
		}
	}
	if *webEnable && *webAuth == "" {
		host, _, err := net.SplitHostPort(*webAddr)
		ip := net.ParseIP(host)
		if err != nil || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			fmt.Fprintln(os.Stderr, "A non-loopback web listener requires CPRA_AUTH_TOKEN or -web.auth-file")
			os.Exit(1)
		}
	}
	// Enable SSRF protection when the monitor manifest is not a trusted input.
	jobs.SSRFProtect = *ssrfProtect

	// Initialize loggers first
	controller.InitializeLoggers(*debug)

	controller.SystemLogger.Info("Starting CPRA Controller")
	if *pprofEnable {
		go func(addr string) {
			mux := http.NewServeMux()
			mux.HandleFunc("/debug/pprof/", pprof.Index)
			mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
			mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
			mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
			mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
			controller.SystemLogger.Info("Profiling server listening at http://%s/debug/pprof/", addr)
			if err := http.ListenAndServe(addr, mux); err != nil {
				controller.SystemLogger.Warn("Profiling server error: %v", err)
			}
		}(*pprofAddr)
	}
	controller.SystemLogger.Info("Input file: %s", *yamlFile)

	// Create configuration
	config := controller.DefaultConfig()
	config.Debug = *debug

	// Optional entity-count threshold for queue selection before startup.
	if v := os.Getenv("CPRA_ENTITY_THRESHOLD"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			config.EntityCountThreshold = n
		}
	}

	runtimeSettings, err := runtimeconfig.Load(*runtimeFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Runtime configuration:", err)
		os.Exit(1)
	}
	store, err := durable.Open(context.Background(), runtimeSettings)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Durable startup:", err)
		os.Exit(1)
	}
	defer store.Close()
	config.Store, config.Runtime = store, runtimeSettings

	// Create the new controller
	oc := controller.NewController(config)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Load monitors if YAML file exists
	if _, err := os.Stat(*yamlFile); err == nil {
		fmt.Println("Loading monitors from", *yamlFile, "...")
		start := time.Now()

		if err := oc.LoadMonitors(ctx, *yamlFile); err != nil {
			fmt.Println("Error loading monitors:", err)
			os.Exit(1)
		}

		fmt.Println("Monitor loading completed in", time.Since(start))
	} else {
		fmt.Fprintln(os.Stderr, "Cannot open monitor manifest:", err)
		os.Exit(1)
	}

	if !*allowEmpty && oc.GetWorld().Stats().Entities.Used == 0 {
		fmt.Fprintln(os.Stderr, "Monitor manifest contains no monitors; use -allow-empty for an intentional empty instance")
		os.Exit(1)
	}

	// Start the controller
	if err := oc.Start(); err != nil {
		fmt.Println("Error starting controller:", err)
		os.Exit(1)
	}

	// Start the read-only web server (dashboard + REST API + Prometheus /metrics).
	var webSrv *server.Server
	if *webEnable {
		queueType := "hybrid"
		if oc.UseAdaptiveQueue() {
			queueType = "adaptive"
		}
		webSrv = server.New(server.ServerConfig{
			Addr:             *webAddr,
			Store:            store,
			CORSAllowOrigins: splitCSV(*webCors),
			AuthToken:        *webAuth,
		}, oc.SnapshotHolder(), oc.Metrics(),
			oc.PulseQueue(), oc.InterventionQueue(), oc.CodeQueue(),
			oc.PulsePool(), oc.InterventionPool(), oc.CodePool(),
			server.PublicConfig{
				QueueCapacity:  uint64(oc.PulseQueue().Stats().Capacity),
				BatchSize:      config.BatchSize,
				AlertCooldown:  config.AlertCooldown,
				RecoveryBypass: config.RecoveryBypass,
				UseAdaptive:    oc.UseAdaptiveQueue(),
				QueueType:      queueType,
			})
		if err := webSrv.Start(); err != nil {
			fmt.Println("Error starting web server:", err)
			oc.Stop()
			controller.CloseLoggers()
			os.Exit(1)
		}
	}

	// Wait for shutdown signal
	<-ctx.Done()
	fmt.Println("Shutting down...")

	// Stop the web server first so in-flight requests drain before the controller.
	if webSrv != nil {
		webSrv.Stop()
	}

	// Print memory Usage
	PrintMemUsage()

	// Stop the controller
	oc.Stop()

	// Close loggers after everything is done
	controller.CloseLoggers()

	fmt.Println("CPRA Controller stopped")
}

// bToMb converts bytes to megabytes
func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
}

// splitCSV splits a comma-separated string into trimmed, non-empty fields.
func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// PrintMemUsage outputs the current, total, and system memory usage
func PrintMemUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Println("Memory usage on exit:")
	fmt.Println("Alloc =", bToMb(m.Alloc), "MiB")
	fmt.Println("TotalAlloc =", bToMb(m.TotalAlloc), "MiB")
	fmt.Println("Sys =", bToMb(m.Sys), "MiB")
	fmt.Println("NumGC =", m.NumGC)
}
