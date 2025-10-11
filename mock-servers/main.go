package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// LightweightServer represents a single, lightweight HTTP server instance.
type LightweightServer struct {
	ID      int64
	Port    int
	Server  *http.Server
	running int32 // 1 for running, 0 for stopped
}

// ServerPool manages a collection of LightweightServer instances.
type ServerPool struct {
	servers   map[int]*LightweightServer
	counter   int64
	basePort  int
	maxPort   int
	mu        sync.RWMutex
	stats     PoolStats
	startTime time.Time
}

// PoolStats contains statistics about the server pool.
type PoolStats struct {
	TotalStarted int64 `json:"total_started"`
	TotalKilled  int64 `json:"total_killed"`
	CurrentAlive int64 `json:"current_alive"`
}

// NewServerPool creates and initializes a new ServerPool.
func NewServerPool(basePort, maxPort int) *ServerPool {
	return &ServerPool{
		servers:   make(map[int]*LightweightServer),
		basePort:  basePort,
		maxPort:   maxPort,
		startTime: time.Now(),
	}
}

// StartServers starts a specified number of servers in the pool using a worker pool pattern.
func (sp *ServerPool) StartServers(count int) error {
	runtime.GOMAXPROCS(runtime.NumCPU() * 2)
	log.Printf("Attempting to start %d servers...", count)

	// Pre-allocate ports to eliminate lock contention
	ports := make([]int, 0, count)
	sp.mu.Lock()
	for port := sp.basePort; port <= sp.maxPort && len(ports) < count; port++ {
		if _, exists := sp.servers[port]; !exists {
			ports = append(ports, port)
			// Pre-register the port to prevent race conditions
			sp.servers[port] = &LightweightServer{Port: port}
		}
	}
	sp.mu.Unlock()

	if len(ports) < count {
		return fmt.Errorf("only %d ports available, requested %d", len(ports), count)
	}

	// Create a buffered channel for port queue
	portChan := make(chan int, count)
	for _, port := range ports {
		portChan <- port
	}
	close(portChan)

	// Use a worker pool pattern with controlled concurrency
	// For I/O-bound tasks like server startup, use more workers than CPU cores
	numWorkers := runtime.NumCPU() * 4
	if count < numWorkers {
		numWorkers = count
	}

	// Additional semaphore to limit concurrent ListenAndServe calls
	maxConcurrentServers := 500
	if count < maxConcurrentServers {
		maxConcurrentServers = count
	}
	semaphore := make(chan struct{}, maxConcurrentServers)

	log.Printf("Using %d workers with max %d concurrent server startups", numWorkers, maxConcurrentServers)

	var wg sync.WaitGroup
	started := int64(0)

	// Start worker goroutines
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for port := range portChan {
				// Acquire semaphore before starting server
				semaphore <- struct{}{}

				sp.startSingleServerOptimized(port, semaphore)

				current := atomic.AddInt64(&started, 1)
				if current%1000 == 0 || current == int64(count) {
					log.Printf("Progress: %d/%d servers started", current, count)
				}
			}
		}(w)
	}

	wg.Wait()
	log.Printf("Successfully started %d servers.", count)
	return nil
}

// startSingleServerOptimized creates and starts a single server on the given port with semaphore control.
func (sp *ServerPool) startSingleServerOptimized(port int, semaphore chan struct{}) {
	defer func() { <-semaphore }()

	id := atomic.AddInt64(&sp.counter, 1)

	server := &LightweightServer{
		ID:      id,
		Port:    port,
		running: 1,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&server.running) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status":"healthy","id":%d,"port":%d}`, server.ID, server.Port)
		} else {
			http.Error(w, "Server is not running", http.StatusServiceUnavailable)
		}
	})

	mux.HandleFunc("/kill", func(w http.ResponseWriter, r *http.Request) {
		if atomic.CompareAndSwapInt32(&server.running, 1, 0) {
			atomic.AddInt64(&sp.stats.TotalKilled, 1)
			atomic.AddInt64(&sp.stats.CurrentAlive, -1)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status":"killed","id":%d,"port":%d}`, server.ID, server.Port)

			go func() {
				// Shutdown the server in a separate goroutine to not block the kill request.
				if err := server.Server.Shutdown(context.Background()); err != nil {
					log.Printf("Error shutting down server %d on port %d: %v", server.ID, server.Port, err)
				}
			}()
		} else {
			http.Error(w, "Server already stopped", http.StatusBadRequest)
		}
	})

	server.Server = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	sp.mu.Lock()
	sp.servers[port] = server
	sp.mu.Unlock()

	atomic.AddInt64(&sp.stats.TotalStarted, 1)
	atomic.AddInt64(&sp.stats.CurrentAlive, 1)

	go func() {
		if err := server.Server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Server %d on port %d failed: %v", server.ID, server.Port, err)
			atomic.StoreInt32(&server.running, 0)
			atomic.AddInt64(&sp.stats.CurrentAlive, -1)
		}
	}()
}

