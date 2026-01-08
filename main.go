package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	// automaxprocs automatically sets GOMAXPROCS to match container CPU quota.
	_ "go.uber.org/automaxprocs"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"

	"cpra/gen/cpra/v1/cprav1connect"
	"cpra/internal/api/handlers"
	"cpra/internal/config"
	"cpra/internal/controller"
	"cpra/internal/runtime/jobs"
	"cpra/internal/platform/limits"
	"cpra/internal/logger"
	"cpra/internal/observability"
	cpraRuntime "cpra/internal/runtime"
	"cpra/internal/server"
	"cpra/internal/runtime/shutdown"
	"expvar"
)

// appStart tracks application start time for uptime metrics.
var appStart = time.Now()

// ShutdownTimeout is the maximum time allowed for graceful shutdown
const ShutdownTimeout = 30 * time.Second

func main() {

	// Command line flags
	var (
		configFile  = flag.String("config", "", "Configuration file path")
		yamlFile    = flag.String("yaml", "internal/loader/replicated_test.yaml", "YAML file with monitors")
		debug       = flag.Bool("debug", false, "Enable debug logging")
		pprofEnable = flag.Bool("pprof", false, "Enable pprof web server (security risk if exposed)")
		pprofAddr   = flag.String("pprof.addr", "localhost:6060", "pprof listen address (host:port)")

		// GC tuning flags for large-scale deployments (1M+ monitors)
		memLimitMiB   = flag.Int("memlimit", 0, "Memory limit in MiB (sets GOMEMLIMIT, 0=use env or default)")
		gogcPercent   = flag.Int("gogc", 0, "GC target percentage (sets GOGC, 0=use env or default 100)")
		ballastSizeMB = flag.Int("ballast", 0, "GC ballast size in MB (0=disabled, recommended: 512-1024 for large heaps)")

		// Profiling flags for performance analysis
		blockProfileRate = flag.Int("blockprofile", 0, "Block profile rate (0=disabled, 1=all, >1=sampling rate in ns)")
		mutexProfileFrac = flag.Int("mutexprofile", 0, "Mutex profile fraction (0=disabled, 1=all, >1=1/n sampling)")
	)
	flag.Parse()

	// Normalize file paths for cross-platform compatibility.
	*yamlFile = filepath.Clean(*yamlFile)
	if *configFile != "" {
		*configFile = filepath.Clean(*configFile)
	}

	// Configure GC tuning and contention profiling
	cpraRuntime.ConfigureGC(*memLimitMiB, *gogcPercent, *ballastSizeMB)
	cpraRuntime.ConfigureContentionProfiling(*blockProfileRate, *mutexProfileFrac)

	// Load and validate centralized environment configuration
	envCfg := config.Load()

	// Apply --debug flag to env config if set (CLI takes precedence)
	if *debug && !envCfg.Debug {
		envCfg.Debug = true
		envCfg.LogLevel = "debug"
		envCfg.LogDevelopment = true
	}

	if err := envCfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	// Initialize root logger
	// Using "SYSTEM" component as the root logger for main
	log, err := logger.NewLoggerWithComponentFromConfig(envCfg, "SYSTEM")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	log.Info("Starting CPRA Optimized Controller for 1M Monitors")

	// Log environment configuration at startup
	log.Info("Environment configuration:")
	for _, line := range envCfg.GetConfigLines() {
		if line.IsCustom {
			log.Info("Custom config", logger.Field{Key: line.Key, Value: line.Value}, logger.Field{Key: "default", Value: line.Default})
		} else {
			log.Debug("Default config", logger.Field{Key: line.Key, Value: line.Value})
		}
	}

	// Setup pprof server with graceful shutdown capability
	pprofServer := observability.SetupPprofServer(log, *pprofEnable, *pprofAddr)
	log.Info("Input file", logger.Field{Key: "path", Value: *yamlFile})

	// Create an optimized configuration with the pre-loaded env config
	ctrlConfig := controller.DefaultConfig()
	ctrlConfig.EnvConfig = envCfg // Use the already-loaded and validated config
	ctrlConfig.Debug = *debug
	// Controller expects *zap.SugaredLogger.
	// We need to type assert or use the Sugar() method if we have access to the concrete type,
	// or create a new SugaredLogger.
	// Since NewLoggerWithComponentFromConfig returns Logger interface, we can't easily get Sugar()
	// unless we type assert to *ZapLogger.
	if zapLog, ok := log.(*logger.ZapLogger); ok {
		ctrlConfig.Logger = zapLog.Sugar()
	} else {
		// Fallback if not ZapLogger (shouldn't happen with current implementation)
		// Re-create simple sugared logger
		slog, _ := logger.NewSugaredLoggerWithComponentFromConfig(envCfg, "CONTROLLER")
		ctrlConfig.Logger = slog
	}

	// Override configuration if file provided
	if *configFile != "" {
		log.Info("Loading configuration", logger.Field{Key: "file", Value: *configFile})
		// Configuration loading would be implemented here
	}

	// Create the new optimized controller
	oc, err := controller.NewController(ctrlConfig)
	if err != nil {
		log.Fatal("Failed to create controller", logger.Field{Key: "error", Value: err.Error()})
	}

	// Log detected runtime limits (non-fatal if undefined)
	if lim := limits.Detect(); lim.Cores != nil || lim.MemoryBytes != nil {
		var coresVal any = "undefined"
		if lim.Cores != nil {
			coresVal = *lim.Cores
		}
		var memVal any = "undefined"
		if lim.MemoryBytes != nil {
			memVal = *lim.MemoryBytes
		}
		log.Info("Runtime limits detected",
			logger.Field{Key: "source", Value: lim.Source},
			logger.Field{Key: "cores", Value: coresVal},
			logger.Field{Key: "memory_bytes", Value: memVal})
	} else {
		log.Debug("Runtime limits undefined", logger.Field{Key: "source", Value: lim.Source})
	}

	// Publish expvar metrics for pull-based telemetry (security-safe subset)
	publishExpvarMetrics(oc)

	// Setup context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var shutdownInitiated sync.Once

	reasonCh := shutdown.Listen(func() {
		shutdownInitiated.Do(cancel)
	})

	go func() {
		reason := <-reasonCh
		shutdownInitiated.Do(func() {
			log.Info("Shutdown signal received", logger.Field{Key: "reason", Value: reason})
			cancel()
		})
	}()

	// Load monitors
	log.Info("Loading monitors", logger.Field{Key: "file", Value: *yamlFile})
	start := time.Now()

	if err := oc.LoadMonitors(ctx, *yamlFile); err != nil {
		log.Fatal("Error loading monitors", logger.Field{Key: "error", Value: err.Error()})
	}

	log.Info("Monitor loading completed", logger.Field{Key: "duration", Value: time.Since(start)})

	// Start the optimized controller
	if err := oc.Start(ctx); err != nil {
		log.Fatal("Error starting controller", logger.Field{Key: "error", Value: err.Error()})
	}

	// Start watchdog in separate goroutine with heartbeat monitoring
	watchdogHeartbeat := make(chan struct{}, 1)
	wdConfig := controller.DefaultWatchdogConfig()
	if wdConfig.Enabled {
		// Create a separate logger for watchdog
		wdLogger, _ := logger.NewLoggerWithComponentFromConfig(envCfg, "WATCHDOG")
		wd := controller.NewWatchdog(oc, wdConfig, watchdogHeartbeat, wdLogger)
		go func() {
			if err := wd.Run(ctx); err != nil {
				log.Error("Watchdog error", logger.Field{Key: "error", Value: err.Error()})
			}
		}()

		// Monitor watchdog health from main
		go monitorWatchdogHealth(ctx, log, watchdogHeartbeat, wdConfig.CheckInterval*3)
	}

	// Setup HTTP server with Connect-Go API handlers and OTEL interceptors
	var apiServer *server.Server
	if envCfg.HealthPort > 0 {
		// Create a dedicated logger for the API server
		apiLogger, logErr := logger.NewSugaredLoggerWithComponentFromConfig(envCfg, "API")
		if logErr != nil {
			log.Error("Failed to create API logger", logger.Field{Key: "error", Value: logErr.Error()})
		} else {
			var serverErr error
			apiServer, serverErr = server.NewServer(envCfg, apiLogger, oc.CommandChan())
			if serverErr != nil {
				log.Error("Failed to create API server", logger.Field{Key: "error", Value: serverErr.Error()})
			} else {
				// Create OTEL interceptor for Connect-Go handlers
				otelInterceptor, _ := otelconnect.NewInterceptor()

				// Create and register MonitorService handler
				monitorHandler := handlers.NewMonitorHandler(oc.CommandChan())
				path, handler := cprav1connect.NewMonitorServiceHandler(
					monitorHandler,
					connect.WithInterceptors(otelInterceptor),
				)
				apiServer.RegisterHandler(path, handler)

				statusHandler := handlers.NewStatusHandler(oc, envCfg.ServiceName, envCfg.ServiceVersion, appStart)
				apiServer.RegisterHandler("/status", statusHandler)

				// Start API server in background
				go func() {
					if err := apiServer.Start(ctx); err != nil && err != http.ErrServerClosed {
						log.Error("API server error", logger.Field{Key: "error", Value: err.Error()})
					}
				}()
				log.Info("API server started", logger.Field{Key: "port", Value: envCfg.HealthPort})
			}
		}
	}

	// Wait for a shutdown signal and perform graceful shutdown
	performGracefulShutdown(ctx, log, oc, pprofServer)
}

