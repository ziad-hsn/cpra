package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

// TestManagementBrowser is deliberately opt-in and never downloads a browser.
// It exercises real HTTP admission, authentication and encrypted Raft storage.
// No controller runs: every mutation must remain committed, never "applied".
// Serving private dist assets here avoids replacing the release's embedded assets.
func TestManagementBrowser(t *testing.T) {
	module := os.Getenv("CPRA_BROWSER_MODULE")
	if module == "" {
		if os.Getenv("CPRA_BROWSER_REQUIRED") == "1" {
			t.Fatal("required browser harness is missing CPRA_BROWSER_MODULE")
		}
		t.Skip("set CPRA_BROWSER_MODULE to an installed Playwright package to run the private browser fixture")
	}
	node, browser := os.Getenv("CPRA_BROWSER_NODE"), os.Getenv("CPRA_BROWSER_EXECUTABLE")
	for name, path := range map[string]string{"Playwright package": module, "Node executable": node, "browser executable": browser} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s requires an explicit absolute installed path", name)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s is unavailable", name)
		}
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(root, "dashboard", "dist")
	index, err := os.ReadFile(filepath.Join(dist, "index.html"))
	if err != nil {
		t.Fatal("build dashboard/dist before running this fixture")
	}
	indexDigest := sha256.Sum256(index)
	t.Logf("private dist index sha256=%x; compiler=%s; platform=%s/%s", indexDigest, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = filepath.Join(t.TempDir(), "state")
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := management.NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	operator, _ := httpauth.HashToken(managementOperatorToken)
	reader, _ := httpauth.HashToken(managementReaderToken)
	auth, err := httpauth.New(httpauth.Config{Principals: []httpauth.Principal{
		{ID: "operator", Role: httpauth.Operator, TokenSHA256: operator},
		{ID: "reader", Role: httpauth.Reader, TokenSHA256: reader},
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := New(ServerConfig{Management: catalog, ManagementAuth: auth, Store: store, Ready: func() bool { return true }}, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	mux := http.NewServeMux()
	s.registerAPI(mux)
	s.registerMetrics(mux)
	files := http.FileServer(http.Dir(dist))
	assets := os.DirFS(dist)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if fs.ValidPath(path) && !strings.Contains(path, "\\") {
			if info, err := fs.Stat(assets, path); err == nil && info.Mode().IsRegular() {
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
	transport := httptest.NewTLSServer(s.corsMiddleware(s.authMiddleware(mux)))
	t.Cleanup(transport.Close)
	certificate, err := x509.ParseCertificate(transport.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, node, filepath.Join(root, "scripts", "dashboard", "verify_browser.cjs"))
	command.Env = append(os.Environ(), "CPRA_BROWSER_ORIGIN="+transport.URL, "CPRA_BROWSER_CERT_PIN="+base64.StdEncoding.EncodeToString(pin[:]))
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("private browser fixture failed: %v\n%s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
	// The browser deliberately supplies this synthetic marker as a secret.
	// Check real durable files as well as the browser's read/URL/storage checks.
	if err := filepath.WalkDir(cfg.Storage.Directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte("browser-fixture-private-value")) {
			t.Error("plaintext synthetic credential found in durable state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
