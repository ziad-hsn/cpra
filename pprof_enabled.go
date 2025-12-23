//go:build !noprofile

package main

import (
	"errors"
	"expvar"
	"net/http"
	"net/http/pprof"

	"cpra/internal/controller"
)

const defaultPprofEnabled = true

func setupPprof(enable bool, addr string) shutdowner {
	if !enable {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle("/debug/vars", expvar.Handler())

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		controller.SystemLogger.Infof("Profiling server listening at http://%s/debug/pprof/", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			controller.SystemLogger.Warnf("Profiling server error: %v", err)
		}
	}()

	return srv
}

// Ensure http.Server satisfies shutdowner.
var _ shutdowner = (*http.Server)(nil)

