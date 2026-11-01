package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestMainRestoredAccessKeepsExplicitMode(t *testing.T) {
	for _, mode := range []string{"management", "observation"} {
		t.Run(mode, func(t *testing.T) {
			f := newMainManagementFixture(t)
			scheme := "https://"
			oldToken := startupOperatorToken
			if mode == "observation" {
				f.settings.Management = runtimeconfig.Management{}
				f.client = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
				f.options.webAuth = startupReaderToken
				oldToken, scheme = startupReaderToken, "http://"
			}
			writeMainRuntime(t, f.options.runtimeFile, f.settings)
			_, stop := startMainManagement(t, f)
			stop()
			original := f.settings.Storage.Directory
			private := filepath.Dir(f.options.runtimeFile)
			keyPath := filepath.Join(private, "backup.key")
			key := make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(keyPath, key, 0600); err != nil {
				t.Fatal(err)
			}
			clear(key)
			backup, restored := original+"-backup", original+"-restored"
			if err := localadmin.Backup(original, backup, f.options.runtimeFile, keyPath); err != nil {
				t.Fatal(err)
			}
			if err := localadmin.Restore(backup, restored, keyPath); err != nil {
				t.Fatal(err)
			}
			f.settings.Storage.Directory = restored
			writeMainRuntime(t, f.options.runtimeFile, f.settings)
			if err := runCPRa(t.Context(), f.options, func() { t.Error("unprovisioned restored owner became ready") }); !errors.Is(err, persistence.ErrAuthenticationResetRequired) {
				t.Fatal("restored authentication did not require explicit provisioning", err)
			}
			request := localadmin.AuthenticationRequest{Action: "reprovision", Mode: mode, DataDirectory: restored, TokenOutput: filepath.Join(private, "restored.token")}
			if mode == "management" {
				request.PrincipalID, request.Role = "restored-operator", "operator"
			}
			report, err := localadmin.AdministerAuthentication(t.Context(), request)
			if err != nil || report.ResetRequired || report.Outcome != "committed" {
				t.Fatal("mode-aware stopped provisioning failed", err)
			}
			tokenBytes, err := os.ReadFile(request.TokenOutput)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(tokenBytes)
			newToken := strings.TrimSpace(string(tokenBytes))
			_, stop = startMainManagement(t, f)
			defer stop()
			get := func(path, token string) (int, []byte) {
				t.Helper()
				r, err := http.NewRequestWithContext(t.Context(), "GET", scheme+f.options.webAddr+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				response, err := f.client.Do(r)
				if err != nil {
					t.Fatal("restored HTTP request failed", err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
				if err != nil {
					t.Fatal(err)
				}
				return response.StatusCode, body
			}
			for _, path := range []string{"/api/v1/state", "/api/v2/readyz", "/api/v2/self"} {
				if status, _ := get(path, newToken); status != http.StatusOK {
					t.Fatal("fresh restored authority cannot read", path, status)
				}
				revoked := []string{oldToken, ""}
				if mode == "management" {
					revoked = append(revoked, startupReaderToken)
				}
				for _, token := range revoked {
					if status, _ := get(path, token); status != http.StatusUnauthorized {
						t.Fatal("restore resurrected old or anonymous authority", path, status)
					}
				}
			}
			status, body := get("/api/v2/access", "")
			var access struct {
				Mode                   string `json:"mode"`
				AuthenticationRequired bool   `json:"authenticationRequired"`
			}
			if status != http.StatusOK || json.Unmarshal(body, &access) != nil || access.Mode != mode || !access.AuthenticationRequired {
				t.Fatal("restore changed the selected access mode", status, access)
			}
		})
	}
}