// startSingleServer creates and starts a single server on the given port.
func (sp *ServerPool) startSingleServer(port int) {
	id := atomic.AddInt64(&sp.counter, 1)

	server := &LightweightServer{
		ID:      id,
		Port:    port,
		running: 1,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&server.running) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status":"healthy","id":%d,"port":%d}`, server.ID, server.Port)
		} else {
			http.Error(w, "Server is not running", http.StatusServiceUnavailable)
		}
	})

	mux.HandleFunc("/kill", func(w http.ResponseWriter, r *http.Request) {
		if atomic.CompareAndSwapInt32(&server.running, 1, 0) {
			atomic.AddInt64(&sp.stats.TotalKilled, 1)
			atomic.AddInt64(&sp.stats.CurrentAlive, -1)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status":"killed","id":%d,"port":%d}`, server.ID, server.Port)

			go func() {
				// Shutdown the server in a separate goroutine to not block the kill request.
				if err := server.Server.Shutdown(context.Background()); err != nil {
					log.Printf("Error shutting down server %d on port %d: %v", server.ID, server.Port, err)
				}
			}()
		} else {
			http.Error(w, "Server already stopped", http.StatusBadRequest)
		}
	})

	server.Server = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	sp.mu.Lock()
	sp.servers[port] = server
	sp.mu.Unlock()

	atomic.AddInt64(&sp.stats.TotalStarted, 1)
	atomic.AddInt64(&sp.stats.CurrentAlive, 1)

	go func() {
		if err := server.Server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Server %d on port %d failed: %v", server.ID, server.Port, err)
			atomic.StoreInt32(&server.running, 0)
			atomic.AddInt64(&sp.stats.CurrentAlive, -1)
		}
	}()
}

// findAvailablePort finds an unused port in the pool's range.
func (sp *ServerPool) findAvailablePort() int {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	for port := sp.basePort; port <= sp.maxPort; port++ {
		if _, exists := sp.servers[port]; !exists {
			// Mark the port as used immediately to prevent race conditions
			sp.servers[port] = &LightweightServer{Port: port}
			return port
		}
	}
	return -1
}

// GetStats returns the current statistics of the server pool.
func (sp *ServerPool) GetStats() map[string]interface{} {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	return map[string]interface{}{
		"total_started": atomic.LoadInt64(&sp.stats.TotalStarted),
		"total_killed":  atomic.LoadInt64(&sp.stats.TotalKilled),
		"current_alive": atomic.LoadInt64(&sp.stats.CurrentAlive),
		"registered":    len(sp.servers),
		"max_possible":  sp.maxPort - sp.basePort + 1,
		"uptime_ms":     time.Since(sp.startTime).Milliseconds(),
	}
}

// GetEndpoints returns a list of health check endpoints for running servers.
func (sp *ServerPool) GetEndpoints(limit int) []string {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	endpoints := make([]string, 0, limit)
	count := 0

	for _, srv := range sp.servers {
		if count >= limit {
			break
		}
		if atomic.LoadInt32(&srv.running) == 1 {
			endpoints = append(endpoints, fmt.Sprintf("http://localhost:%d/health", srv.Port))
			count++
		}
	}
	return endpoints
}

// ShutdownAll gracefully shuts down all running servers in the pool.
func (sp *ServerPool) ShutdownAll() {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	var wg sync.WaitGroup
	for _, srv := range sp.servers {
		if atomic.LoadInt32(&srv.running) == 1 {
			wg.Add(1)
			go func(server *LightweightServer) {
				defer wg.Done()
				if err := server.Server.Shutdown(context.Background()); err != nil {
					log.Printf("Error during shutdown of server %d: %v", server.ID, err)
				}
			}(srv)
		}
	}
	wg.Wait()
}

func (sp *ServerPool) ReviveServer(port int) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	server, exists := sp.servers[port]
	if !exists {
		return fmt.Errorf("server on port %d not found", port)
	}

	if atomic.LoadInt32(&server.running) == 1 {
		return fmt.Errorf("server on port %d is already running", port)
	}

	// Re-create the server and handler
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&server.running) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status":"healthy","id":%d,"port":%d}`, server.ID, server.Port)
		} else {
			http.Error(w, "Server is not running", http.StatusServiceUnavailable)
		}
	})

	mux.HandleFunc("/kill", func(w http.ResponseWriter, r *http.Request) {
		if atomic.CompareAndSwapInt32(&server.running, 1, 0) {
			atomic.AddInt64(&sp.stats.TotalKilled, 1)
			atomic.AddInt64(&sp.stats.CurrentAlive, -1)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status":"killed","id":%d,"port":%d}`, server.ID, server.Port)

			go func() {
				// Shutdown the server in a separate goroutine to not block the kill request.
				if err := server.Server.Shutdown(context.Background()); err != nil {
					log.Printf("Error shutting down server %d on port %d: %v", server.ID, server.Port, err)
				}
			}()
		} else {
			http.Error(w, "Server already stopped", http.StatusBadRequest)
		}
	})

	server.Server = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	atomic.StoreInt32(&server.running, 1)
	atomic.AddInt64(&sp.stats.CurrentAlive, 1)

	go func() {
		if err := server.Server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Server %d on port %d failed: %v", server.ID, server.Port, err)
			atomic.StoreInt32(&server.running, 0)
			atomic.AddInt64(&sp.stats.CurrentAlive, -1)
		}
	}()

	log.Printf("Revived server %d on port %d", server.ID, port)
	return nil
}

