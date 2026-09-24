package httpauth

import (
	"errors"
	"net/http"
	"testing"
)

func TestObservationRoutesPreserveReadScopeAndTransport(t *testing.T) {
	a := newFixture(t, configFixture(t))
	for _, path := range []string{"/api/v1/history", "/api/v1/slo", "/api/v1/state", "/api/v1/overview", "/api/v1/monitors", "/api/v1/monitors/42", "/api/v1/incidents", "/api/v1/systems", "/api/v1/queues", "/api/v1/queues/history", "/api/v1/pools", "/api/v1/pools/history", "/api/v1/config", "/api/v1/healthz", "/api/v1/readyz", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			r := requestFixture(http.MethodGet, path, readerToken)
			got, err := a.AuthorizeObservation(r)
			if err != nil || got.PrincipalID != "reader-one" {
				t.Fatalf("read permission lost: %+v %v", got, err)
			}
			if r.URL.Path != path {
				t.Fatal("request was mutated")
			}
			r.TLS = nil
			if _, err := a.AuthorizeObservation(r); !errors.Is(err, ErrForbidden) {
				t.Fatal("cleartext named credential accepted", err)
			}
		})
	}
	for _, path := range []string{"/api/v1/monitors/1/disable", "/api/v1/monitors/", "/api/v1/unknown", "/api/v2/monitors"} {
		if _, err := a.AuthorizeObservation(requestFixture(http.MethodGet, path, operatorToken)); !errors.Is(err, ErrForbidden) {
			t.Fatal(path, err)
		}
	}
	if _, err := a.AuthorizeObservation(requestFixture(http.MethodPost, "/api/v1/monitors", operatorToken)); !errors.Is(err, ErrForbidden) {
		t.Fatal("write accepted", err)
	}
}