func publishExpvarMetrics(oc *controller.Controller) {
	expvar.Publish("cpra_controller", expvar.Func(func() any {
		stats := oc.Stats()
		return map[string]any{
			"pulse_queue_depth":           stats.PulseQueue,
			"intervention_queue_depth":    stats.InterventionQueue,
			"code_queue_depth":            stats.CodeQueue,
			"pulse_workers_active":        stats.PulseWorkers,
			"intervention_workers_active": stats.InterventionWorkers,
			"code_workers_active":         stats.CodeWorkers,
			"world_entities":              stats.World,
			"uptime_seconds":              time.Since(appStart).Seconds(),
		}
	}))

	expvar.Publish("cpra_circuit_breakers", expvar.Func(func() any {
		if cb := jobs.GetDockerCircuitBreaker(); cb != nil {
			failures, successes, state := cb.GetMetrics()
			return map[string]any{
				"docker_failures":  failures,
				"docker_successes": successes,
				"docker_state":     state,
			}
		}
		return map[string]any{}
	}))

	expvar.Publish("cpra_runtime", expvar.Func(func() any {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		lastPauseNs := uint64(0)
		if m.NumGC > 0 {
			lastPauseNs = m.PauseNs[(m.NumGC+255)%256]
		}

		return map[string]any{
			"goroutines":        runtime.NumGoroutine(),
			"gomaxprocs":        runtime.GOMAXPROCS(0),
			"num_cpu":           runtime.NumCPU(),
			"heap_alloc_mb":     m.Alloc / 1024 / 1024,
			"heap_sys_mb":       m.HeapSys / 1024 / 1024,
			"heap_idle_mb":      m.HeapIdle / 1024 / 1024,
			"heap_inuse_mb":     m.HeapInuse / 1024 / 1024,
			"heap_released_mb":  m.HeapReleased / 1024 / 1024,
			"heap_objects":      m.HeapObjects,
			"stack_inuse_mb":    m.StackInuse / 1024 / 1024,
			"stack_sys_mb":      m.StackSys / 1024 / 1024,
			"sys_mb":            m.Sys / 1024 / 1024,
			"total_alloc_mb":    m.TotalAlloc / 1024 / 1024,
			"num_gc":            m.NumGC,
			"gc_cpu_fraction":   m.GCCPUFraction,
			"gc_pause_total_ms": m.PauseTotalNs / 1_000_000,
			"gc_last_pause_us":  lastPauseNs / 1000,
			"num_forced_gc":     m.NumForcedGC,
			"next_gc_mb":        m.NextGC / 1024 / 1024,
			"mallocs":           m.Mallocs,
			"frees":             m.Frees,
			"live_objects":      m.Mallocs - m.Frees,
		}
	}))
}

