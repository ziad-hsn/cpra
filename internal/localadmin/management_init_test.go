package localadmin

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func TestManagementInitializationProtectsCredentialsAndLocalTLS(t *testing.T) {
	l := testLayout(t)
	files, err := InitManagement(t.Context(), l, ManagementInitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	config, err := runtimeconfig.Load(files.Runtime)
	if err != nil || !config.Management.Enabled || config.Storage.Directory != l.StateDir {
		t.Fatal("management settings invalid", err)
	}
	token, err := secureconfig.ReadProtectedFile(t.Context(), secureconfig.ProtectedFileOptions{Path: files.Token, DataDirectory: l.StateDir, MaxBytes: 128})
	if err != nil || len(strings.TrimSpace(string(token))) != 64 {
		t.Fatal("operator token not protected", err)
	}
	defer clear(token)
	verifier, err := httpauth.HashToken(strings.TrimSpace(string(token)))
	if err != nil {
		t.Fatal(err)
	}
	policyRaw, err := os.ReadFile(config.Management.PolicyFile)
	if err != nil || strings.Contains(string(policyRaw), strings.TrimSpace(string(token))) {
		t.Fatal("policy leaked bearer", err)
	}
	policy, err := httpauth.ParsePolicy(policyRaw)
	if err != nil || len(policy.Principals) != 1 || policy.Principals[0].TokenSHA256 != verifier || policy.Principals[0].ID != "local/operator" || policy.LegacyTokenSHA256 != "" {
		t.Fatal("named authority invalid", err)
	}
	keyPath := config.Management.Encryption.Local.ActiveKeyFile
	if key, err := secureconfig.ReadProtectedFile(t.Context(), secureconfig.ProtectedFileOptions{Path: keyPath, DataDirectory: l.StateDir, MaxBytes: 32}); err != nil || len(key) != 32 {
		t.Fatal("wrapping key not protected", err)
	} else {
		clear(key)
	}
	material, err := tls.LoadX509KeyPair(config.Management.TLS.CertFile, config.Management.TLS.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(material.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(files.CA)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(caPEM)
	if block == nil {
		t.Fatal("local CA unavailable")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA || leaf.CheckSignatureFrom(ca) != nil {
		t.Fatal("local certificate chain invalid", err)
	}
	for _, name := range []string{"localhost", "127.0.0.1", "::1"} {
		if err := leaf.VerifyHostname(name); err != nil {
			t.Fatal("loopback certificate identity missing", err)
		}
	}
	for _, name := range []string{"raft.db", "node.json"} {
		if _, err := os.Lstat(filepath.Join(l.StateDir, name)); !os.IsNotExist(err) {
			t.Fatal("initialization opened durable storage", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(l.ConfigDir, "auth.token")); !os.IsNotExist(err) {
		t.Fatal("management initialization created a read-only bearer", err)
	}
	service, err := Render(l, "")
	if err != nil || strings.Contains(service, "-web.auth-file") || strings.Contains(service, "operator.token") {
		t.Fatal("service arguments misuse operator credential", err, service)
	}
	if _, err := InitManagement(t.Context(), l, ManagementInitOptions{}); err == nil {
		t.Fatal("repeat initialization replaced existing credentials")
	}
	again, err := os.ReadFile(files.Token)
	if err != nil || string(again) != string(token) {
		t.Fatal("failed initialization changed token", err)
	}
}

func TestManagementInitializationRejectsExistingStateAndServiceTokenPaths(t *testing.T) {
	for _, test := range []string{"existing-state", "missing-system-output", "system-config-output", "state-output"} {
		t.Run(test, func(t *testing.T) {
			l := testLayout(t)
			options := ManagementInitOptions{}
			switch test {
			case "existing-state":
				if err := os.MkdirAll(l.StateDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(l.StateDir, "retained"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-system-output":
				l.Scope = "system"
			case "system-config-output":
				l.Scope, options.TokenOutput = "system", filepath.Join(l.ConfigDir, "operator.token")
			case "state-output":
				options.TokenOutput = filepath.Join(l.StateDir, "operator.token")
			}
			if _, err := InitManagement(t.Context(), l, options); err == nil {
				t.Fatal("unsafe initialization accepted")
			}
			if _, err := os.Lstat(filepath.Join(l.ConfigDir, "runtime.yaml")); !os.IsNotExist(err) {
				t.Fatal("rejected request published runtime", err)
			}
		})
	}
}
