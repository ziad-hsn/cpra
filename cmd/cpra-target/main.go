// cpra-target is the independent network target and bounded audit collector for
// the scale campaign. It does not link the controller or substitute a transport.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	count := flag.Int("monitors", 1000000, "Distinct monitor IDs accepted")
	addr := flag.String("addr", "127.0.0.1:0", "Loopback target listener")
	flag.Parse()
	if *count < 1 {
		fmt.Fprintln(os.Stderr, "monitors must be positive")
		os.Exit(1)
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		fmt.Fprintln(os.Stderr, "target listener must use a loopback IP")
		os.Exit(1)
	}
	counts := make([]atomic.Uint64, *count)
	last := make([]atomic.Int64, *count)
	var received, completed, active, peak, early, actions atomic.Uint64
	var delay atomic.Int64
	var outage atomic.Bool
	var cohort atomic.Int64
	cohort.Store(int64(*count))
	var faultEpoch atomic.Uint64
	var duplicates atomic.Uint64
	actionEpoch := make([]atomic.Uint64, *count)
	actionCounts := make([]atomic.Uint64, *count)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /check/", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/check/"))
		if err != nil || n < 0 || n >= len(counts) {
			http.Error(w, "unknown monitor", 404)
			return
		}
		now := time.Now().UnixNano()
		previous := last[n].Swap(now)
		if previous != 0 && now-previous < int64(30*time.Second) {
			early.Add(1)
		}
		counts[n].Add(1)
		received.Add(1)
		current := active.Add(1)
		for p := peak.Load(); current > p && !peak.CompareAndSwap(p, current); p = peak.Load() {
		}
		defer func() { active.Add(^uint64(0)); completed.Add(1) }()
		if d := delay.Load(); d > 0 && int64(n) < cohort.Load() {
			timer := time.NewTimer(time.Duration(d))
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
		}
		if outage.Load() && int64(n) < cohort.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /action/", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/action/"))
		if err != nil || n < 0 || n >= len(actionCounts) {
			http.Error(w, "unknown action target", 404)
			return
		}
		epoch := faultEpoch.Load()
		if actionEpoch[n].Swap(epoch) == epoch {
			duplicates.Add(1)
		}
		actionCounts[n].Add(1)
		actions.Add(1)
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /fault", func(w http.ResponseWriter, r *http.Request) {
		d, err := time.ParseDuration(r.URL.Query().Get("delay"))
		if err != nil {
			http.Error(w, "invalid delay", 400)
			return
		}
		if d < 0 {
			http.Error(w, "invalid delay", 400)
			return
		}
		limit := *count
		if raw := r.URL.Query().Get("cohort"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > *count {
				http.Error(w, "invalid cohort", 400)
				return
			}
			limit = n
		}
		cohort.Store(int64(limit))
		if r.URL.Query().Get("new_episode") == "true" {
			faultEpoch.Add(1)
		}
		delay.Store(int64(d))
		outage.Store(r.URL.Query().Get("outage") == "true")
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /audit", func(w http.ResponseWriter, r *http.Request) {
		result := map[string]any{"at": time.Now().UTC(), "configured_monitors": *count, "received": received.Load(), "completed": completed.Load(), "active": active.Load(), "peak_active": peak.Load(), "early_checks": early.Load(), "actions": actions.Load(), "duplicate_actions": duplicates.Load(), "fault_epoch": faultEpoch.Load(), "goroutines": runtime.NumGoroutine()}
		if r.URL.Query().Get("digest") == "true" {
			h := sha256.New()
			var b [8]byte
			var distinct uint64
			minimum := ^uint64(0)
			maximum := uint64(0)
			for n := range counts {
				c := counts[n].Load()
				if c > 0 {
					distinct++
				}
				minimum = min(minimum, c)
				maximum = max(maximum, c)
				binary.BigEndian.PutUint64(b[:], c)
				h.Write(b[:])
			}
			cohortActions := make([]uint64, min(100, *count))
			for n := range cohortActions {
				cohortActions[n] = actionCounts[n].Load()
			}
			result["cohort_actions"] = cohortActions
			result["distinct"], result["minimum_checks"], result["maximum_checks"], result["count_digest"] = distinct, minimum, maximum, hex.EncodeToString(h.Sum(nil))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Println("http://" + listener.Addr().String())
	if err = server.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