func performGracefulShutdown(ctx context.Context, log logger.Logger, oc *controller.Controller, pprofServer *http.Server) {
	<-ctx.Done()

	shutdownStart := time.Now()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer shutdownCancel()

	log.Info("=== GRACEFUL SHUTDOWN STARTED ===")

	log.Info("Stopping controller and draining worker pools...", logger.Field{Key: "step", Value: "1/5"})
	controllerDone := make(chan struct{})
	go func() {
		oc.Stop(shutdownCtx)
		close(controllerDone)
	}()

	select {
	case <-controllerDone:
		log.Info("Controller stopped successfully", logger.Field{Key: "step", Value: "1/5"})
	case <-shutdownCtx.Done():
		log.Warn("Controller stop timed out", logger.Field{Key: "step", Value: "1/5"}, logger.Field{Key: "timeout", Value: ShutdownTimeout.String()})
	}

	log.Info("Flushing log manager...", logger.Field{Key: "step", Value: "2/5"})
	logManagerDone := make(chan struct{})
	go func() {
		defer close(logManagerDone)
		jobs.GetLogManager().Shutdown()
	}()

	logFlushTimeout := 10 * time.Second
	select {
	case <-logManagerDone:
		log.Info("Log manager flushed successfully", logger.Field{Key: "step", Value: "2/5"})
	case <-time.After(logFlushTimeout):
		log.Warn("Log manager flush timed out", logger.Field{Key: "step", Value: "2/5"}, logger.Field{Key: "timeout", Value: logFlushTimeout.String()})
	case <-shutdownCtx.Done():
		log.Warn("Log manager flush cancelled due to overall shutdown timeout", logger.Field{Key: "step", Value: "2/5"})
	}

	observability.StopPprofServer(log, pprofServer, 5*time.Second)

	log.Info("Final memory statistics:", logger.Field{Key: "step", Value: "4/5"})
	cpraRuntime.PrintMemUsage()

	log.Info("Closing loggers...", logger.Field{Key: "step", Value: "5/5"})
	log.Info("=== GRACEFUL SHUTDOWN COMPLETED ===", logger.Field{Key: "duration", Value: time.Since(shutdownStart).String()})

	controller.CloseLoggers()

	fmt.Println("CPRA Optimized Controller stopped")
}

// monitorWatchdogHealth monitors the watchdog goroutine health via heartbeat.
// Uses Go 1.23+ timer semantics which are race-free for Reset.
func monitorWatchdogHealth(ctx context.Context, log logger.Logger, heartbeat <-chan struct{}, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		case <-timer.C:
			log.Warn("Watchdog heartbeat missed", logger.Field{Key: "timeout", Value: timeout})
			timer.Reset(timeout)
		}
	}
}
