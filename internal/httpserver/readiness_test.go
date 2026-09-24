package httpserver

import (
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDrainingChangesReadinessNotLiveness(t *testing.T) {
	s := newTestServer(t)
	var ready atomic.Bool
	ready.Store(true)
	s.cfg.Ready = ready.Load
	for _, state := range []bool{true, false} {
		ready.Store(state)
		for _, endpoint := range []string{"readyz", "healthz"} {
			w := httptest.NewRecorder()
			apiMux(s).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/"+endpoint, nil))
			want := 200
			if endpoint == "readyz" && !state {
				want = 503
			}
			if w.Code != want {
				t.Fatalf("ready=%v %s returned %d, want %d", state, endpoint, w.Code, want)
			}
		}
	}
}

func TestExplicitEmptyRuntimeCanBeReady(t *testing.T) {
	s := newTestServer(t)
	s.holder.Set(&fleetview.StatsSnapshot{Generated: time.Now()})
	for _, allow := range []bool{false, true} {
		s.cfg.AllowEmpty = allow
		w := httptest.NewRecorder()
		s.handleReadyz(w, httptest.NewRequest("GET", "/api/v1/readyz", nil))
		want := 503
		if allow {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("allow-empty=%v status=%d", allow, w.Code)
		}
	}
}

func TestReadinessDoesNotDependOnDashboardProjection(t *testing.T) {
	s := newTestServer(t)
	s.cfg.Ready = func() bool { return true }
	s.holder.Set(&fleetview.StatsSnapshot{Generated: time.Now().Add(-time.Hour)})
	w := httptest.NewRecorder()
	s.handleReadyz(w, httptest.NewRequest("GET", "/api/v1/readyz", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"projection_fresh":false`) {
		t.Fatalf("dashboard delay changed readiness: %d %s", w.Code, w.Body.String())
	}
}
