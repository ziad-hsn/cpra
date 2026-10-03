package httpauth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestRequestAuthenticationHonorsPolicyLockDeadline(t *testing.T) {
	for _, operation := range []string{"authorize", "admission", "policy-admission", "observation"} {
		t.Run(operation, func(t *testing.T) {
			a := newFixture(t, configFixture(t))
			generation := a.Generation()
			a.mu.Lock()
			var release sync.Once
			unlock := func() { release.Do(a.mu.Unlock) }
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			finished, result := make(chan struct{}), make(chan error, 1)
			var callbacks atomic.Int32
			t.Cleanup(func() { cancel(); unlock(); <-finished })
			go func() {
				defer close(finished)
				r := requestFixture(http.MethodGet, "/api/v2/monitors", readerToken).WithContext(ctx)
				var err error
				switch operation {
				case "authorize":
					_, err = a.Authorize(r, "ListMonitors")
				case "admission":
					err = a.WithAdmission(r, "ListMonitors", func(api.AccessInfo) error { callbacks.Add(1); return nil })
				case "policy-admission":
					err = a.WithPolicyAdmission(r, "ListMonitors", func(api.AccessInfo, uint64) error { callbacks.Add(1); return nil })
				case "observation":
					r.URL.Path = "/api/v1/monitors"
					_, err = a.AuthorizeObservation(r)
				}
				result <- err
			}()
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) || callbacks.Load() != 0 {
					t.Fatal("expired policy wait admitted work", err, callbacks.Load())
				}
			case <-time.After(time.Second):
				t.Fatal("authentication waited past its request deadline")
			}
			unlock()
			if a.Generation() != generation {
				t.Fatal("request timeout changed authentication policy")
			}
			if _, err := a.Authorize(requestFixture(http.MethodGet, "/api/v2/monitors", readerToken), "ListMonitors"); err != nil {
				t.Fatal("request timeout prevented subsequent authentication", err)
			}
		})
	}
}

func TestAdmissionCancellationDuringAuthenticationSkipsCallback(t *testing.T) {
	a := newFixture(t, configFixture(t))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a.now = func() time.Time { cancel(); return time.Now() }
	r := requestFixture(http.MethodPost, "/api/v2/monitors", operatorToken).WithContext(ctx)
	called := false
	err := a.WithPolicyAdmission(r, "CreateMonitor", func(api.AccessInfo, uint64) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("cancellation during authentication reached admission", err, called)
	}
}

func TestCanceledAdmissionKeepsPolicyBarrierUntilCallbackReturns(t *testing.T) {
	a := newFixture(t, configFixture(t))
	ctx, cancel := context.WithCancel(t.Context())
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { cancel(); unblock(); <-finished })
	go func() {
		defer close(finished)
		r := requestFixture(http.MethodPost, "/api/v2/monitors", operatorToken).WithContext(ctx)
		result <- a.WithAdmission(r, "CreateMonitor", func(api.AccessInfo) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("admission did not enter its callback")
	}
	cancel()
	config := configFixture(t)
	config.Principals[1].Revoked = true
	replacement, replaced := make(chan error, 1), make(chan struct{})
	t.Cleanup(func() { unblock(); <-replaced })
	go func() { defer close(replaced); replacement <- a.Replace(config) }()
	select {
	case err := <-replacement:
		t.Fatal("cancellation released an active admission barrier", err)
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	if err := <-result; err != nil {
		t.Fatal("accepted callback result changed", err)
	}
	if err := <-replacement; err != nil {
		t.Fatal(err)
	}
	called := false
	err := a.WithAdmission(requestFixture(http.MethodPost, "/api/v2/monitors", operatorToken), "CreateMonitor", func(api.AccessInfo) error { called = true; return nil })
	if !errors.Is(err, ErrUnauthorized) || called {
		t.Fatal("replacement policy did not prevent new admission", err, called)
	}
}