func main() {
	defaultCount := 100
	defaultBasePort := 10000
	defaultMaxPort := 60000

	countFlag := flag.Int("count", defaultCount, "Number of servers to start")
	basePortFlag := flag.Int("base-port", defaultBasePort, "Base port for listener allocation")
	maxPortFlag := flag.Int("max-port", defaultMaxPort, "Maximum port for listener allocation")
	flag.Parse()

	startCount := *countFlag
	basePort := *basePortFlag
	maxPort := *maxPortFlag

	if len(flag.Args()) > 0 {
		if count, err := strconv.Atoi(flag.Arg(0)); err == nil {
			startCount = count
		} else {
			log.Fatalf("Invalid count argument: %v", err)
		}
	}

	if len(flag.Args()) > 1 {
		if bp, err := strconv.Atoi(flag.Arg(1)); err == nil {
			basePort = bp
		} else {
			log.Fatalf("Invalid base port argument: %v", err)
		}
	}

	if len(flag.Args()) > 2 {
		if mp, err := strconv.Atoi(flag.Arg(2)); err == nil {
			maxPort = mp
		} else {
			log.Fatalf("Invalid max port argument: %v", err)
		}
	}

	if startCount <= 0 {
		log.Fatalf("Count must be a positive integer (received %d)", startCount)
	}

	if basePort < 2 {
		log.Fatalf("Base port must be >= 2 to derive management port (received %d)", basePort)
	}

	if maxPort < basePort {
		log.Fatalf("Max port (%d) must be greater than or equal to base port (%d)", maxPort, basePort)
	}

	maxPossible := maxPort - basePort + 1
	if startCount > maxPossible {
		log.Fatalf("Requested %d servers but only %d ports available in range %d-%d", startCount, maxPossible, basePort, maxPort)
	}

	log.Printf("=== Server Pool Manager Starting ===")
	log.Printf("Target servers to start: %d", startCount)
	log.Printf("Port range: %d-%d", basePort, maxPort)

	pool := NewServerPool(basePort, maxPort)

	startTime := time.Now()
	if err := pool.StartServers(startCount); err != nil {
		log.Fatalf("Failed to start servers: %v", err)
	}
	elapsed := time.Since(startTime)
	log.Printf("Completed startup of %d servers in %v", startCount, elapsed)

	// Management interface
	mgmtPort := basePort - 1
	mgmtMux := http.NewServeMux()

	mgmtMux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pool.GetStats())
	})

	mgmtMux.HandleFunc("/endpoints", func(w http.ResponseWriter, r *http.Request) {
		limit := 1000
		if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
			limit = l
		}
		endpoints := pool.GetEndpoints(limit)
		w.Header().Set("Content-Type", "text/plain")
		for _, ep := range endpoints {
			fmt.Fprintln(w, ep)
		}
	})

	mgmtMux.HandleFunc("/scale", func(w http.ResponseWriter, r *http.Request) {
		count, err := strconv.Atoi(r.URL.Query().Get("count"))
		if err != nil {
			http.Error(w, "Invalid 'count' parameter", http.StatusBadRequest)
			return
		}

		start := time.Now()
		if err := pool.StartServers(count); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		elapsed := time.Since(start)
		fmt.Fprintf(w, `{"status":"scaled","count":%d,"elapsed_ms":%d}`, count, elapsed.Milliseconds())
	})

	mgmtMux.HandleFunc("/revive", func(w http.ResponseWriter, r *http.Request) {
		portStr := r.URL.Query().Get("port")
		if portStr == "" {
			http.Error(w, "Missing 'port' parameter", http.StatusBadRequest)
			return
		}

		port, err := strconv.Atoi(portStr)
		if err != nil {
			http.Error(w, "Invalid 'port' parameter", http.StatusBadRequest)
			return
		}

		if err := pool.ReviveServer(port); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		fmt.Fprintf(w, `{"status":"revived","port":%d}`, port)
	})

	go func() {
		log.Printf("Management server listening on :%d", mgmtPort)
		if err := http.ListenAndServe(fmt.Sprintf(":%d", mgmtPort), mgmtMux); err != nil {
			log.Printf("Management server error: %v", err)
		}
	}()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("=== Shutdown Signal Received ===")
	shutdownStart := time.Now()
	pool.ShutdownAll()
	log.Printf("All servers have been shut down in %v.", time.Since(shutdownStart))
}
