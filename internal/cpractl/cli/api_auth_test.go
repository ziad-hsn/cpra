package cli

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAPIHTTPSUsesConfiguredCA(t *testing.T) {
	t.Setenv("CPRA_AUTH_TOKEN", "api-tls-token")
	t.Setenv("CPRA_AUTH_TOKEN_FILE", "")
	t.Setenv("CPRA_CA_FILE", "")
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer api-tls-token" {
			t.Error("missing TLS authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true}`))
	}))
	defer server.Close()
	command, _ := newTestCommand(t, server.URL, "health")
	if err := command.Execute(); err == nil || requests.Load() != 0 {
		t.Fatalf("untrusted certificate was accepted: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	command, _ = newTestCommand(t, server.URL, "--ca-file", path, "health")
	if err := command.Execute(); err != nil || requests.Load() != 1 {
		t.Fatalf("configured TLS trust failed: %v", err)
	}
}

func TestAPIBearerRequiresExplicitHTTP(t *testing.T) {
	for _, source := range []string{"environment", "file"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("CPRA_AUTH_TOKEN", "")
			t.Setenv("CPRA_AUTH_TOKEN_FILE", "")
			const token = "private-api-token"
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("missing configured authentication")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"available":true}`))
			}))
			defer server.Close()
			args := []string{"health"}
			if source == "environment" {
				t.Setenv("CPRA_AUTH_TOKEN", token)
			} else {
				path := filepath.Join(t.TempDir(), "token")
				if err := os.WriteFile(path, []byte(token), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--token-file", path)
			}
			command, _ := newTestCommand(t, server.URL, args...)
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), "requires HTTPS") || strings.Contains(err.Error(), token) || requests.Load() != 0 {
				t.Fatalf("HTTP authentication was not rejected before sending: %v; requests=%d", err, requests.Load())
			}
			command, _ = newTestCommand(t, server.URL, append(args, "--allow-insecure-http")...)
			if err := command.Execute(); err != nil || requests.Load() != 1 {
				t.Fatalf("explicit HTTP authentication failed: %v; requests=%d", err, requests.Load())
			}
		})
	}
}

func TestAPIHTTPOptInDoesNotFollowRedirects(t *testing.T) {
	t.Setenv("CPRA_AUTH_TOKEN", "private-redirect-token")
	t.Setenv("CPRA_AUTH_TOKEN_FILE", "")
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true}`))
	}))
	defer target.Close()
	for _, sameOrigin := range []bool{false, true} {
		t.Run(map[bool]string{false: "cross-origin", true: "same-origin"}[sameOrigin], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/healthz" {
					requests.Add(1)
					return
				}
				location := target.URL + "/redirected"
				if sameOrigin {
					location = "/redirected"
				}
				http.Redirect(w, r, location, http.StatusFound)
			}))
			defer server.Close()
			command, _ := newTestCommand(t, server.URL, "--allow-insecure-http", "health")
			if err := command.Execute(); err == nil || requests.Load() != 0 {
				t.Fatalf("redirect was followed: %v; requests=%d", err, requests.Load())
			}
		})
	}
}

func TestAPITokenFileErrorIsRedacted(t *testing.T) {
	t.Setenv("CPRA_AUTH_TOKEN", "")
	t.Setenv("CPRA_AUTH_TOKEN_FILE", "")
	command, _ := newTestCommand(t, "https://localhost", "health", "--token-file", filepath.Join(t.TempDir(), "private-file-name"))
	err := command.Execute()
	if err == nil || strings.Contains(err.Error(), "private-file-name") {
		t.Fatalf("token file error was not redacted: %v", err)
	}
}
